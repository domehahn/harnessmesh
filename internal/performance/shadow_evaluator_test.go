package performance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// testSnapshot builds a snapshot for a FRESH decision (a new,
// Core-owned DecisionID each call) - exactly mirroring how two separate
// production decisions, even for the same TaskID, each get their own
// distinct decision identity.
func testSnapshot(t *testing.T, tenant string, taskID supervisor.TaskID, taskClass TaskClass, agents []string, capturedAt time.Time) CandidateSnapshot {
	t.Helper()
	return testSnapshotForDecision(t, supervisor.NewDecisionID(taskID), tenant, taskID, taskClass, agents, capturedAt)
}

func testSnapshotForDecision(t *testing.T, decisionID supervisor.DecisionID, tenant string, taskID supervisor.TaskID, taskClass TaskClass, agents []string, capturedAt time.Time) CandidateSnapshot {
	t.Helper()
	candidates := make([]supervisor.AgentCandidate, len(agents))
	for i, a := range agents {
		candidates[i] = supervisor.AgentCandidate{AgentID: supervisor.AgentID(a)}
	}
	snap, err := NewCandidateSnapshot(decisionID, tenant, taskID, taskClass, candidates, capturedAt, DefaultEvaluationLimits())
	if err != nil {
		t.Fatalf("NewCandidateSnapshot: %v", err)
	}
	return snap
}

func championDesc() RoutingPolicyDescriptor {
	return RoutingPolicyDescriptor{ID: "legacy", Version: "v1"}
}
func challengerDesc() RoutingPolicyDescriptor {
	return RoutingPolicyDescriptor{ID: "performance", Version: "v1"}
}

// --- End-to-End Shadow Test (ADR 0008 Part 21) ---

func TestE2E_ShadowEvaluation_DifferentRecommendation(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedFinalized(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, 20, 12, 5)
	seedFinalized(t, store, "tenant-a", "agent-B", TaskClassSecurityReview, 20, 18, 1)

	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	challenger := NewAdvisorRoutingPolicy(advisor, challengerDesc())

	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())
	evalStore := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)

	records, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]
	if rec.Status != StatusDifferentRecommendation {
		t.Fatalf("expected different_recommendation (legacy->A, performance->B), got %q (champion=%q challenger=%q)", rec.Status, rec.ChampionAgentID, rec.ChallengerAgentID)
	}
	if rec.ChampionAgentID != "agent-A" || rec.ChallengerAgentID != "agent-B" {
		t.Fatalf("unexpected agent IDs: champion=%q challenger=%q", rec.ChampionAgentID, rec.ChallengerAgentID)
	}

	// Actual execution: Agent A, ExecutionID E1, completed+verified.
	if err := evaluator.AttachChampionOutcome(context.Background(), rec.EvaluationID, "T1@E1", ExecutionCompleted, QualityVerified); err != nil {
		t.Fatalf("AttachChampionOutcome: %v", err)
	}
	final, err := evalStore.Get(context.Background(), rec.EvaluationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if final.ExecutionID != "T1@E1" || final.ActualExecutionOutcome != ExecutionCompleted || final.ActualQualityOutcome != QualityVerified {
		t.Fatalf("expected outcome attached to champion, got %+v", final)
	}
	if final.ChallengerAgentID != "agent-B" {
		t.Fatalf("challenger recommendation must remain recorded, got %q", final.ChallengerAgentID)
	}
	// Status must never claim challenger "won" or "lost".
	if final.Status == StatusSameRecommendation {
		t.Fatalf("status should remain different_recommendation after outcome attachment")
	}
}

func TestShadowEvaluation_SameRecommendation(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedFinalized(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, 20, 18, 1)
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	challenger := NewAdvisorRoutingPolicy(advisor, challengerDesc())

	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	evaluator := NewShadowEvaluator(NewMemoryEvaluationStore(), DefaultEvaluationLimits(), nil)

	records, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if records[0].Status != StatusSameRecommendation {
		t.Fatalf("expected same_recommendation, got %q", records[0].Status)
	}
}

func TestNegative_ChallengerReturnsUnknownAgent(t *testing.T) {
	challenger := StaticRoutingPolicy{Descriptor_: challengerDesc(), AgentID: "nonexistent-agent"}
	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	evaluator := NewShadowEvaluator(NewMemoryEvaluationStore(), DefaultEvaluationLimits(), nil)

	records, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if records[0].Status != StatusChallengerInvalidRecommendation {
		t.Fatalf("expected challenger_invalid_recommendation, got %q", records[0].Status)
	}
}

func TestNegative_ChallengerReturnsAgentOutsideSnapshot(t *testing.T) {
	// "agent-C" is a real, otherwise-valid agent ID, but it was never
	// part of THIS decision's snapshot (e.g. it belongs to another
	// tenant, or was filtered for capability/availability reasons).
	challenger := StaticRoutingPolicy{Descriptor_: challengerDesc(), AgentID: "agent-C"}
	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())
	evaluator := NewShadowEvaluator(NewMemoryEvaluationStore(), DefaultEvaluationLimits(), nil)

	records, _ := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
	if records[0].Status != StatusChallengerInvalidRecommendation {
		t.Fatalf("expected challenger_invalid_recommendation for an agent outside the snapshot, got %q", records[0].Status)
	}
}

func TestNegative_ChallengerReturnsCrossTenantAgent(t *testing.T) {
	// A cross-tenant agent could never have been in the snapshot to
	// begin with (snapshots are built from already-tenant-filtered
	// candidates) - this proves that structural guarantee end to end:
	// even though "agent-foreign" is a real agent elsewhere in the
	// system, it is absent from tenant-a's snapshot, so a challenger
	// naming it is treated identically to any other invalid recommendation.
	challenger := StaticRoutingPolicy{Descriptor_: challengerDesc(), AgentID: "agent-foreign"}
	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	evaluator := NewShadowEvaluator(NewMemoryEvaluationStore(), DefaultEvaluationLimits(), nil)

	records, _ := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
	if records[0].Status != StatusChallengerInvalidRecommendation {
		t.Fatalf("expected challenger_invalid_recommendation for a cross-tenant agent, got %q", records[0].Status)
	}
}

type erroringPolicy struct{ desc RoutingPolicyDescriptor }

func (p erroringPolicy) Descriptor() RoutingPolicyDescriptor { return p.desc }
func (p erroringPolicy) Recommend(ctx context.Context, input RoutingEvaluationInput) (RoutingRecommendation, error) {
	return RoutingRecommendation{}, errors.New("challenger backend unavailable")
}

func TestNegative_ChallengerException(t *testing.T) {
	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	evaluator := NewShadowEvaluator(NewMemoryEvaluationStore(), DefaultEvaluationLimits(), nil)
	records, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), []RoutingPolicy{erroringPolicy{desc: challengerDesc()}})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if records[0].Status != StatusChallengerError {
		t.Fatalf("expected challenger_error, got %q", records[0].Status)
	}
}

type panickingPolicy struct{ desc RoutingPolicyDescriptor }

func (p panickingPolicy) Descriptor() RoutingPolicyDescriptor { return p.desc }
func (p panickingPolicy) Recommend(ctx context.Context, input RoutingEvaluationInput) (RoutingRecommendation, error) {
	panic("challenger panic")
}

func TestNegative_ChallengerPanicRecordedAsError(t *testing.T) {
	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	evaluator := NewShadowEvaluator(NewMemoryEvaluationStore(), DefaultEvaluationLimits(), nil)
	records, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), []RoutingPolicy{panickingPolicy{desc: challengerDesc()}})
	if err != nil {
		t.Fatalf("Evaluate must not itself fail on a panicking challenger: %v", err)
	}
	if records[0].Status != StatusChallengerError {
		t.Fatalf("expected challenger_error for a panicking challenger, got %q", records[0].Status)
	}
}

func TestNegative_AllCandidatesColdStart(t *testing.T) {
	store := NewMemoryPerformanceStore() // no history at all
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	challenger := NewAdvisorRoutingPolicy(advisor, challengerDesc())
	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())
	evaluator := NewShadowEvaluator(NewMemoryEvaluationStore(), DefaultEvaluationLimits(), nil)

	records, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if records[0].Status != StatusChallengerInsufficientData {
		t.Fatalf("expected challenger_insufficient_data when all candidates are cold-start, got %q", records[0].Status)
	}
}

func TestNegative_DuplicateEvaluationEventIdempotent(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedFinalized(t, store, "tenant-a", "agent-B", TaskClassSecurityReview, 20, 18, 1)
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	challenger := NewAdvisorRoutingPolicy(advisor, challengerDesc())
	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	evalStore := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	decisionTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if _, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", decisionTime, []RoutingPolicy{challenger}); err != nil {
		t.Fatalf("Evaluate 1: %v", err)
	}
	if _, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", decisionTime, []RoutingPolicy{challenger}); err != nil {
		t.Fatalf("Evaluate 2 (replay): %v", err)
	}

	all, err := evalStore.Query(context.Background(), EvaluationFilter{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 evaluation record after a duplicate shadow-event replay, got %d", len(all))
	}
}

func TestNegative_TooManyChallengers(t *testing.T) {
	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	limits := DefaultEvaluationLimits()
	limits.MaxChallengersPerDecision = 2
	evaluator := NewShadowEvaluator(NewMemoryEvaluationStore(), limits, nil)

	challengers := []RoutingPolicy{
		StaticRoutingPolicy{Descriptor_: RoutingPolicyDescriptor{ID: "c1", Version: "v1"}, AgentID: "agent-A"},
		StaticRoutingPolicy{Descriptor_: RoutingPolicyDescriptor{ID: "c2", Version: "v1"}, AgentID: "agent-A"},
		StaticRoutingPolicy{Descriptor_: RoutingPolicyDescriptor{ID: "c3", Version: "v1"}, AgentID: "agent-A"},
	}
	_, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), challengers)
	if err == nil {
		t.Fatalf("expected a TooManyChallengers error")
	}
	if _, ok := err.(*TooManyChallengers); !ok {
		t.Fatalf("expected *TooManyChallengers, got %T", err)
	}
}

func TestNegative_HugeReasonsBounded(t *testing.T) {
	hugeReasons := make([]string, 50)
	for i := range hugeReasons {
		hugeReasons[i] = string(make([]byte, 10000))
	}
	challenger := StaticRoutingPolicy{Descriptor_: challengerDesc(), AgentID: "agent-A", Reasons: hugeReasons}
	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	evaluator := NewShadowEvaluator(NewMemoryEvaluationStore(), DefaultEvaluationLimits(), nil)

	records, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(records[0].Reasons) > DefaultMaxReasonCount {
		t.Fatalf("expected reasons bounded to %d, got %d", DefaultMaxReasonCount, len(records[0].Reasons))
	}
	for _, r := range records[0].Reasons {
		if len(r) > DefaultMaxReasonLength {
			t.Fatalf("expected each reason bounded to %d bytes, got %d", DefaultMaxReasonLength, len(r))
		}
	}
}

func TestNegative_HugeCandidateListRejected(t *testing.T) {
	candidates := make([]supervisor.AgentCandidate, DefaultMaxCandidatesPerSnapshot+1)
	for i := range candidates {
		candidates[i] = supervisor.AgentCandidate{AgentID: supervisor.AgentID("agent-" + string(rune(i)))}
	}
	_, err := NewCandidateSnapshot(supervisor.NewDecisionID("T1"), "tenant-a", "T1", TaskClassSecurityReview, candidates, time.Now(), DefaultEvaluationLimits())
	if err != ErrSnapshotTooLarge {
		t.Fatalf("expected ErrSnapshotTooLarge, got %v", err)
	}
}

func TestNegative_OutcomeOnlyAttachesToChampion(t *testing.T) {
	evalStore := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())
	rec := RoutingEvaluationRecord{
		EvaluationID: ComputeEvaluationID(snapshot, championDesc(), challengerDesc()),
		Tenant:       "tenant-a", TaskID: "T1", TaskClass: TaskClassSecurityReview,
		ChampionPolicy: championDesc(), ChallengerPolicy: challengerDesc(),
		CandidateAgentIDs: snapshot.AgentIDs,
		ChampionAgentID:   "agent-A", ChallengerAgentID: "agent-B",
		Status: StatusDifferentRecommendation,
	}
	if err := evalStore.Save(context.Background(), rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := evaluator.AttachChampionOutcome(context.Background(), rec.EvaluationID, "T1@E1", ExecutionCompleted, QualityVerified); err != nil {
		t.Fatalf("AttachChampionOutcome: %v", err)
	}
	final, _ := evalStore.Get(context.Background(), rec.EvaluationID)
	if final.ChampionAgentID != "agent-A" {
		t.Fatalf("outcome must attach to the champion's agent, got champion=%q", final.ChampionAgentID)
	}
	// There is structurally no field to check for a "challenger outcome"
	// - TestNegative_RoutingEvaluationRecordCannotFakeChallengerOutcome
	// already proves this; here we additionally confirm the challenger's
	// OWN recommendation is left completely unchanged by the attach call.
	if final.ChallengerAgentID != "agent-B" {
		t.Fatalf("challenger recommendation must remain unchanged, got %q", final.ChallengerAgentID)
	}
}

func TestNegative_AttachOutcomeToUnknownEvaluationFailsClosed(t *testing.T) {
	evaluator := NewShadowEvaluator(NewMemoryEvaluationStore(), DefaultEvaluationLimits(), nil)
	err := evaluator.AttachChampionOutcome(context.Background(), "nonexistent-id", "T1@E1", ExecutionCompleted, QualityVerified)
	if err == nil {
		t.Fatalf("expected a fail-closed error for an unknown evaluation ID")
	}
}

func TestNegative_PolicyVersionCollisionDetectedViaDistinctIDs(t *testing.T) {
	// performance-v1 and performance-v2 must never silently share one
	// identity - proven by their String() forms differing, which is
	// what ComputeEvaluationID's idempotency key relies on.
	v1 := RoutingPolicyDescriptor{ID: "performance", Version: "v1"}
	v2 := RoutingPolicyDescriptor{ID: "performance", Version: "v2"}
	if v1.String() == v2.String() {
		t.Fatalf("expected performance-v1 and performance-v2 to have distinct identities")
	}
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	if ComputeEvaluationID(snap, championDesc(), v1) == ComputeEvaluationID(snap, championDesc(), v2) {
		t.Fatalf("expected different EvaluationIDs for different policy versions")
	}
}
