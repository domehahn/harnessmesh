package supervisor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ReadinessState is Scheduler's own, purely-computed view of whether a
// task can be dispatched right now. It is NEVER persisted as part of
// Task.Status - it is recomputed from the current TaskGraph on every
// Tick, from Dependencies and each dependency's current Status. This
// keeps "is this task ready" a derived fact Core always re-derives from
// authoritative state, never a cached flag that could go stale, and
// avoids adding new persisted Task.Status values (and therefore new
// state-transition preconditions) beyond what Phase 1/2A/2B already
// proved safe.
type ReadinessState string

const (
	// ReadinessReady: task is Pending (or has no Status set), and every
	// Dependency is TaskStatusCompleted. Eligible for assignment.
	ReadinessReady ReadinessState = "ready"
	// ReadinessWaiting: task is Pending, at least one Dependency has not
	// yet reached a terminal status.
	ReadinessWaiting ReadinessState = "waiting"
	// ReadinessBlocked: task is Pending, and at least one Dependency is
	// TaskStatusFailed. The task cannot become Ready through normal
	// dependency satisfaction - only a Supervisor Replan (creating a
	// remediation path, or retrying the failed dependency) can unblock
	// the workflow. A Blocked task is never dispatched.
	ReadinessBlocked ReadinessState = "blocked"
	// ReadinessActive: task is already Assigned or Executing.
	ReadinessActive ReadinessState = "active"
	// ReadinessDone: task is already terminal (Completed or Failed).
	ReadinessDone ReadinessState = "done"
)

// computeReadiness derives every task's ReadinessState from the current
// graph. It is a pure function - no I/O, no locking beyond what the
// caller already holds via a Snapshot.
func computeReadiness(graph TaskGraph) map[TaskID]ReadinessState {
	statusOf := make(map[TaskID]TaskStatus, len(graph.Tasks))
	for _, t := range graph.Tasks {
		statusOf[t.ID] = t.Status
	}

	result := make(map[TaskID]ReadinessState, len(graph.Tasks))
	for _, t := range graph.Tasks {
		switch t.Status {
		case TaskStatusCompleted, TaskStatusFailed:
			result[t.ID] = ReadinessDone
			continue
		case TaskStatusAssigned, TaskStatusExecuting:
			result[t.ID] = ReadinessActive
			continue
		}

		blocked := false
		waiting := false
		for _, dep := range t.Dependencies {
			switch statusOf[dep] {
			case TaskStatusCompleted:
				// satisfied
			case TaskStatusFailed:
				blocked = true
			default:
				waiting = true
			}
		}
		switch {
		case blocked:
			result[t.ID] = ReadinessBlocked
		case waiting:
			result[t.ID] = ReadinessWaiting
		default:
			result[t.ID] = ReadinessReady
		}
	}
	return result
}

// blockingDependency returns the first failed dependency of a blocked
// task, for DependencyBlocked event reporting.
func blockingDependency(graph TaskGraph, taskID TaskID) (TaskID, bool) {
	statusOf := make(map[TaskID]TaskStatus, len(graph.Tasks))
	for _, t := range graph.Tasks {
		statusOf[t.ID] = t.Status
	}
	task, ok := graph.ByID(taskID)
	if !ok {
		return "", false
	}
	for _, dep := range task.Dependencies {
		if statusOf[dep] == TaskStatusFailed {
			return dep, true
		}
	}
	return "", false
}

// routeAgent performs deterministic, capability-based agent selection:
// list every known agent, keep those whose declared capabilities are a
// superset of the task's RequiredCapabilities, and pick the
// lexicographically smallest AgentID among them. This is Core logic, not
// Supervisor logic - the Supervisor is never consulted here, and a
// Supervisor-proposed CandidateAgentID (if any) is not used by Scheduler;
// Scheduler always re-derives the assignment itself from capabilities.
// Where multiple agents qualify, selection is deterministic (not
// load-based, not historical/ML-based - that is explicitly out of scope
// for Phase 3).
func routeAgent(ctx context.Context, catalog AgentCatalog, task Task) (AgentID, error) {
	candidates, err := eligibleCandidates(ctx, catalog, task)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", &AgentRoutingFailure{TaskID: task.ID, Reason: fmt.Sprintf("no eligible agent for required capabilities %v", task.RequiredCapabilities)}
	}
	ids := make([]AgentID, len(candidates))
	for i, c := range candidates {
		ids[i] = c.AgentID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids[0], nil
}

// eligibleCandidates lists every known agent whose declared
// capabilities are a superset of task.RequiredCapabilities AND, for a
// tenant-scoped task, whose authoritative tenant matches task.Tenant -
// the single eligibility computation both routeAgent (deterministic
// fallback) and an optional AgentDecisionAdvisor (Phase 7B) see. The
// tenant filter (ADR 0006 Phase 7C) exists so a cross-tenant agent is
// removed BEFORE it could ever reach the advisor - Decision-data
// isolation, not just execution-time safety, since Executor's own
// TOCTOU tenant re-check (unchanged, still the authoritative gate at
// dispatch time) only prevents the WRONG agent from actually running, it
// does not prevent the wrong agent's existence from being visible to
// advisory ranking/telemetry in the first place.
//
// For an unscoped task (task.Tenant == ""), behavior is unchanged from
// Phase 3 - every capability-eligible agent is a candidate regardless of
// tenant.
func eligibleCandidates(ctx context.Context, catalog AgentCatalog, task Task) ([]AgentCandidate, error) {
	agents, err := catalog.ListAgents(ctx)
	if err != nil {
		return nil, &AgentRoutingFailure{TaskID: task.ID, Reason: fmt.Sprintf("listing agents: %v", err)}
	}

	var td TenantDirectory
	if task.Tenant != "" {
		t, ok := catalog.(TenantDirectory)
		if !ok {
			// Fail closed, mirroring Executor.Execute's identical
			// rationale: a tenant-scoped task with no way to confirm any
			// candidate's tenant membership must produce NO candidates,
			// never "include all and let Executor decide later."
			return nil, &AuthorizationFailure{TaskID: task.ID, Reason: fmt.Sprintf("task requires tenant %q but no TenantDirectory is configured - failing closed", task.Tenant)}
		}
		td = t
	}

	var eligible []AgentCandidate
	for _, a := range agents {
		if len(MissingCapabilities(a.Capabilities, task.RequiredCapabilities)) != 0 {
			continue
		}
		if td != nil {
			tenant, known := td.TenantOf(a.AgentID)
			if !known || tenant != task.Tenant {
				continue // cross-tenant agent never becomes a candidate
			}
		}
		eligible = append(eligible, AgentCandidate{AgentID: a.AgentID, Capabilities: a.Capabilities})
	}
	return eligible, nil
}

// selectAgent is Phase 7B's decision point: it always computes the
// existing deterministic fallback first (identical to routeAgent), and
// ONLY IF an advisor is configured and enabled does it also consult it.
// The advisor's output can never widen the candidate set (it is shown
// exactly the same eligible list the fallback was computed from) and
// can never be used without Core re-validating the recommendation is
// still present in that same list (TOCTOU). In AgentDecisionShadow
// mode, the fallback is always what gets returned - the advisor's top
// pick is only recorded as an event. In AgentDecisionAdvisory mode, the
// advisor's (re-validated) top pick is returned if usable, with an
// explicit event either way (advisory_selection or advisor_fallback).
func (s *Scheduler) selectAgent(ctx context.Context, task Task) (AgentID, []ExecutionEvent, error) {
	candidates, err := eligibleCandidates(ctx, s.Catalog, task)
	if err != nil {
		return "", nil, err
	}
	if len(candidates) == 0 {
		return "", nil, &AgentRoutingFailure{TaskID: task.ID, Reason: fmt.Sprintf("no eligible agent for required capabilities %v", task.RequiredCapabilities)}
	}
	ids := make([]AgentID, len(candidates))
	for i, c := range candidates {
		ids[i] = c.AgentID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	fallback := ids[0]
	legacyPolicy := PolicyProvenance{PolicyID: LegacyDeterministicPolicyID, PolicyVersion: LegacyDeterministicPolicyVersion}
	decisionID := NewDecisionID(task.ID)

	// capture is called exactly once, on every return path below, with
	// the FINAL candidate set (unchanged from this point on) and
	// whichever (agent, policy) this call is about to return - ADR
	// 0009's "every evaluation-eligible production decision
	// automatically captures the exact authoritative decision context"
	// requirement, implemented as a single choke point rather than
	// scattered calls that could drift out of sync with the return
	// value.
	//
	// It returns the FINAL selected agent and any additional events -
	// Phase 10's critical authority change lives entirely here: if
	// s.ExperimentAdmission admits a Challenger for this exact
	// DecisionID, THAT agent (re-validated against THIS tick's
	// candidates, exactly like an Advisor's pick already is) becomes
	// the return value every call site below must actually use - a
	// prior version of this function computed the override but never
	// propagated it to its own return statement, a real defect found
	// and fixed during this phase's security closure review.
	capture := func(agent AgentID, policy PolicyProvenance) (AgentID, []ExecutionEvent) {
		// Capture the immutable decision context first. Experiment admission
		// is deliberately downstream of this hook so its context gate can
		// require the persisted snapshot/evaluation record.
		s.captureDecisionWithID(ctx, decisionID, task, ids, agent, policy)
		if s.ExperimentAdmission == nil {
			return agent, nil
		}
		selected, admitted, admitErr := s.admitExperimentChallenger(ctx, task, decisionID, candidates, agent, policy)
		if admitErr != nil || !admitted {
			return agent, nil
		}
		if !isStillEligible(candidates, selected) {
			// Defense in depth: the admission gate itself is required to
			// only ever return a member of `candidates`, but this is
			// re-checked here regardless, exactly mirroring how an
			// Advisor's top pick is independently re-validated rather
			// than trusted - ADR 0009's TOCTOU discipline, now extended
			// to experiment admission.
			return agent, nil
		}
		return selected, []ExecutionEvent{{
			Kind: EventChallengerAdmitted, TaskID: task.ID,
			Detail: fmt.Sprintf("experiment-admitted challenger=%s champion=%s decision=%s", selected, agent, decisionID),
		}}
	}

	if s.Advisor == nil || s.AdvisorMode == "" || s.AdvisorMode == AgentDecisionDisabled {
		finalAgent, extra := capture(fallback, legacyPolicy)
		return finalAgent, extra, nil
	}

	tel := s.AdvisorTelemetry
	if tel == nil {
		tel = noopAdvisorTelemetry{}
	}

	advisorPolicyID, advisorPolicyVersion := s.advisorIdentity()

	tel.RecordAdvisorCall()
	advice, advErr := s.Advisor.Advise(ctx, task, candidates)
	if advErr != nil {
		tel.RecordAdvisorFailure()
		finalAgent, extra := capture(fallback, PolicyProvenance{PolicyID: legacyPolicy.PolicyID, PolicyVersion: legacyPolicy.PolicyVersion, AttemptedPolicyID: advisorPolicyID, AttemptedPolicyVersion: advisorPolicyVersion, FallbackUsed: true})
		return finalAgent, append([]ExecutionEvent{{Kind: EventAdvisorFailure, TaskID: task.ID, Detail: advErr.Error()}}, extra...), nil
	}
	if len(advice.Ranked) == 0 {
		finalAgent, extra := capture(fallback, PolicyProvenance{PolicyID: legacyPolicy.PolicyID, PolicyVersion: legacyPolicy.PolicyVersion, AttemptedPolicyID: advisorPolicyID, AttemptedPolicyVersion: advisorPolicyVersion, FallbackUsed: true})
		return finalAgent, extra, nil
	}
	top := advice.Ranked[0].AgentID

	switch s.AdvisorMode {
	case AgentDecisionShadow:
		tel.RecordShadowRecommendation()
		// Even in shadow mode (zero production effect on selection), the
		// recommendation named in telemetry/provenance must itself have
		// been eligible - a buggy or malicious advisor naming an agent
		// outside `candidates` (e.g. a tenant-filtered one) must never
		// have that ID surfaced into the event stream at all. ADR 0006
		// Phase 7C: "no cross-tenant IDs in shadow telemetry."
		if !isStillEligible(candidates, top) {
			finalAgent, extra := capture(fallback, legacyPolicy)
			return finalAgent, append([]ExecutionEvent{{
				Kind: EventAdvisorFailure, TaskID: task.ID,
				Detail: "shadow recommendation named an ineligible candidate - discarded, not recorded",
			}}, extra...), nil
		}
		// Shadow mode NEVER selects the advisor's pick - the champion
		// policy that actually produced `fallback` is still the legacy
		// fallback, always. The advisor's attempt is preserved as
		// provenance only. (Experiment admission, if any, is still
		// consulted via capture() - shadow-mode-for-the-Advisor and
		// experiment-admission-for-a-Challenger are independent
		// mechanisms; the former's "never selects" guarantee is about
		// the ADVISOR's pick specifically, not about whether a SEPARATE,
		// durably-admitted Challenger may become the selection.)
		finalAgent, extra := capture(fallback, PolicyProvenance{PolicyID: legacyPolicy.PolicyID, PolicyVersion: legacyPolicy.PolicyVersion, AttemptedPolicyID: advisorPolicyID, AttemptedPolicyVersion: advisorPolicyVersion})
		return finalAgent, append([]ExecutionEvent{{
			Kind: EventAdvisorShadowRecommendation, TaskID: task.ID,
			Detail: fmt.Sprintf("shadow recommendation=%s actual=%s basis=%s policy=%s", top, fallback, advice.Basis, s.AdvisorPolicyVersion),
		}}, extra...), nil

	case AgentDecisionAdvisory:
		// TOCTOU re-validation: the recommendation must still be present
		// in THIS tick's eligible candidate list (it already is, by
		// construction, since Advise only ever saw `candidates` - but
		// re-checking explicitly, rather than assuming, keeps this
		// correct even if a future Advisor implementation ever violated
		// that contract).
		if !isStillEligible(candidates, top) {
			tel.RecordAdvisorFallback()
			finalAgent, extra := capture(fallback, PolicyProvenance{PolicyID: legacyPolicy.PolicyID, PolicyVersion: legacyPolicy.PolicyVersion, AttemptedPolicyID: advisorPolicyID, AttemptedPolicyVersion: advisorPolicyVersion, FallbackUsed: true})
			return finalAgent, append([]ExecutionEvent{{
				Kind: EventAdvisorFallback, TaskID: task.ID,
				Detail: fmt.Sprintf("recommended agent %s no longer eligible, falling back to %s", top, fallback),
			}}, extra...), nil
		}
		tel.RecordAdvisorySelection()
		finalAgent, extra := capture(top, PolicyProvenance{PolicyID: advisorPolicyID, PolicyVersion: advisorPolicyVersion})
		return finalAgent, append([]ExecutionEvent{{
			Kind: EventAdvisorySelection, TaskID: task.ID,
			Detail: fmt.Sprintf("selected=%s basis=%s policy=%s", top, advice.Basis, s.AdvisorPolicyVersion),
		}}, extra...), nil

	default:
		finalAgent, extra := capture(fallback, legacyPolicy)
		return finalAgent, extra, nil
	}
}

// advisorIdentity returns the configured Advisor's own PolicyID/Version
// if it implements PolicyIdentifiable, else falls back to the single
// combined AdvisorPolicyVersion string as PolicyID with an empty
// Version - strictly better than capturing nothing, but coarser
// granularity than a conforming Advisor provides.
func (s *Scheduler) advisorIdentity() (id, version string) {
	if pi, ok := s.Advisor.(PolicyIdentifiable); ok {
		return pi.PolicyID(), pi.PolicyVersion()
	}
	return s.AdvisorPolicyVersion, ""
}

// admitExperimentChallenger calls s.ExperimentAdmission.Admit
// panic-recover-wrapped - ADR 0011's explicit fail-safe principle:
// "Experiment control failure -> no exploration," never "fail open to
// Challenger." A panic or error here is indistinguishable, from
// selectAgent's point of view, from "not admitted" - the Champion
// fallback is always what gets used in that case, and no budget is
// consumed (the panic/error happens before any store mutation the
// admission implementation itself would have performed).
func (s *Scheduler) admitExperimentChallenger(ctx context.Context, task Task, decisionID DecisionID, candidates []AgentCandidate, champion AgentID, policy PolicyProvenance) (selected AgentID, admitted bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			selected, admitted, err = "", false, fmt.Errorf("experiment admission panicked")
		}
	}()
	return s.ExperimentAdmission.Admit(ctx, task, decisionID, candidates, champion, policy)
}

// captureDecision builds and fires the RoutingDecisionContext for one
// production decision - a no-op if EvaluationMode is disabled or no
// observer is configured (the conservative default). Panic-isolated
// exactly like ExecutionObserver's own call site (ADR 0006 Phase 7C):
// an observer failure can never affect which agent selectAgent actually
// returns, since capture is called with that decision already made.
func (s *Scheduler) captureDecision(ctx context.Context, task Task, candidateIDs []AgentID, championAgentID AgentID, policy PolicyProvenance) {
	s.captureDecisionWithID(ctx, NewDecisionID(task.ID), task, candidateIDs, championAgentID, policy)
}

func (s *Scheduler) captureDecisionWithID(ctx context.Context, decisionID DecisionID, task Task, candidateIDs []AgentID, championAgentID AgentID, policy PolicyProvenance) {
	if s.EvaluationMode == "" || s.EvaluationMode == EvaluationDisabled || s.EvaluationObserver == nil {
		return
	}
	decision := RoutingDecisionContext{
		DecisionID:        decisionID,
		TaskID:            task.ID,
		Tenant:            task.Tenant,
		TaskClass:         task.Type,
		CandidateAgentIDs: append([]AgentID(nil), candidateIDs...),
		CapturedAt:        time.Now().UTC(),
		ChampionPolicy:    policy,
		ChampionAgentID:   championAgentID,
	}
	func() {
		defer func() {
			if r := recover(); r != nil && s.EvaluationFailureSink != nil {
				func() {
					defer func() { _ = recover() }()
					s.EvaluationFailureSink.RecordRoutingDecisionObserverPanic()
				}()
			}
		}()
		s.EvaluationObserver.ObserveRoutingDecision(ctx, decision)
	}()
}

// isStillEligible re-checks, against the SAME eligible-candidate list a
// Tick already computed, that candidateID is still present - the TOCTOU
// re-validation an advisor's recommendation must pass before Scheduler
// will use it in AgentDecisionAdvisory mode.
func isStillEligible(candidates []AgentCandidate, candidateID AgentID) bool {
	for _, c := range candidates {
		if c.AgentID == candidateID {
			return true
		}
	}
	return false
}

// Scheduler is Core's dependency-aware, concurrency-bounded dispatcher.
// It is the only thing that decides a task is actually executable - the
// Supervisor may propose task decomposition and assignments, but
// Scheduler independently re-derives readiness from the authoritative
// graph and independently re-derives the agent assignment from
// capabilities, through the existing, unmodified Applier (validate ->
// authorize -> atomic apply) and Executor (TOCTOU revalidation ->
// re-authorization -> dispatch). Scheduler adds no new authority: every
// state mutation it causes still goes through Applier/Executor exactly
// as any other caller's would.
type Scheduler struct {
	Applier  *Applier
	Executor *Executor
	Catalog  AgentCatalog
	Limits   SupervisorLimits

	// Advisor/AdvisorMode/AdvisorPolicyVersion are Phase 7B's explicit,
	// Core-owned configuration - never Agent-/LLM-controllable. Advisor
	// may be nil; AdvisorMode defaults to AgentDecisionDisabled (the
	// zero value) when unset, meaning NewScheduler's existing callers
	// are unaffected unless they explicitly opt in.
	Advisor              AgentDecisionAdvisor
	AdvisorMode          AgentDecisionMode
	AdvisorPolicyVersion string
	// AdvisorTelemetry records advisor_calls/advisor_failures/
	// advisor_shadow_recommendations/advisor_advisory_selections/
	// advisor_fallbacks - defaults to a no-op if unset (NewScheduler's
	// existing callers are unaffected).
	AdvisorTelemetry AdvisorTelemetry

	// EvaluationObserver/EvaluationMode/EvaluationFailureSink are ADR
	// 0009's explicit, Core-owned production-evaluation-capture
	// configuration - never Agent-/LLM-controllable. EvaluationMode
	// defaults to EvaluationDisabled (the zero value), so NewScheduler's
	// existing callers are unaffected unless they explicitly opt in -
	// the conservative default the mandate requires.
	EvaluationObserver    RoutingDecisionObserver
	EvaluationMode        EvaluationMode
	EvaluationFailureSink RoutingDecisionObserverFailureSink
	// ExperimentAdmission is optional and Core-owned. It is consulted only
	// after capability/tenant eligibility and before Applier/Executor.
	// Leaving it nil preserves the champion-only behavior.
	ExperimentAdmission ExperimentAdmission

	mu              sync.Mutex
	reportedBlocked map[TaskID]bool // dedupes TaskBlocked event reporting across ticks
	reportedReady   map[TaskID]bool // dedupes TaskReady event reporting across ticks
}

// NewScheduler constructs a Scheduler with explicit, required
// dependencies. Advisor/AdvisorMode are left at their zero values
// (disabled) - see WithAdvisor to opt in explicitly.
func NewScheduler(applier *Applier, executor *Executor, catalog AgentCatalog, limits SupervisorLimits) *Scheduler {
	return &Scheduler{
		Applier: applier, Executor: executor, Catalog: catalog, Limits: limits,
		reportedBlocked: map[TaskID]bool{}, reportedReady: map[TaskID]bool{},
	}
}

// WithAdvisor returns a copy of s configured to consult advisor in the
// given mode, tagging every resulting advice-related event with
// policyVersion for later auditability (see ADR 0006's policy-
// versioning requirement). mode must be AgentDecisionShadow or
// AgentDecisionAdvisory to have any effect; any other value (including
// the zero value) behaves exactly like AgentDecisionDisabled.
func (s *Scheduler) WithAdvisor(advisor AgentDecisionAdvisor, mode AgentDecisionMode, policyVersion string) *Scheduler {
	// Mutates and returns the SAME *Scheduler (not a value copy) because
	// Scheduler embeds a sync.Mutex by value - copying it would copy a
	// lock, which go vet's copylocks check correctly flags as unsafe.
	// Scheduler is always used by pointer, so this is safe and matches
	// how every other Scheduler field is already configured.
	s.Advisor = advisor
	s.AdvisorMode = mode
	s.AdvisorPolicyVersion = policyVersion
	return s
}

// WithAdvisorTelemetry sets the AdvisorTelemetry sink (optional - a nil
// sink behaves as a no-op). Mutates and returns the same *Scheduler
// pointer for the same reason WithAdvisor does.
func (s *Scheduler) WithAdvisorTelemetry(tel AdvisorTelemetry) *Scheduler {
	s.AdvisorTelemetry = tel
	return s
}

// WithEvaluation configures s to capture every production decision's
// authoritative context via observer, in the given mode. If observer
// also implements RoutingDecisionObserverFailureSink, it is
// automatically wired as the EvaluationFailureSink too (the same
// auto-wiring convention WithObserver already uses for
// ExecutionObserver/ExecutionObserverFailureSink). mode must be
// EvaluationShadow to have any effect; any other value (including the
// zero value) behaves exactly like EvaluationDisabled.
func (s *Scheduler) WithEvaluation(observer RoutingDecisionObserver, mode EvaluationMode) *Scheduler {
	s.EvaluationObserver = observer
	s.EvaluationMode = mode
	if sink, ok := observer.(RoutingDecisionObserverFailureSink); ok {
		s.EvaluationFailureSink = sink
	}
	return s
}

func (s *Scheduler) WithExperimentAdmission(admission ExperimentAdmission) *Scheduler {
	s.ExperimentAdmission = admission
	return s
}

// Tick computes readiness, dispatches as many Ready tasks as the
// concurrency limit allows (assigning and executing each concurrently,
// through the real Applier/Executor), waits for everything it dispatched
// this tick to finish, and returns a bounded, typed event list
// summarizing what happened. It performs no network I/O itself (that is
// ExecutionProvider's concern) and spawns no goroutine that outlives this
// call - Tick does not return until every goroutine it started has
// finished, so it leaks nothing and is safe to call repeatedly in a loop.
func (s *Scheduler) Tick(ctx context.Context) ([]ExecutionEvent, error) {
	snapshot := s.Applier.State.Snapshot()
	readiness := computeReadiness(snapshot.Tasks)

	var events []ExecutionEvent
	active := 0
	var readyIDs []TaskID
	for _, t := range snapshot.Tasks.Tasks {
		switch readiness[t.ID] {
		case ReadinessActive:
			active++
		case ReadinessBlocked:
			s.mu.Lock()
			already := s.reportedBlocked[t.ID]
			if !already {
				s.reportedBlocked[t.ID] = true
			}
			s.mu.Unlock()
			if !already {
				dep, _ := blockingDependency(snapshot.Tasks, t.ID)
				events = append(events, ExecutionEvent{Kind: EventTaskBlocked, TaskID: t.ID, Detail: fmt.Sprintf("blocked by failed dependency %q", dep)})
			}
		case ReadinessReady:
			readyIDs = append(readyIDs, t.ID)
			s.mu.Lock()
			alreadyReady := s.reportedReady[t.ID]
			if !alreadyReady {
				s.reportedReady[t.ID] = true
			}
			s.mu.Unlock()
			if !alreadyReady {
				events = append(events, ExecutionEvent{Kind: EventTaskReady, TaskID: t.ID})
			}
		}
	}
	sort.Slice(readyIDs, func(i, j int) bool { return readyIDs[i] < readyIDs[j] })

	limit := s.Limits.MaxConcurrency
	if limit <= 0 {
		limit = DefaultSupervisorLimits().MaxConcurrency
	}
	available := limit - active
	if available < 0 {
		available = 0
	}
	if available > len(readyIDs) {
		available = len(readyIDs)
	}
	dispatch := readyIDs[:available]

	var (
		wg       sync.WaitGroup
		eventsMu sync.Mutex
	)
	for _, id := range dispatch {
		task, _ := snapshot.Tasks.ByID(id)

		agentID, adviceEvents, err := s.selectAgent(ctx, task)
		eventsMu.Lock()
		events = append(events, adviceEvents...)
		eventsMu.Unlock()
		if err != nil {
			eventsMu.Lock()
			events = append(events, ExecutionEvent{Kind: EventAgentUnavailable, TaskID: id, Detail: err.Error()})
			eventsMu.Unlock()
			continue
		}

		_, err = s.Applier.Apply(ctx, []AgentProposal{
			AssignTaskProposal{
				TaskID:           id,
				CandidateAgentID: agentID,
				RequiredCaps:     task.RequiredCapabilities,
				RationaleSummary: "scheduler: capability-based deterministic routing",
			},
		})
		if err != nil {
			eventsMu.Lock()
			events = append(events, classifyApplyFailure(id, err))
			eventsMu.Unlock()
			continue
		}

		eventsMu.Lock()
		events = append(events, ExecutionEvent{Kind: EventTaskAssigned, TaskID: id, Detail: string(agentID)})
		eventsMu.Unlock()

		wg.Add(1)
		go func(taskID TaskID) {
			defer wg.Done()
			eventsMu.Lock()
			events = append(events, ExecutionEvent{Kind: EventTaskStarted, TaskID: taskID})
			eventsMu.Unlock()

			result, err := s.Executor.Execute(ctx, taskID)
			eventsMu.Lock()
			defer eventsMu.Unlock()
			switch {
			case err != nil:
				events = append(events, classifyExecutionFailure(taskID, err))
			case result.Status == TaskStatusFailed:
				events = append(events, ExecutionEvent{Kind: EventTaskFailed, TaskID: taskID, Detail: result.Summary})
			default:
				events = append(events, ExecutionEvent{Kind: EventTaskSucceeded, TaskID: taskID, Detail: result.Summary})
				if len(result.Findings) > 0 {
					events = append(events, ExecutionEvent{Kind: EventFindingProduced, TaskID: taskID, Detail: fmt.Sprintf("%v", result.Findings)})
				}
			}
		}(id)
	}
	wg.Wait()

	return events, nil
}

// classifyExecutionFailure maps Executor's typed errors onto the
// requested, distinct ExecutionEventKind taxonomy rather than collapsing
// them into one generic event.
func classifyExecutionFailure(taskID TaskID, err error) ExecutionEvent {
	switch err.(type) {
	case *AgentRoutingFailure:
		return ExecutionEvent{Kind: EventAgentUnavailable, TaskID: taskID, Detail: err.Error()}
	case *AuthorizationFailure, *PolicyRejection:
		return ExecutionEvent{Kind: EventAuthorizationRejected, TaskID: taskID, Detail: err.Error()}
	case *ExecutionTimeout:
		return ExecutionEvent{Kind: EventExecutionTimedOut, TaskID: taskID, Detail: err.Error()}
	default:
		return ExecutionEvent{Kind: EventTaskFailed, TaskID: taskID, Detail: err.Error()}
	}
}

// classifyApplyFailure maps an Applier.Apply error onto the same typed
// event taxonomy. Applier wraps Authorizer rejections with
// fmt.Errorf("proposal %d: %w", ...), so errors.As (not a bare type
// switch) is required to see through that wrapping.
func classifyApplyFailure(taskID TaskID, err error) ExecutionEvent {
	var authErr *AuthorizationFailure
	var policyErr *PolicyRejection
	var routingErr *AgentRoutingFailure
	switch {
	case errors.As(err, &authErr):
		return ExecutionEvent{Kind: EventAuthorizationRejected, TaskID: taskID, Detail: err.Error()}
	case errors.As(err, &policyErr):
		return ExecutionEvent{Kind: EventAuthorizationRejected, TaskID: taskID, Detail: err.Error()}
	case errors.As(err, &routingErr):
		return ExecutionEvent{Kind: EventAgentUnavailable, TaskID: taskID, Detail: err.Error()}
	default:
		return ExecutionEvent{Kind: EventTaskFailed, TaskID: taskID, Detail: err.Error()}
	}
}

// WorkflowStatus derives, purely from the current (authoritative)
// TaskGraph, whether the workflow is complete and, if complete, whether
// it succeeded. This is Core-derived completion: Synthesis is never
// consulted, and a Supervisor cannot cause WorkflowStatus to report
// success by merely asserting it in a SynthesizeProposal/Synthesis value
// (those types carry no status field at all - see wire.go / types.go).
func WorkflowStatus(graph TaskGraph) (complete bool, succeeded bool, blocked []TaskID) {
	readiness := computeReadiness(graph)
	complete = true
	succeeded = true
	for _, t := range graph.Tasks {
		switch readiness[t.ID] {
		case ReadinessDone:
			if t.Status == TaskStatusFailed {
				succeeded = false
			}
		case ReadinessBlocked:
			complete = false
			succeeded = false
			blocked = append(blocked, t.ID)
		default: // Waiting, Ready, Active
			complete = false
			succeeded = false
		}
	}
	return complete, succeeded, blocked
}

// Phase 3 execution-event kinds. These are new, distinct constants rather
// than repurposing Phase 1's EventAgentFailed/EventPolicyRejection/etc.
// (which existing Phase 1/2A/2B tests already construct directly with a
// specific established meaning - manually signaling an agent/model-level
// failure to a Supervisor's Replan). Phase 3's Scheduler emits this more
// granular, task-lifecycle-oriented set instead.
const (
	EventTaskReady             ExecutionEventKind = "task_ready"
	EventTaskAssigned          ExecutionEventKind = "task_assigned"
	EventTaskStarted           ExecutionEventKind = "task_started"
	EventTaskSucceeded         ExecutionEventKind = "task_succeeded"
	EventTaskFailed            ExecutionEventKind = "task_failed"
	EventFindingProduced       ExecutionEventKind = "finding_produced"
	EventExecutionTimedOut     ExecutionEventKind = "execution_timed_out"
	EventAgentUnavailable      ExecutionEventKind = "agent_unavailable"
	EventAuthorizationRejected ExecutionEventKind = "authorization_rejected"
	EventTaskBlocked           ExecutionEventKind = "task_blocked"

	// Phase 7B advice-related events - purely observational signals
	// about the advisor's involvement, never authority themselves.
	EventAdvisorShadowRecommendation ExecutionEventKind = "advisor_shadow_recommendation"
	EventAdvisorySelection           ExecutionEventKind = "advisor_advisory_selection"
	EventAdvisorFallback             ExecutionEventKind = "advisor_fallback"
	EventAdvisorFailure              ExecutionEventKind = "advisor_failure"

	// EventChallengerAdmitted (ADR 0011 Phase 10) records that a durably
	// admitted experiment Challenger - not the Champion/Advisor pick -
	// became this decision's actual production selection. This is the
	// ONLY event kind that can mean the dispatched agent differs from
	// every other event kind's own recorded agent; Detail always names
	// both the admitted Challenger and the Champion it replaced.
	EventChallengerAdmitted ExecutionEventKind = "challenger_admitted"
)
