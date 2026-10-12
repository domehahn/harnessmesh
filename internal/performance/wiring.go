package performance

import (
	"context"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
	"github.com/domehahn/harnessmesh/internal/telemetry"
)

// WiringTelemetry records the Phase 7A/7C production-wiring metrics ADR
// 0006 requires. Never receives prompt text, source code, secrets, or a
// recovered panic's raw payload - only counts.
type WiringTelemetry interface {
	RecordObservationCreated()
	RecordObservationWriteFailure()
	RecordFinalizationFailure()
	RecordAttributionFailure()
	// RecordExecutionObserverPanic/RecordReviewObserverPanic record that
	// an observer itself panicked (Phase 7C's observer-failure-
	// visibility closure) - distinct from a store/attribution failure,
	// which already has its own counter above.
	RecordExecutionObserverPanic()
	RecordReviewObserverPanic()
	// RecordModelOutcomeFeedback/RecordModelOutcomeFeedbackFailure
	// record ADR 0007's joint-intelligence model-outcome feedback path -
	// evidence/telemetry only, never a routing command (see
	// ModelOutcomeObserver's doc comment).
	RecordModelOutcomeFeedback()
	RecordModelOutcomeFeedbackFailure()
}

type noopWiringTelemetry struct{}

func (noopWiringTelemetry) RecordObservationCreated()          {}
func (noopWiringTelemetry) RecordObservationWriteFailure()     {}
func (noopWiringTelemetry) RecordFinalizationFailure()         {}
func (noopWiringTelemetry) RecordAttributionFailure()          {}
func (noopWiringTelemetry) RecordExecutionObserverPanic()      {}
func (noopWiringTelemetry) RecordReviewObserverPanic()         {}
func (noopWiringTelemetry) RecordModelOutcomeFeedback()        {}
func (noopWiringTelemetry) RecordModelOutcomeFeedbackFailure() {}

var _ WiringTelemetry = noopWiringTelemetry{}

// RegistryWiringTelemetry adapts the repository's existing dependency-
// free telemetry.Registry to WiringTelemetry - reused directly, no
// second telemetry system, exactly mirroring supervisor.RegistryTelemetry's
// pattern.
type RegistryWiringTelemetry struct{ Registry *telemetry.Registry }

func NewRegistryWiringTelemetry(r *telemetry.Registry) *RegistryWiringTelemetry {
	return &RegistryWiringTelemetry{Registry: r}
}
func (t *RegistryWiringTelemetry) RecordObservationCreated() {
	t.Registry.Counter("performance.observations_created").Add(1)
}
func (t *RegistryWiringTelemetry) RecordObservationWriteFailure() {
	t.Registry.Counter("performance.observation_write_failures").Add(1)
}
func (t *RegistryWiringTelemetry) RecordFinalizationFailure() {
	t.Registry.Counter("performance.finalization_failures").Add(1)
}
func (t *RegistryWiringTelemetry) RecordAttributionFailure() {
	t.Registry.Counter("performance.attribution_failures").Add(1)
}
func (t *RegistryWiringTelemetry) RecordExecutionObserverPanic() {
	t.Registry.Counter("performance.execution_observer_panics").Add(1)
}
func (t *RegistryWiringTelemetry) RecordReviewObserverPanic() {
	t.Registry.Counter("performance.review_observer_panics").Add(1)
}
func (t *RegistryWiringTelemetry) RecordModelOutcomeFeedback() {
	t.Registry.Counter("model_outcome_feedback").Add(1)
}
func (t *RegistryWiringTelemetry) RecordModelOutcomeFeedbackFailure() {
	t.Registry.Counter("model_outcome_feedback_failure").Add(1)
}

var _ WiringTelemetry = (*RegistryWiringTelemetry)(nil)

// SupervisorExecutionObserver implements supervisor.ExecutionObserver -
// the ONLY place in this package a PerformanceObservation is created
// from a real production execution. It never propagates an error back
// to Executor (supervisor.ExecutionObserver's method returns nothing),
// so a failure here can never affect execution truth - failures are
// recorded via Telemetry and otherwise dropped, per ADR 0006's explicit
// failure-isolation requirement. It also implements
// supervisor.ExecutionObserverFailureSink, so a single WithObserver call
// on Executor wires both the observation hook and its own panic-
// visibility sink.
type SupervisorExecutionObserver struct {
	Store     PerformanceStore
	Telemetry WiringTelemetry
	// ModelOutcome is optional (nil-safe) - when set, every ModelUsages
	// entry on a newly created observation is surfaced to it as
	// execution-time feedback (QualityOutcome is still QualityUnreviewed
	// at this point; ReviewFinalizationHook emits a second, updated
	// record once quality is authoritatively known).
	ModelOutcome ModelOutcomeObserver
}

// NewSupervisorExecutionObserver constructs the observer. Telemetry may
// be nil (defaults to a no-op).
func NewSupervisorExecutionObserver(store PerformanceStore, tel WiringTelemetry) *SupervisorExecutionObserver {
	if tel == nil {
		tel = noopWiringTelemetry{}
	}
	return &SupervisorExecutionObserver{Store: store, Telemetry: tel}
}

func (o *SupervisorExecutionObserver) ObserveExecution(ctx context.Context, task supervisor.Task, agentID supervisor.AgentID, executionID supervisor.ExecutionID, result supervisor.ExecutionResult, execErr error, startedAt, finishedAt time.Time) {
	// task.Type is the EXISTING field this package reuses as TaskClass's
	// source - no new parallel task-type field was introduced (ADR
	// 0006's explicit "no parallel IDs/fields" instruction).
	taskClass := TaskClass(task.Type)
	if !KnownTaskClass(taskClass) {
		// Fail closed: an unrecognized/missing task class means this
		// execution cannot be attributed to a meaningful performance
		// bucket. Recorded, never fabricated.
		o.Telemetry.RecordAttributionFailure()
		return
	}
	if agentID == "" {
		o.Telemetry.RecordAttributionFailure()
		return
	}
	if executionID == "" {
		// Should be structurally impossible (Executor always generates
		// one before calling the observer - ADR 0006 Phase 7C), but
		// fail closed rather than fabricate an ID here if it ever
		// happens (e.g. a future caller invoking this observer directly
		// without going through Executor).
		o.Telemetry.RecordAttributionFailure()
		return
	}

	in := NewObservationInput{
		Tenant: task.Tenant, TaskID: task.ID, ExecutionID: string(executionID),
		TaskClass: taskClass, Risk: task.Risk,
		AgentID:          agentID,
		ExecutionOutcome: ClassifyExecutionOutcome(result, execErr),
		ModelUsages:      convertModelUsages(result.ModelUsages),
		Metrics:          PerformanceMetrics{Duration: finishedAt.Sub(startedAt), ExecutionAttempts: 1},
		StartedAt:        startedAt, FinishedAt: finishedAt,
	}
	obs, err := NewObservation(in)
	if err != nil {
		o.Telemetry.RecordAttributionFailure()
		return
	}
	if err := o.Store.Save(ctx, obs); err != nil {
		o.Telemetry.RecordObservationWriteFailure()
		return
	}
	o.Telemetry.RecordObservationCreated()
	emitModelOutcomeFeedback(ctx, o.ModelOutcome, o.Telemetry, obs)
}

// RecordExecutionObserverPanic implements
// supervisor.ExecutionObserverFailureSink.
func (o *SupervisorExecutionObserver) RecordExecutionObserverPanic() {
	o.Telemetry.RecordExecutionObserverPanic()
}

func convertModelUsages(in []supervisor.ModelUsageRef) []ModelUsageRef {
	if len(in) == 0 {
		return nil
	}
	// Merge entries sharing the same (RouteID, Provider, Model) identity
	// by summing Count - ADR 0007's dedup requirement: a provider that
	// (buggily or redundantly) reports the same route/model combination
	// as two separate entries must not inflate inference-usage counts.
	// Identity is derived only from these existing fields - no new ID.
	type key struct{ routeID, provider, model string }
	merged := map[key]*ModelUsageRef{}
	order := make([]key, 0, len(in))
	for _, u := range in {
		k := key{u.RouteID, u.Provider, u.Model}
		if existing, ok := merged[k]; ok {
			existing.Count += u.Count
			continue
		}
		ref := ModelUsageRef{RouteID: u.RouteID, Provider: u.Provider, Model: u.Model, Count: u.Count}
		merged[k] = &ref
		order = append(order, k)
	}
	out := make([]ModelUsageRef, 0, len(order))
	for _, k := range order {
		out = append(out, *merged[k])
	}
	return out
}

var _ supervisor.ExecutionObserver = (*SupervisorExecutionObserver)(nil)
var _ supervisor.ExecutionObserverFailureSink = (*SupervisorExecutionObserver)(nil)

// ReviewFinalizationHook implements review.ReviewFinalizationObserver -
// the ONLY place in this package a PerformanceObservation's quality
// dimension is finalized from a real production review transition. It
// reads ONLY the authoritative review.ReviewGroup passed in (never raw
// Arbiter/agent output).
//
// Correlation (ADR 0006 Phase 7C): for each FindingObservation, the
// EXACT execution it originated from is resolved - never a "most
// recently started pending" heuristic - via, in order:
//  1. FindingObservation.SourceExecutionRef, if set (the Core-owned,
//     per-attempt execution identity, set only from real execution
//     context - see its doc comment).
//  2. Otherwise, FindingObservation.EvidenceRefs resolved against
//     Evidence (if configured): each referenced EvidenceRecord's own
//     Core-owned ExecutionID is read, scoped to the same tenant and task.
//     If more than one DISTINCT ExecutionID is found this way for the
//     SAME observation, that is ambiguous provenance - fails closed
//     (not finalized, recorded as an attribution failure), never
//     resolved by guessing.
//
// An observation whose execution cannot be resolved by either path is
// skipped (recorded as an attribution failure) - it is never finalized
// against some other, unrelated execution. The resolved (Tenant, TaskID,
// ExecutionID) triple is used to compute the EXACT PerformanceObservation
// ID (ComputeObservationID) and fetch it directly (Store.Get) - there is
// no query-and-sort-by-timestamp step anywhere in this path.
//
// Quality attribution semantics: a review claim can be supported by
// MULTIPLE agents' executions (the claimant's own, plus every
// independent reviewer's). Each contributing execution's OWN
// PerformanceObservation is finalized independently with this SAME
// review outcome - this is the current performance model's attribution
// rule (ADR 0005/0006): the claimant's implementation is credited/
// debited by the verdict on its claim, and each reviewer's own
// review-task execution is likewise finalized (its own ExecutionOutcome,
// paired with the SAME QualityOutcome the claim reached). There is no
// "pick one execution" step - every resolvable, distinct
// (TaskID, ExecutionID) pair found across the group's observations is
// finalized.
type ReviewFinalizationHook struct {
	Store     PerformanceStore
	Evidence  review.EvidenceStore // optional; nil skips EvidenceRefs resolution
	Telemetry WiringTelemetry
	// ModelOutcome is optional (nil-safe) - when set, a second,
	// quality-updated ModelOutcomeRecord is emitted for every
	// ModelUsages entry once an observation is finalized (see
	// SupervisorExecutionObserver.ModelOutcome's doc comment for the
	// execution-time counterpart).
	ModelOutcome ModelOutcomeObserver
}

// NewReviewFinalizationHook constructs the hook. evidence may be nil if
// no EvidenceStore is available - resolution then relies solely on
// FindingObservation.SourceExecutionRef. tel may be nil (defaults to a
// no-op).
func NewReviewFinalizationHook(store PerformanceStore, evidence review.EvidenceStore, tel WiringTelemetry) *ReviewFinalizationHook {
	if tel == nil {
		tel = noopWiringTelemetry{}
	}
	return &ReviewFinalizationHook{Store: store, Evidence: evidence, Telemetry: tel}
}

// ResolvedExecutionTarget is one FindingObservation's resolved,
// authoritative execution reference - exported so a caller (e.g.
// ProductionEvaluationCoordinator, ADR 0009) can learn exactly which
// executions a review transition touched, using the SAME resolution
// logic ObserveReviewTransition itself uses, without duplicating it.
type ResolvedExecutionTarget struct {
	TaskID      supervisor.TaskID
	ExecutionID string
}

// ResolvedExecutionTargets resolves every DISTINCT, exact execution
// target group.Observations refers to - the same resolution
// ObserveReviewTransition performs internally, exposed for reuse. Never
// guesses: an observation with unresolvable provenance is simply
// absent from the result (recorded as an attribution failure via
// Telemetry), never approximated.
func (h *ReviewFinalizationHook) ResolvedExecutionTargets(ctx context.Context, group review.ReviewGroup) []ResolvedExecutionTarget {
	seen := map[ResolvedExecutionTarget]bool{}
	var targets []ResolvedExecutionTarget
	for _, obs := range group.Observations {
		taskID := supervisor.TaskID(obs.SourceExecutionID)
		if taskID == "" {
			h.Telemetry.RecordAttributionFailure()
			continue
		}
		executionID, ok := h.resolveExecutionID(ctx, group.Tenant, taskID, obs)
		if !ok {
			h.Telemetry.RecordAttributionFailure()
			continue
		}
		ref := ResolvedExecutionTarget{TaskID: taskID, ExecutionID: executionID}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		targets = append(targets, ref)
	}
	return targets
}

func (h *ReviewFinalizationHook) ObserveReviewTransition(ctx context.Context, group review.ReviewGroup, previousStatus protocol.FindingStatus) {
	targets := h.ResolvedExecutionTargets(ctx, group)

	for _, ref := range targets {
		id := ComputeObservationID(group.Tenant, ref.TaskID, ref.ExecutionID, ObservationKindExecution)
		existing, err := h.Store.Get(ctx, id)
		if err != nil {
			if _, notFound := err.(*ObservationNotFound); notFound {
				// No observation was ever created for this exact
				// execution (e.g. it predates production wiring, or the
				// execution bypassed Core entirely) - nothing to
				// finalize, not an error.
				continue
			}
			h.Telemetry.RecordFinalizationFailure()
			continue
		}
		finalized := FinalizeWithReview(existing, group)
		if err := h.Store.Save(ctx, finalized); err != nil {
			h.Telemetry.RecordFinalizationFailure()
			continue
		}
		emitModelOutcomeFeedback(ctx, h.ModelOutcome, h.Telemetry, finalized)
	}
}

// resolveExecutionID determines obs's EXACT, authoritative execution
// identity. Never guesses: returns ok=false (fail closed) rather than
// picking among ambiguous candidates.
func (h *ReviewFinalizationHook) resolveExecutionID(ctx context.Context, tenant string, taskID supervisor.TaskID, obs review.FindingObservation) (string, bool) {
	if obs.SourceExecutionRef != "" {
		return obs.SourceExecutionRef, true
	}
	if h.Evidence == nil {
		return "", false
	}

	resolved := map[string]bool{}
	for _, ref := range obs.EvidenceRefs {
		record, err := h.Evidence.Get(ctx, review.EvidenceID(ref))
		if err != nil {
			continue
		}
		if record.Tenant != tenant {
			continue // never resolve across a tenant boundary
		}
		if record.TaskID != taskID {
			continue // evidence must actually belong to this observation's task
		}
		if record.ExecutionID == "" {
			continue
		}
		resolved[record.ExecutionID] = true
	}

	if len(resolved) != 1 {
		// Zero matches: unresolvable, fail closed. More than one
		// DISTINCT candidate: ambiguous provenance, also fail closed -
		// never resolved by picking one arbitrarily.
		return "", false
	}
	for id := range resolved {
		return id, true
	}
	return "", false // unreachable
}

// RecordReviewObserverPanic implements review.ReviewObserverFailureSink.
func (h *ReviewFinalizationHook) RecordReviewObserverPanic() {
	h.Telemetry.RecordReviewObserverPanic()
}

var _ review.ReviewFinalizationObserver = (*ReviewFinalizationHook)(nil)
var _ review.ReviewObserverFailureSink = (*ReviewFinalizationHook)(nil)
