package supervisor

import (
	"context"
	"sort"
)

// WorkflowResult is the final, Core-derived outcome of running a Workflow
// to completion (or to a limit). Succeeded/Completed are computed by
// WorkflowStatus from the authoritative TaskGraph - never from Synthesis,
// which is carried here purely as informational output for a caller to
// display, not as the source of truth for whether the workflow actually
// succeeded.
type WorkflowResult struct {
	Completed bool
	Succeeded bool
	Synthesis Synthesis
	Events    []ExecutionEvent
	Ticks     int
	Replans   int
	BlockedAt []TaskID
}

// replanTriggerKinds are the ExecutionEventKinds that warrant asking the
// Supervisor to Replan. Everything else (TaskReady/TaskAssigned/
// TaskStarted/TaskSucceeded/FindingProduced without a blocking
// implication) is informational only.
var replanTriggerKinds = map[ExecutionEventKind]bool{
	EventTaskFailed:            true,
	EventTaskBlocked:           true,
	EventAgentUnavailable:      true,
	EventAuthorizationRejected: true,
	EventExecutionTimedOut:     true,
}

// Workflow drives Goal -> Plan -> Scheduler ticks -> event-driven Replan
// -> Core-derived completion -> Synthesize. It is a deterministic Go
// orchestrator (no LLM calls of its own) - Supervisor remains the only
// LLM-backed component, and every proposal Workflow receives from it
// still passes through the unmodified Applier (validate -> authorize ->
// atomic apply) before anything happens. Workflow adds coordination, not
// authority: it cannot cause a task to execute, an agent to be assigned,
// or a workflow to be reported successful except via Core's own,
// independently re-derived state.
type Workflow struct {
	Supervisor Supervisor
	Scheduler  *Scheduler
	Limits     SupervisorLimits
}

// NewWorkflow constructs a Workflow with explicit, required dependencies.
func NewWorkflow(sv Supervisor, scheduler *Scheduler, limits SupervisorLimits) *Workflow {
	return &Workflow{Supervisor: sv, Scheduler: scheduler, Limits: limits}
}

// Run executes one goal to completion, to a workflow-tick limit, or until
// an unresolvable Apply failure occurs (e.g. the replan limit is
// exhausted). It never returns Succeeded=true unless WorkflowStatus
// independently confirms it from the current TaskGraph.
func (w *Workflow) Run(ctx context.Context, goal Goal) (WorkflowResult, error) {
	initial := w.Scheduler.Applier.State.Snapshot()
	planProposals, err := w.Supervisor.Plan(ctx, goal, initial)
	if err != nil {
		return WorkflowResult{}, err
	}
	if _, err := w.Scheduler.Applier.Apply(ctx, planProposals); err != nil {
		return WorkflowResult{}, err
	}

	maxTicks := w.Limits.MaxWorkflowTicks
	if maxTicks <= 0 {
		maxTicks = DefaultSupervisorLimits().MaxWorkflowTicks
	}

	var allEvents []ExecutionEvent
	replans := 0
	ticks := 0

	for {
		ticks++
		if ticks > maxTicks {
			return WorkflowResult{Events: allEvents, Ticks: ticks - 1, Replans: replans},
				&WorkflowLimitExceeded{Guardrail: "max_workflow_ticks", Limit: maxTicks, Actual: ticks}
		}

		tickEvents, err := w.Scheduler.Tick(ctx)
		if err != nil {
			return WorkflowResult{Events: allEvents, Ticks: ticks, Replans: replans}, err
		}
		allEvents = append(allEvents, tickEvents...)

		// A failure/blocked event from THIS tick gets one Replan
		// opportunity BEFORE completion is evaluated - otherwise a
		// workflow where every currently-known task happens to already
		// be terminal (e.g. the only tasks so far are T1/T2/T3, and T2
		// just failed) would be declared complete without ever letting
		// the Supervisor propose remediation.
		if trigger := firstReplanTrigger(tickEvents); trigger != nil {
			snapshot := w.Scheduler.Applier.State.Snapshot()
			replanProposals, err := w.Supervisor.Replan(ctx, snapshot, *trigger)
			if err != nil {
				return WorkflowResult{Events: allEvents, Ticks: ticks, Replans: replans}, err
			}
			if len(replanProposals) > 0 {
				if _, err := w.Scheduler.Applier.Apply(ctx, replanProposals); err != nil {
					// A rejected replan (e.g. ReplanLimitExceeded) ends
					// the workflow here - Core's guardrail is
					// authoritative, not a reason to keep looping.
					return WorkflowResult{Events: allEvents, Ticks: ticks, Replans: replans}, err
				}
				replans++
			}
			// Whether or not the Supervisor proposed anything, re-tick
			// before evaluating completion: either new tasks now exist
			// (not complete yet) or the Supervisor declined to remediate
			// (proposals empty), in which case the next iteration's
			// completion check correctly reports the failure as final.
			continue
		}

		snapshot := w.Scheduler.Applier.State.Snapshot()
		complete, succeeded, blocked := WorkflowStatus(snapshot.Tasks)
		if complete {
			synth, _ := w.Supervisor.Synthesize(ctx, snapshot)
			return WorkflowResult{
				Completed: true, Succeeded: succeeded, Synthesis: synth,
				Events: allEvents, Ticks: ticks, Replans: replans, BlockedAt: blocked,
			}, nil
		}
		// Not complete, and no replan trigger this tick: either still
		// waiting on in-flight tasks (progress will show up on a later
		// tick) or genuinely stuck - MaxWorkflowTicks bounds how long
		// this can continue either way.
	}
}

// firstReplanTrigger returns the first event in a tick's event list that
// warrants a Replan call, in a deterministic (stable, input) order.
func firstReplanTrigger(events []ExecutionEvent) *ExecutionEvent {
	candidates := make([]ExecutionEvent, 0, len(events))
	for _, e := range events {
		if replanTriggerKinds[e.Kind] {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].TaskID < candidates[j].TaskID })
	return &candidates[0]
}
