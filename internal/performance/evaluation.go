package performance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// EvaluationStatus classifies a shadow evaluation's RECOMMENDATION
// comparison - never an outcome comparison, since the Challenger never
// executes. ADR 0008 explicitly forbids "challenger_won"/
// "challenger_lost"-style statuses, since no real Challenger execution
// ever happened to win or lose.
type EvaluationStatus string

const (
	StatusSameRecommendation              EvaluationStatus = "same_recommendation"
	StatusDifferentRecommendation         EvaluationStatus = "different_recommendation"
	StatusChallengerInsufficientData      EvaluationStatus = "challenger_insufficient_data"
	StatusChallengerError                 EvaluationStatus = "challenger_error"
	StatusChallengerInvalidRecommendation EvaluationStatus = "challenger_invalid_recommendation"
	StatusEvaluationIncomplete            EvaluationStatus = "evaluation_incomplete"
)

// InvalidRecommendationReason is a typed, audit-oriented classification
// of WHY a Challenger's recommendation was rejected - for audit/
// security analysis/debugging only (ADR 0009 Part 14). It never
// weakens InvalidRecommendationCount's status as a hard promotion
// blocker; it only adds granularity to an already-blocked result. Only
// a reason Core can actually PROVE from authoritative data is ever
// used - never manufactured for test/display purposes.
type InvalidRecommendationReason string

const (
	InvalidReasonUnknownCandidate       InvalidRecommendationReason = "unknown_candidate"
	InvalidReasonNotInSnapshot          InvalidRecommendationReason = "candidate_not_in_snapshot"
	InvalidReasonTenantBoundary         InvalidRecommendationReason = "tenant_boundary"
	InvalidReasonCapabilityMismatch     InvalidRecommendationReason = "capability_mismatch"
	InvalidReasonAuthorizationViolation InvalidRecommendationReason = "authorization_violation"
	InvalidReasonInvalidPolicyOutput    InvalidRecommendationReason = "invalid_policy_output"
)

// RoutingEvaluationRecord is a shadow (or replay-time) comparison
// between a Champion's ACTUAL decision and one Challenger's
// recommendation-only output, over the SAME CandidateSnapshot.
//
// Decision facts (everything through Reasons/CandidateAgentIDs) are
// IMMUTABLE once saved - see MemoryEvaluationStore.Save. Outcome fields
// (ExecutionID onward) are the only fields a later AttachChampionOutcome
// call may update - ADR 0008 Part 16's explicit separation of "decision
// facts" from "outcome projection".
//
// ActualExecutionOutcome/ActualQualityOutcome belong EXCLUSIVELY to the
// EXECUTED Champion decision - there is no field anywhere on this type
// named anything like "ChallengerOutcome"/"PredictedOutcome"/
// "WouldHaveVerified" (ADR 0008's explicit "no fake challenger outcome"
// prohibition) - structurally impossible to populate, not merely
// unpopulated by convention.
type RoutingEvaluationRecord struct {
	EvaluationID string

	// DecisionID (ADR 0009D) is the first-class binding to the ONE
	// routing decision this record evaluates - TaskID remains a
	// queryable, fachlich grouping attribute (a task may have many
	// decisions: retries, reselections), but it is explicitly NOT what
	// gives this record its identity or what two records with the same
	// TaskID are distinguished by. Always equal to the CandidateSnapshot's
	// own DecisionID this record was evaluated against.
	DecisionID supervisor.DecisionID

	Tenant    string
	TaskID    supervisor.TaskID
	TaskClass TaskClass

	DecisionTime time.Time

	ChampionPolicy   RoutingPolicyDescriptor
	ChallengerPolicy RoutingPolicyDescriptor

	CandidateAgentIDs []string

	ChampionAgentID   supervisor.AgentID
	ChallengerAgentID supervisor.AgentID

	Status EvaluationStatus
	// InvalidReason is set only when Status ==
	// StatusChallengerInvalidRecommendation, giving audit/security
	// analysis a typed reason without weakening the hard promotion
	// blocker - InvalidRecommendationCount > 0 remains a hard blocker
	// regardless of which InvalidRecommendationReason value is recorded
	// here (ADR 0009 Part 14).
	InvalidReason InvalidRecommendationReason
	Reasons       []string

	// ExecutionID correlates to the Champion's REAL, Core-owned
	// execution (ADR 0006 Phase 7C's supervisor.ExecutionID) - never a
	// TaskID-latest or timestamp guess (see AttachChampionOutcome).
	ExecutionID string

	// ExecutedSelection/ExecutedPolicy (ADR 0011 Part 15) name WHICH of
	// this record's two candidates (ChampionAgentID/ChampionPolicy vs
	// ChallengerAgentID/ChallengerPolicy) the ActualExecutionOutcome/
	// ActualQualityOutcome fields below actually belong to. For every
	// pure-shadow record (Phase 9 and earlier, and any Phase 10 decision
	// where no Challenger was admitted) this is always the Champion -
	// ADR 0008's original "actual outcome belongs to Champion only"
	// invariant still holds exactly as before. Only a durably-admitted,
	// actually-dispatched Challenger (ADR 0011 canary execution) can
	// ever set this to the Challenger - see AttachOutcome.
	ExecutedSelection supervisor.AgentID
	ExecutedPolicy    RoutingPolicyDescriptor

	ActualExecutionOutcome ExecutionOutcome
	ActualQualityOutcome   QualityOutcome
	OutcomeAttached        bool

	CreatedAt        time.Time
	OutcomeUpdatedAt time.Time
}

// ComputeEvaluationID derives a deterministic idempotency key from
// stable decision provenance - NOT from a timestamp alone (ADR 0008
// Part 15's explicit instruction), and explicitly keyed on DecisionID
// (ADR 0009D), not merely on TaskID/CapturedAt as a proxy for it. Two
// evaluations of the EXACT same decision (same DecisionID, same
// snapshot content, same champion policy, same challenger policy)
// always produce the same ID - a replay of that event is therefore
// naturally idempotent - while a genuinely NEW decision for the SAME
// TaskID (a retry/reselection, which captures a NEW CandidateSnapshot
// under a NEW DecisionID) always produces a distinct ID, even if its
// candidate universe happens to be semantically identical to an earlier
// decision's (snapshot.fingerprint() deliberately excludes DecisionID,
// so two decisions CAN share one fingerprint while still producing
// different EvaluationIDs here, because DecisionID itself is hashed
// separately below).
func ComputeEvaluationID(snapshot CandidateSnapshot, championPolicy, challengerPolicy RoutingPolicyDescriptor) string {
	h := sha256.New()
	for _, part := range []string{string(snapshot.DecisionID), snapshot.fingerprint(), championPolicy.String(), challengerPolicy.String()} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// EvaluationFilter scopes an EvaluationStore query - Tenant is REQUIRED
// unless AllTenants is set, mirroring ObservationFilter's fail-closed
// tenant discipline exactly (ADR 0005/0006, unchanged pattern).
type EvaluationFilter struct {
	Tenant             string
	AllTenants         bool
	TaskClass          TaskClass
	ChallengerPolicyID RoutingPolicyID
}

func (f EvaluationFilter) validate() error {
	if !f.AllTenants && f.Tenant == "" {
		return &CrossTenantQueryDenied{Reason: "no tenant specified and AllTenants not explicitly set"}
	}
	return nil
}

func (f EvaluationFilter) matches(rec RoutingEvaluationRecord) bool {
	if !f.AllTenants && rec.Tenant != f.Tenant {
		return false
	}
	if f.TaskClass != "" && rec.TaskClass != f.TaskClass {
		return false
	}
	if f.ChallengerPolicyID != "" && rec.ChallengerPolicy.ID != f.ChallengerPolicyID {
		return false
	}
	return true
}

// EvaluationNotFound is returned by EvaluationStore.Get for an unknown
// EvaluationID.
type EvaluationNotFound struct{ EvaluationID string }

func (e *EvaluationNotFound) Error() string {
	return fmt.Sprintf("routing evaluation %q not found", e.EvaluationID)
}

// EvaluationIdentityConflict is returned when a Save would change an
// existing record's DECISION FACTS (everything except the outcome
// fields) - decision facts are immutable once recorded; only
// AttachChampionOutcome may update the outcome projection.
type EvaluationIdentityConflict struct{ EvaluationID string }

func (e *EvaluationIdentityConflict) Error() string {
	return fmt.Sprintf("routing evaluation %q already exists with different decision facts - decision facts are immutable", e.EvaluationID)
}

// EvaluationStore persists RoutingEvaluationRecords. Save is idempotent
// for an exact replay of the same decision facts, fails closed
// (*EvaluationIdentityConflict) if decision facts would change, and
// otherwise allows the outcome-projection fields to be updated in place
// (see decisionFactsEqual) - this is the "append-only decision facts,
// mutable outcome projection" split ADR 0008 Part 16 asks for, built on
// this package's existing Save-idempotency convention rather than a
// literal append-only log structure.
type EvaluationStore interface {
	Save(ctx context.Context, rec RoutingEvaluationRecord) error
	Get(ctx context.Context, evaluationID string) (RoutingEvaluationRecord, error)
	Query(ctx context.Context, filter EvaluationFilter) ([]RoutingEvaluationRecord, error)
}

func decisionFactsEqual(a, b RoutingEvaluationRecord) bool {
	if a.DecisionID != b.DecisionID {
		return false
	}
	if a.Tenant != b.Tenant || a.TaskID != b.TaskID || a.TaskClass != b.TaskClass {
		return false
	}
	if a.ChampionPolicy != b.ChampionPolicy || a.ChallengerPolicy != b.ChallengerPolicy {
		return false
	}
	if a.ChampionAgentID != b.ChampionAgentID || a.ChallengerAgentID != b.ChallengerAgentID {
		return false
	}
	if a.Status != b.Status {
		return false
	}
	if len(a.CandidateAgentIDs) != len(b.CandidateAgentIDs) {
		return false
	}
	for i := range a.CandidateAgentIDs {
		if a.CandidateAgentIDs[i] != b.CandidateAgentIDs[i] {
			return false
		}
	}
	return true
}

// MemoryEvaluationStore is a race-safe, deterministic in-memory
// EvaluationStore.
type MemoryEvaluationStore struct {
	mu      sync.Mutex
	records map[string]RoutingEvaluationRecord
}

func NewMemoryEvaluationStore() *MemoryEvaluationStore {
	return &MemoryEvaluationStore{records: map[string]RoutingEvaluationRecord{}}
}

func (s *MemoryEvaluationStore) Save(ctx context.Context, rec RoutingEvaluationRecord) error {
	if rec.EvaluationID == "" {
		return fmt.Errorf("routing evaluation record has no EvaluationID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.records[rec.EvaluationID]
	if !ok {
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = time.Now().UTC()
		}
		s.records[rec.EvaluationID] = rec
		return nil
	}
	if !decisionFactsEqual(existing, rec) {
		return &EvaluationIdentityConflict{EvaluationID: rec.EvaluationID}
	}
	// Decision facts match - allow the outcome-projection fields to
	// update in place (this is how AttachChampionOutcome persists).
	updated := existing
	updated.ExecutionID = rec.ExecutionID
	updated.ActualExecutionOutcome = rec.ActualExecutionOutcome
	updated.ActualQualityOutcome = rec.ActualQualityOutcome
	updated.OutcomeAttached = rec.OutcomeAttached
	updated.OutcomeUpdatedAt = rec.OutcomeUpdatedAt
	s.records[rec.EvaluationID] = updated
	return nil
}

func (s *MemoryEvaluationStore) Get(ctx context.Context, evaluationID string) (RoutingEvaluationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[evaluationID]
	if !ok {
		return RoutingEvaluationRecord{}, &EvaluationNotFound{EvaluationID: evaluationID}
	}
	return rec, nil
}

func (s *MemoryEvaluationStore) Query(ctx context.Context, filter EvaluationFilter) ([]RoutingEvaluationRecord, error) {
	if err := filter.validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []RoutingEvaluationRecord
	for _, rec := range s.records {
		if filter.matches(rec) {
			out = append(out, rec)
		}
	}
	return out, nil
}

var _ EvaluationStore = (*MemoryEvaluationStore)(nil)
