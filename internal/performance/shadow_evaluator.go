package performance

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
	"github.com/domehahn/harnessmesh/internal/telemetry"
)

// EvaluationTelemetry records ADR 0008's observability counters. Never
// receives prompt text, source code, or secrets - only counts.
type EvaluationTelemetry interface {
	RecordEvaluationCreated()
	RecordEvaluationCompleted()
	RecordEvaluationIncomplete()
	RecordSameRecommendation()
	RecordDifferentRecommendation()
	RecordInvalidRecommendation()
	RecordChallengerError()
	RecordReplayStarted()
	RecordReplayCompleted()
	RecordReplayIncomplete()
	RecordPromotionGateEligible()
	RecordPromotionGateBlocked()
	RecordTimeFaithfulReplay()
	RecordRetrospectiveReplay()
}

type noopEvaluationTelemetry struct{}

func (noopEvaluationTelemetry) RecordEvaluationCreated()       {}
func (noopEvaluationTelemetry) RecordEvaluationCompleted()     {}
func (noopEvaluationTelemetry) RecordEvaluationIncomplete()    {}
func (noopEvaluationTelemetry) RecordSameRecommendation()      {}
func (noopEvaluationTelemetry) RecordDifferentRecommendation() {}
func (noopEvaluationTelemetry) RecordInvalidRecommendation()   {}
func (noopEvaluationTelemetry) RecordChallengerError()         {}
func (noopEvaluationTelemetry) RecordReplayStarted()           {}
func (noopEvaluationTelemetry) RecordReplayCompleted()         {}
func (noopEvaluationTelemetry) RecordReplayIncomplete()        {}
func (noopEvaluationTelemetry) RecordPromotionGateEligible()   {}
func (noopEvaluationTelemetry) RecordPromotionGateBlocked()    {}
func (noopEvaluationTelemetry) RecordTimeFaithfulReplay()      {}
func (noopEvaluationTelemetry) RecordRetrospectiveReplay()     {}

var _ EvaluationTelemetry = noopEvaluationTelemetry{}

// RegistryEvaluationTelemetry adapts telemetry.Registry - reused
// directly, no second telemetry system.
type RegistryEvaluationTelemetry struct{ Registry *telemetry.Registry }

func NewRegistryEvaluationTelemetry(r *telemetry.Registry) *RegistryEvaluationTelemetry {
	return &RegistryEvaluationTelemetry{Registry: r}
}

func (t *RegistryEvaluationTelemetry) RecordEvaluationCreated() {
	t.Registry.Counter("routing_evaluation_created").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordEvaluationCompleted() {
	t.Registry.Counter("routing_evaluation_completed").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordEvaluationIncomplete() {
	t.Registry.Counter("routing_evaluation_incomplete").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordSameRecommendation() {
	t.Registry.Counter("challenger_same_recommendation").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordDifferentRecommendation() {
	t.Registry.Counter("challenger_different_recommendation").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordInvalidRecommendation() {
	t.Registry.Counter("challenger_invalid_recommendation").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordChallengerError() {
	t.Registry.Counter("challenger_error").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordReplayStarted() {
	t.Registry.Counter("replay_started").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordReplayCompleted() {
	t.Registry.Counter("replay_completed").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordReplayIncomplete() {
	t.Registry.Counter("replay_incomplete").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordPromotionGateEligible() {
	t.Registry.Counter("promotion_gate_eligible").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordPromotionGateBlocked() {
	t.Registry.Counter("promotion_gate_blocked").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordTimeFaithfulReplay() {
	t.Registry.Counter("time_faithful_replay_count").Add(1)
}
func (t *RegistryEvaluationTelemetry) RecordRetrospectiveReplay() {
	t.Registry.Counter("retrospective_replay_count").Add(1)
}

var _ EvaluationTelemetry = (*RegistryEvaluationTelemetry)(nil)

// ShadowEvaluator compares a Champion's ACTUAL, already-made decision
// against one or more Challengers' recommendation-only output, over the
// SAME CandidateSnapshot. It has NO reference anywhere - not in its own
// fields, not in any type it imports, not in any function signature it
// exposes - to supervisor.Executor or supervisor.ExecutionProvider.
// This is the structural proof that "Challenger output cannot reach
// Executor": there is no code path through which it could, even if a
// Challenger's RoutingPolicy implementation tried to smuggle a side
// effect, since Evaluate's only output is a value (RoutingEvaluationRecord)
// persisted to Store - it never calls anything resembling dispatch.
type ShadowEvaluator struct {
	Store     EvaluationStore
	Limits    EvaluationLimits
	Telemetry EvaluationTelemetry

	identityMu  sync.Mutex
	fingerprint map[string]string // "ID:Version" -> first-seen non-empty Fingerprint
}

func NewShadowEvaluator(store EvaluationStore, limits EvaluationLimits, tel EvaluationTelemetry) *ShadowEvaluator {
	if tel == nil {
		tel = noopEvaluationTelemetry{}
	}
	return &ShadowEvaluator{Store: store, Limits: limits, Telemetry: tel, fingerprint: map[string]string{}}
}

// checkPolicyIdentity detects "same Policy ID + same Version, different
// deterministic Fingerprint" (ADR 0009 Part 8) - the smallest-design
// enforcement point the mandate asks for (no separate policy registry;
// the evaluator's own bookkeeping is sufficient). Only enforced when
// BOTH the newly-seen and previously-seen fingerprints are non-empty -
// a descriptor with no fingerprint (e.g. a historical/manual Phase 9
// record, or a simple test descriptor) never trips this check, since
// there is nothing deterministic to compare.
func (e *ShadowEvaluator) checkPolicyIdentity(policy RoutingPolicyDescriptor) error {
	if policy.Fingerprint == "" {
		return nil
	}
	key := policy.String()
	e.identityMu.Lock()
	defer e.identityMu.Unlock()
	existing, seen := e.fingerprint[key]
	if !seen {
		e.fingerprint[key] = policy.Fingerprint
		return nil
	}
	if existing != policy.Fingerprint {
		return &PolicyIdentityConflict{PolicyID: string(policy.ID), PolicyVersion: policy.Version}
	}
	return nil
}

// PolicyIdentityConflict is returned when the SAME (ID, Version) public
// identity is seen with two DIFFERENT deterministic fingerprints - the
// same public name silently representing changed routing semantics.
// Evaluation fails closed for the affected policy; this NEVER affects
// Champion execution (which has already happened, or happens entirely
// outside ShadowEvaluator regardless).
type PolicyIdentityConflict struct {
	PolicyID      string
	PolicyVersion string
}

func (e *PolicyIdentityConflict) Error() string {
	return fmt.Sprintf("policy identity conflict: %s:%s has been seen with two different fingerprints", e.PolicyID, e.PolicyVersion)
}

// Evaluate records one RoutingEvaluationRecord per challenger (bounded
// by Limits.MaxChallengersPerDecision - exceeding it is a typed error,
// never a silent truncation). championAgentID is the REAL agent Core
// already selected via championPolicy for this decision - Evaluate
// never re-derives or second-guesses it; it is ground truth input, not
// something computed here.
func (e *ShadowEvaluator) Evaluate(ctx context.Context, snapshot CandidateSnapshot, championPolicy RoutingPolicyDescriptor, championAgentID supervisor.AgentID, decisionTime time.Time, challengers []RoutingPolicy) ([]RoutingEvaluationRecord, error) {
	if len(challengers) > e.Limits.maxChallengersPerDecision() {
		return nil, &TooManyChallengers{Count: len(challengers), Limit: e.Limits.maxChallengersPerDecision()}
	}

	// A champion-policy identity conflict makes the entire decision's
	// evaluation batch unreliable (every record below would carry a
	// Champion identity that may not mean what it used to) - fail the
	// whole Evaluate call closed. This NEVER touches Champion execution
	// itself, which has either already happened or happens entirely
	// outside this type regardless (see ShadowEvaluator's own doc
	// comment on structural separation from Executor).
	if err := e.checkPolicyIdentity(championPolicy); err != nil {
		e.Telemetry.RecordInvalidRecommendation()
		return nil, err
	}

	input := RoutingEvaluationInput{Tenant: snapshot.Tenant, TaskID: snapshot.TaskID, TaskClass: snapshot.TaskClass, Snapshot: snapshot}

	var records []RoutingEvaluationRecord
	var identityConflict error
	for _, challenger := range challengers {
		challengerPolicy := challenger.Descriptor()
		if err := e.checkPolicyIdentity(challengerPolicy); err != nil {
			// Fail closed for THIS challenger only - other challengers in
			// the same batch are unaffected, but the conflict is
			// surfaced (returned) rather than silently swallowed, per
			// ADR 0009's "failure must be observable" requirement. The
			// caller (e.g. ProductionEvaluationCoordinator) treats this
			// exactly like any other Evaluate error: recorded via
			// telemetry, never propagated to affect Champion execution.
			e.Telemetry.RecordInvalidRecommendation()
			identityConflict = err
			continue
		}
		rec := e.evaluateOne(ctx, snapshot, input, championPolicy, championAgentID, decisionTime, challenger)
		if err := e.Store.Save(ctx, rec); err != nil {
			return records, err
		}
		e.Telemetry.RecordEvaluationCreated()
		switch rec.Status {
		case StatusSameRecommendation:
			e.Telemetry.RecordSameRecommendation()
		case StatusDifferentRecommendation:
			e.Telemetry.RecordDifferentRecommendation()
		case StatusChallengerInvalidRecommendation:
			e.Telemetry.RecordInvalidRecommendation()
		case StatusChallengerError:
			e.Telemetry.RecordChallengerError()
		}
		records = append(records, rec)
	}
	return records, identityConflict
}

func (e *ShadowEvaluator) evaluateOne(ctx context.Context, snapshot CandidateSnapshot, input RoutingEvaluationInput, championPolicy RoutingPolicyDescriptor, championAgentID supervisor.AgentID, decisionTime time.Time, challenger RoutingPolicy) RoutingEvaluationRecord {
	challengerPolicy := challenger.Descriptor()
	base := RoutingEvaluationRecord{
		EvaluationID:      ComputeEvaluationID(snapshot, championPolicy, challengerPolicy),
		DecisionID:        snapshot.DecisionID,
		Tenant:            snapshot.Tenant,
		TaskID:            snapshot.TaskID,
		TaskClass:         snapshot.TaskClass,
		DecisionTime:      decisionTime,
		ChampionPolicy:    championPolicy,
		ChallengerPolicy:  challengerPolicy,
		CandidateAgentIDs: append([]string(nil), snapshot.AgentIDs...),
		ChampionAgentID:   championAgentID,
	}

	rec, err := e.recommendSafely(ctx, challenger, input)
	if err != nil {
		base.Status = StatusChallengerError
		base.Reasons = boundReasons([]string{err.Error()}, e.Limits)
		return base
	}
	if rec.Insufficient {
		base.Status = StatusChallengerInsufficientData
		base.Reasons = boundReasons(rec.Reasons, e.Limits)
		return base
	}
	if rec.AgentID == "" {
		base.Status = StatusChallengerInvalidRecommendation
		base.InvalidReason = InvalidReasonInvalidPolicyOutput
		base.Reasons = boundReasons(rec.Reasons, e.Limits)
		return base
	}
	if !snapshot.Contains(rec.AgentID) {
		// Includes the cross-tenant case structurally: a cross-tenant
		// agent could never have been IN the snapshot to begin with
		// (ADR 0007/Phase 7C's tenant-filtered snapshot source), so any
		// cross-tenant recommendation always lands here. Classified as
		// candidate_not_in_snapshot rather than the more specific
		// tenant_boundary/capability_mismatch, since the snapshot itself
		// retains no exclusion-reason provenance to PROVE which specific
		// filter excluded it - ADR 0009's explicit "only use a specific
		// reason if Core can prove it" / "do not manufacture exclusion
		// reason just for test coverage" instruction.
		base.Status = StatusChallengerInvalidRecommendation
		base.InvalidReason = InvalidReasonNotInSnapshot
		base.ChallengerAgentID = rec.AgentID
		base.Reasons = boundReasons(rec.Reasons, e.Limits)
		return base
	}

	base.ChallengerAgentID = rec.AgentID
	base.Reasons = boundReasons(rec.Reasons, e.Limits)
	if rec.AgentID == championAgentID {
		base.Status = StatusSameRecommendation
	} else {
		base.Status = StatusDifferentRecommendation
	}
	return base
}

// recommendSafely calls challenger.Recommend recover()-wrapped - a
// panicking Challenger must never crash the evaluator (same discipline
// as every other optional hook in this package).
func (e *ShadowEvaluator) recommendSafely(ctx context.Context, challenger RoutingPolicy, input RoutingEvaluationInput) (rec RoutingRecommendation, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errChallengerPanicked
		}
	}()
	return challenger.Recommend(ctx, input)
}

var errChallengerPanicked = &challengerPanicError{}

type challengerPanicError struct{}

func (e *challengerPanicError) Error() string { return "challenger panicked" }

// TooManyChallengers is returned when Evaluate is asked to run more
// challengers than Limits.MaxChallengersPerDecision allows.
type TooManyChallengers struct{ Count, Limit int }

func (e *TooManyChallengers) Error() string {
	return "too many challengers for one decision"
}

// AttachChampionOutcome correlates a REAL, Core-owned executionID to the
// evaluation record's Champion outcome fields ONLY - there is no
// parameter or code path here that could attach an outcome to
// ChallengerAgentID. Fails closed if the evaluation record does not
// exist (never fabricates one) and never guesses which evaluation an
// outcome belongs to by TaskID/timestamp - the caller must already know
// the exact evaluationID (e.g. from the record Evaluate returned).
func (e *ShadowEvaluator) AttachChampionOutcome(ctx context.Context, evaluationID string, executionID string, outcome ExecutionOutcome, quality QualityOutcome) error {
	return e.AttachOutcome(ctx, evaluationID, executionID, outcome, quality, "")
}

// AttachOutcome is ADR 0011's canary-aware generalization of
// AttachChampionOutcome. admittedChallenger is the AgentID Core
// actually dispatched for this decision - the EMPTY string for every
// pure-shadow decision (preserving Phase 9's "outcome belongs to
// Champion only" semantics exactly), or a durably-admitted Challenger's
// AgentID for a Phase 10 canary decision.
//
// The ExecutedSelection/ExecutedPolicy fields this sets are derived
// EXCLUSIVELY from the record's OWN ChampionAgentID/ChallengerAgentID -
// it can never attribute an outcome to a different decision's agent,
// and it only ever marks the Challenger as executed when
// admittedChallenger equals THIS record's own ChallengerAgentID.
// Historical records attached before this field existed, or any record
// for which admittedChallenger is empty, keep ExecutedSelection=Champion
// exactly as ADR 0008 originally specified - this method never rewrites
// a record's ExecutedSelection away from what actually happened.
func (e *ShadowEvaluator) AttachOutcome(ctx context.Context, evaluationID string, executionID string, outcome ExecutionOutcome, quality QualityOutcome, admittedChallenger supervisor.AgentID) error {
	existing, err := e.Store.Get(ctx, evaluationID)
	if err != nil {
		return err
	}
	existing.ExecutionID = executionID
	existing.ActualExecutionOutcome = outcome
	existing.ActualQualityOutcome = quality
	if admittedChallenger != "" && existing.ChallengerAgentID == admittedChallenger {
		existing.ExecutedSelection = existing.ChallengerAgentID
		existing.ExecutedPolicy = existing.ChallengerPolicy
	} else {
		existing.ExecutedSelection = existing.ChampionAgentID
		existing.ExecutedPolicy = existing.ChampionPolicy
	}
	existing.OutcomeAttached = true
	existing.OutcomeUpdatedAt = time.Now().UTC()
	return e.Store.Save(ctx, existing)
}

// AttachChampionQuality updates ONLY the quality-outcome dimension of
// an already-outcome-attached evaluation record - used when review
// finalizes AFTER execution already attached the (still-unreviewed)
// execution outcome. Preserves the existing ExecutionID/
// ActualExecutionOutcome untouched, so a later quality update can never
// accidentally overwrite them with a stale/zero value. Fails closed if
// the record does not exist.
func (e *ShadowEvaluator) AttachChampionQuality(ctx context.Context, evaluationID string, quality QualityOutcome) error {
	existing, err := e.Store.Get(ctx, evaluationID)
	if err != nil {
		return err
	}
	existing.ActualQualityOutcome = quality
	existing.OutcomeAttached = true
	existing.OutcomeUpdatedAt = time.Now().UTC()
	return e.Store.Save(ctx, existing)
}
