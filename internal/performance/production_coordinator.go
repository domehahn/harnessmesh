package performance

import (
	"context"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
	"github.com/domehahn/harnessmesh/internal/telemetry"
)

// ProductionEvaluationCoordinator is the ONE authoritative production
// shadow-evaluation path (ADR 0009 Part 5's explicit "consolidate
// existing shadow paths" instruction - Phase 7B's advisor-shadow
// mechanism and Phase 9's ShadowEvaluator are not duplicated here; this
// type is what WIRES Phase 9's evaluation plane into Phase 7's real
// production selection/execution/finalization hooks, automatically).
//
// It implements THREE existing hook interfaces - supervisor.
// RoutingDecisionObserver, supervisor.ExecutionObserver, and review.
// ReviewFinalizationObserver - by DELEGATING each hook's pre-existing
// job to an embedded implementation, then additionally performing the
// NEW automatic evaluation-capture/outcome-attachment work. No parallel
// orchestration framework was introduced: Executor/ReviewApplier still
// only ever call the SAME single-observer hook points they already
// had; this type is simply what is plugged into them now.
type ProductionEvaluationCoordinator struct {
	// Execution/Review are the pre-existing Phase 7A/7C implementations
	// this coordinator delegates to FIRST, unchanged, before doing any
	// new evaluation work - their own behavior/guarantees are entirely
	// untouched.
	Execution *SupervisorExecutionObserver
	Review    *ReviewFinalizationHook

	SnapshotStore CandidateSnapshotStore
	Evaluator     *ShadowEvaluator
	// Challengers are evaluated automatically for every captured
	// decision - bounded by Evaluator.Limits.MaxChallengersPerDecision.
	Challengers []RoutingPolicy
	Limits      EvaluationLimits
	Telemetry   ProductionWiringTelemetry

	// Experiments is ADR 0011 Phase 10's durable experiment control
	// plane - optional (nil disables Challenger admission entirely,
	// the conservative default). When set, this coordinator ALSO
	// implements supervisor.ExperimentAdmission (see Admit below),
	// consolidating "one authoritative path" exactly like the rest of
	// this type: the SAME object that captures decisions/attaches
	// outcomes is also what Scheduler.WithExperimentAdmission points
	// at, so admission and outcome/budget-release correlation share one
	// internal bookkeeping structure rather than two coordinators
	// drifting out of sync.
	Experiments *ExperimentController

	mu sync.Mutex
	// pendingDecisions hands off a just-captured decision's EvaluationIDs
	// from ObserveRoutingDecision to the SUBSEQUENT ObserveExecution call
	// for the SAME task - transient, overwritten per decision, never
	// read after the matching execution consumes it. Correctness relies
	// on Scheduler's existing, unchanged dispatch discipline: a task is
	// selected (decision) and then immediately dispatched (execution) in
	// the SAME control-flow sequence, with no other decision for the
	// SAME task interleaved in between - exactly as ADR 0006/0009
	// require and ADR 0009's own Tick-ordering analysis confirms.
	pendingDecisions map[string]pendingDecision
	// evaluationsByExecutionID is the STABLE correlation key (Core-owned
	// ExecutionID, ADR 0006 Phase 7C) used by BOTH the execution-time
	// outcome attach and the later finalization-time quality attach -
	// this is what makes retry attribution correct: E1 and E2 are
	// different real ExecutionIDs, so a review resolving to E1 can never
	// find/alter E2's entry.
	evaluationsByExecutionID map[string][]string
	// pendingAdmissions mirrors pendingDecisions' transient handoff
	// pattern: an admission recorded at decision time (Admit) is handed
	// off to the SAME task's next ObserveExecution call, which is the
	// signal that a real execution attempt genuinely happened - at that
	// point the admission's budget reservation is converted to a
	// consumption (ExperimentController.Complete), releasing the
	// concurrency slot while preserving the lifetime total. See
	// ObserveExecution's doc comment for the one known, bounded
	// residual gap this does not close.
	pendingAdmissions map[string]Admission
	// admissionsByExecutionID is the durable (not transient, mirrors
	// evaluationsByExecutionID) ExecutionID->Admission record, populated
	// by ObserveExecution whenever the completing decision had an
	// admitted Challenger. ObserveReviewTransition reads it later, by
	// the SAME stable ExecutionID, to attribute the eventual QUALITY
	// verdict (Verified/Rejected/Contested) to the right experiment -
	// never guessed, never cross-bound to a different decision.
	admissionsByExecutionID map[string]Admission
}

// ProductionWiringTelemetry records ADR 0009's failure-visibility
// counters - a failure in ANY of these must never disappear silently
// (Part 12's explicit requirement), but must also never roll back
// production truth. Never receives prompt text, source code, or
// secrets - only counts.
type ProductionWiringTelemetry interface {
	RecordCandidateSnapshotCaptureFailure()
	RecordShadowEvaluationFailure()
	RecordEvaluationOutcomeAttachmentFailure()
}

type noopProductionWiringTelemetry struct{}

func (noopProductionWiringTelemetry) RecordCandidateSnapshotCaptureFailure()    {}
func (noopProductionWiringTelemetry) RecordShadowEvaluationFailure()            {}
func (noopProductionWiringTelemetry) RecordEvaluationOutcomeAttachmentFailure() {}

var _ ProductionWiringTelemetry = noopProductionWiringTelemetry{}

// RegistryProductionWiringTelemetry adapts telemetry.Registry - reused
// directly, no second telemetry system.
type RegistryProductionWiringTelemetry struct{ Registry *telemetry.Registry }

func NewRegistryProductionWiringTelemetry(r *telemetry.Registry) *RegistryProductionWiringTelemetry {
	return &RegistryProductionWiringTelemetry{Registry: r}
}
func (t *RegistryProductionWiringTelemetry) RecordCandidateSnapshotCaptureFailure() {
	t.Registry.Counter("candidate_snapshot_capture_failure").Add(1)
}
func (t *RegistryProductionWiringTelemetry) RecordShadowEvaluationFailure() {
	t.Registry.Counter("shadow_evaluation_failure").Add(1)
}
func (t *RegistryProductionWiringTelemetry) RecordEvaluationOutcomeAttachmentFailure() {
	t.Registry.Counter("evaluation_outcome_attachment_failure").Add(1)
}

var _ ProductionWiringTelemetry = (*RegistryProductionWiringTelemetry)(nil)

type pendingDecision struct {
	decisionID    supervisor.DecisionID
	evaluationIDs []string
}

func pendingKey(tenant string, taskID supervisor.TaskID) string {
	return tenant + "\x00" + string(taskID)
}

// NewProductionEvaluationCoordinator constructs the coordinator. tel
// may be nil (defaults to a no-op).
func NewProductionEvaluationCoordinator(execution *SupervisorExecutionObserver, reviewHook *ReviewFinalizationHook, snapshotStore CandidateSnapshotStore, evaluator *ShadowEvaluator, challengers []RoutingPolicy, limits EvaluationLimits, tel ProductionWiringTelemetry) *ProductionEvaluationCoordinator {
	if tel == nil {
		tel = noopProductionWiringTelemetry{}
	}
	return &ProductionEvaluationCoordinator{
		Execution: execution, Review: reviewHook, SnapshotStore: snapshotStore, Evaluator: evaluator,
		Challengers: challengers, Limits: limits, Telemetry: tel,
		pendingDecisions:         map[string]pendingDecision{},
		evaluationsByExecutionID: map[string][]string{},
		pendingAdmissions:        map[string]Admission{},
		admissionsByExecutionID:  map[string]Admission{},
	}
}

// WithExperiments attaches the durable experiment control plane -
// optional; a nil/unset Experiments field means Admit always returns
// not-admitted (Champion-only), the conservative default.
func (c *ProductionEvaluationCoordinator) WithExperiments(experiments *ExperimentController) *ProductionEvaluationCoordinator {
	c.Experiments = experiments
	return c
}

// Admit implements supervisor.ExperimentAdmission - the ONLY path by
// which a durably-admitted Challenger can become a production
// selection (ADR 0011). It is consulted by Scheduler.selectAgent AFTER
// the decision context (and its CandidateSnapshot) has already been
// captured via ObserveRoutingDecision in the SAME call chain, so the
// snapshot this method needs already exists - it is fetched, never
// reconstructed, closing any window for the admission check to see a
// DIFFERENT candidate universe than Champion/Evaluation already saw.
//
// If admitted, the resulting budget reservation is tracked under this
// task's pendingAdmissions entry, to be converted to a consumption (or
// released) by ObserveExecution/ObserveReviewTransition - never left
// to leak.
func (c *ProductionEvaluationCoordinator) Admit(ctx context.Context, task supervisor.Task, decisionID supervisor.DecisionID, candidates []supervisor.AgentCandidate, champion supervisor.AgentID, policy supervisor.PolicyProvenance) (supervisor.AgentID, bool, error) {
	if c.Experiments == nil {
		return champion, false, nil
	}
	audit := func(code ExperimentEligibilityReason) {
		_ = c.Experiments.Store.Audit(ctx, ExperimentAudit{ExperimentID: c.Experiments.ExperimentID, DecisionID: decisionID, Event: "admission_denied", Reason: string(code), At: time.Now().UTC()})
	}
	snapshot, found, err := c.SnapshotStore.Get(ctx, task.Tenant, decisionID)
	if err != nil {
		c.Telemetry.RecordCandidateSnapshotCaptureFailure()
		audit(EligibilityControlPlaneUnavailable)
		return champion, false, nil
	}
	if !found {
		// The decision context must already exist (captured moments
		// earlier in the SAME selectAgent call) - its absence means
		// something upstream already failed closed; admission must fail
		// closed too, never fabricate a snapshot here.
		audit(EligibilityMissingDecisionContext)
		return champion, false, nil
	}

	exp, err := c.Experiments.Store.Get(ctx, c.Experiments.ExperimentID)
	if err != nil {
		audit(EligibilityControlPlaneUnavailable)
		return champion, false, nil
	}

	// Hard admission precondition (ADR 0011 Part 4): evaluation context
	// must have been captured successfully for this exact decision -
	// reuses ValidateExperimentContext (ADR 0009D) directly against this
	// coordinator's OWN stores (c.SnapshotStore / c.Evaluator.Store),
	// never a separately-wired pair that could drift out of sync.
	if err := ValidateExperimentContext(ctx, c.SnapshotStore, c.Evaluator.Store, task.Tenant, decisionID, exp.Definition.ChallengerPolicy, exp.Definition.ChallengerAgentID); err != nil {
		audit(ClassifyReadinessError(err))
		return champion, false, nil
	}

	admission, err := c.Experiments.Admit(ctx, AdmissionRequest{
		ExperimentID: c.Experiments.ExperimentID, DecisionID: decisionID, Tenant: task.Tenant, Task: task,
		Snapshot: snapshot, ChampionAgentID: champion, ChallengerAgentID: exp.Definition.ChallengerAgentID,
		ChallengerPolicy: exp.Definition.ChallengerPolicy, Risk: task.Risk, Now: time.Now().UTC(),
	})
	if err != nil || !admission.IsChallenger {
		return champion, false, nil
	}

	c.mu.Lock()
	c.pendingAdmissions[pendingKey(task.Tenant, task.ID)] = admission
	c.mu.Unlock()
	return admission.Selected, true, nil
}

var _ supervisor.ExperimentAdmission = (*ProductionEvaluationCoordinator)(nil)

// ObserveRoutingDecision implements supervisor.RoutingDecisionObserver -
// Core-owned CandidateSnapshot creation + automatic Challenger
// evaluation, from the EXACT final eligible candidate set Scheduler
// just computed. Never widens or substitutes that set.
func (c *ProductionEvaluationCoordinator) ObserveRoutingDecision(ctx context.Context, decision supervisor.RoutingDecisionContext) {
	candidates := make([]supervisor.AgentCandidate, len(decision.CandidateAgentIDs))
	for i, id := range decision.CandidateAgentIDs {
		candidates[i] = supervisor.AgentCandidate{AgentID: id}
	}
	snapshot, err := NewCandidateSnapshot(decision.DecisionID, decision.Tenant, decision.TaskID, TaskClass(decision.TaskClass), candidates, decision.CapturedAt, c.Limits)
	if err != nil {
		c.Telemetry.RecordCandidateSnapshotCaptureFailure()
		return
	}
	if err := c.SnapshotStore.Save(ctx, snapshot); err != nil {
		// A genuine identity conflict (a retried decision's candidate
		// set differs from the one already stored for this TaskID) is
		// observable here but never fatal to the decision that already
		// happened - fail THIS evaluation capture closed; Champion
		// selection/execution is entirely unaffected (it already
		// happened, upstream of this call).
		c.Telemetry.RecordCandidateSnapshotCaptureFailure()
		return
	}

	championPolicy := RoutingPolicyDescriptor{ID: RoutingPolicyID(decision.ChampionPolicy.PolicyID), Version: decision.ChampionPolicy.PolicyVersion}
	records, evalErr := c.Evaluator.Evaluate(ctx, snapshot, championPolicy, decision.ChampionAgentID, decision.CapturedAt, c.Challengers)
	if evalErr != nil {
		c.Telemetry.RecordShadowEvaluationFailure()
	}
	evalIDs := make([]string, 0, len(records))
	for _, r := range records {
		evalIDs = append(evalIDs, r.EvaluationID)
	}

	c.mu.Lock()
	c.pendingDecisions[pendingKey(decision.Tenant, decision.TaskID)] = pendingDecision{decisionID: decision.DecisionID, evaluationIDs: evalIDs}
	c.mu.Unlock()
}

// RecordRoutingDecisionObserverPanic implements
// supervisor.RoutingDecisionObserverFailureSink.
func (c *ProductionEvaluationCoordinator) RecordRoutingDecisionObserverPanic() {
	c.Telemetry.RecordShadowEvaluationFailure()
}

// ObserveExecution implements supervisor.ExecutionObserver - delegates
// PerformanceObservation creation to the embedded SupervisorExecutionObserver
// UNCHANGED, then automatically attaches the EXECUTION-time (still
// unreviewed) outcome to every EvaluationRecord the matching decision
// produced, correlated via the Core-owned ExecutionID.
// ObserveExecution implements supervisor.ExecutionObserver. In addition
// to its pre-existing (ADR 0009) job, it also closes the experiment
// budget lifecycle (ADR 0011): if this task's decision had an admitted
// Challenger, the execution genuinely happening (success, failure,
// timeout, or cancel - ANY outcome Executor's provider call produces)
// is exactly the "real dispatch occurred" signal that converts the
// reservation into a permanent consumption
// (ExperimentController.Complete), freeing the concurrency slot while
// keeping the lifetime total intact.
//
// Known, bounded residual gap (documented in ADR 0011, not solved
// here): if Executor's OWN pre-observer TOCTOU re-check rejects the
// admitted Challenger BEFORE this observer is ever invoked (e.g. the
// agent lost a capability in the microsecond window between admission
// and dispatch), this method never runs for that attempt, and the
// reservation is never released - the SAME class of narrow, rare TOCTOU
// residual risk ADR 0009C already documented for its own concurrency-slot
// bookkeeping, not newly introduced here.
func (c *ProductionEvaluationCoordinator) ObserveExecution(ctx context.Context, task supervisor.Task, agentID supervisor.AgentID, executionID supervisor.ExecutionID, result supervisor.ExecutionResult, execErr error, startedAt, finishedAt time.Time) {
	c.Execution.ObserveExecution(ctx, task, agentID, executionID, result, execErr, startedAt, finishedAt)

	key := pendingKey(task.Tenant, task.ID)

	c.mu.Lock()
	pending, ok := c.pendingDecisions[key]
	if ok {
		delete(c.pendingDecisions, key)
	}
	admission, hasAdmission := c.pendingAdmissions[key]
	if hasAdmission {
		delete(c.pendingAdmissions, key)
	}
	c.mu.Unlock()

	outcome := ClassifyExecutionOutcome(result, execErr)

	if hasAdmission && c.Experiments != nil {
		if err := c.Experiments.Complete(ctx, admission, time.Now().UTC()); err != nil {
			c.Telemetry.RecordEvaluationOutcomeAttachmentFailure()
		}
		if admission.IsChallenger {
			c.Experiments.RecordChallengerExecutionOutcome(ctx, admission.ExperimentID, outcome, time.Now().UTC())
			c.mu.Lock()
			c.admissionsByExecutionID[string(executionID)] = admission
			c.mu.Unlock()
		}
	}

	if !ok || len(pending.evaluationIDs) == 0 {
		return
	}

	c.mu.Lock()
	c.evaluationsByExecutionID[string(executionID)] = pending.evaluationIDs
	c.mu.Unlock()

	var admittedChallenger supervisor.AgentID
	if hasAdmission && admission.IsChallenger {
		admittedChallenger = admission.Selected
	}
	for _, evalID := range pending.evaluationIDs {
		if err := c.Evaluator.AttachOutcome(ctx, evalID, string(executionID), outcome, QualityUnreviewed, admittedChallenger); err != nil {
			c.Telemetry.RecordEvaluationOutcomeAttachmentFailure()
		}
	}
}

// RecordExecutionObserverPanic implements
// supervisor.ExecutionObserverFailureSink, delegating to the embedded
// observer (which already implements it).
func (c *ProductionEvaluationCoordinator) RecordExecutionObserverPanic() {
	c.Execution.RecordExecutionObserverPanic()
}

// ObserveReviewTransition implements review.ReviewFinalizationObserver -
// delegates PerformanceObservation finalization to the embedded
// ReviewFinalizationHook UNCHANGED, then automatically re-attaches the
// now-authoritative QUALITY outcome to every EvaluationRecord whose
// Champion execution this transition resolved to (via the SAME exact
// ExecutionID resolution the embedded hook itself uses - never a
// separate, potentially-divergent resolution).
func (c *ProductionEvaluationCoordinator) ObserveReviewTransition(ctx context.Context, group review.ReviewGroup, previousStatus protocol.FindingStatus) {
	c.Review.ObserveReviewTransition(ctx, group, previousStatus)

	targets := c.Review.ResolvedExecutionTargets(ctx, group)
	quality := QualityFromFindingStatus(group.Status)
	for _, target := range targets {
		c.mu.Lock()
		evalIDs := append([]string(nil), c.evaluationsByExecutionID[target.ExecutionID]...)
		admission, hasAdmission := c.admissionsByExecutionID[target.ExecutionID]
		c.mu.Unlock()
		for _, evalID := range evalIDs {
			if err := c.Evaluator.AttachChampionQuality(ctx, evalID, quality); err != nil {
				c.Telemetry.RecordEvaluationOutcomeAttachmentFailure()
			}
		}
		if hasAdmission && admission.IsChallenger && c.Experiments != nil {
			c.Experiments.RecordChallengerReviewOutcome(ctx, admission.ExperimentID, quality, time.Now().UTC())
		}
	}
}

// RecordReviewObserverPanic implements review.ReviewObserverFailureSink,
// delegating to the embedded hook (which already implements it).
func (c *ProductionEvaluationCoordinator) RecordReviewObserverPanic() {
	c.Review.RecordReviewObserverPanic()
}

var (
	_ supervisor.RoutingDecisionObserver            = (*ProductionEvaluationCoordinator)(nil)
	_ supervisor.RoutingDecisionObserverFailureSink = (*ProductionEvaluationCoordinator)(nil)
	_ supervisor.ExecutionObserver                  = (*ProductionEvaluationCoordinator)(nil)
	_ supervisor.ExecutionObserverFailureSink       = (*ProductionEvaluationCoordinator)(nil)
	_ review.ReviewFinalizationObserver             = (*ProductionEvaluationCoordinator)(nil)
	_ review.ReviewObserverFailureSink              = (*ProductionEvaluationCoordinator)(nil)
)
