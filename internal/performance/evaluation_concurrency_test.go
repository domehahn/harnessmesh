package performance

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNegative_FailedTimedOutCancelledOutcomesAttachHonestly(t *testing.T) {
	evalStore := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())

	for _, outcome := range []ExecutionOutcome{ExecutionFailed, ExecutionTimedOut, ExecutionCancelled} {
		rec := RoutingEvaluationRecord{
			EvaluationID: ComputeEvaluationID(snap, championDesc(), RoutingPolicyDescriptor{ID: RoutingPolicyID("c-" + string(outcome)), Version: "v1"}),
			Tenant:       "tenant-a", TaskID: "T1", TaskClass: TaskClassSecurityReview,
			ChampionPolicy:    championDesc(),
			ChallengerPolicy:  RoutingPolicyDescriptor{ID: RoutingPolicyID("c-" + string(outcome)), Version: "v1"},
			CandidateAgentIDs: snap.AgentIDs, ChampionAgentID: "agent-A", ChallengerAgentID: "agent-B",
			Status: StatusDifferentRecommendation,
		}
		if err := evalStore.Save(context.Background(), rec); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if err := evaluator.AttachChampionOutcome(context.Background(), rec.EvaluationID, "T1@"+string(outcome), outcome, QualityUnreviewed); err != nil {
			t.Fatalf("AttachChampionOutcome: %v", err)
		}
		final, err := evalStore.Get(context.Background(), rec.EvaluationID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if final.ActualExecutionOutcome != outcome {
			t.Fatalf("expected the non-successful outcome %q preserved honestly, got %q", outcome, final.ActualExecutionOutcome)
		}
	}
}

func TestNegative_PendingOutcomeNeverAttachedAsVerified(t *testing.T) {
	evalStore := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	rec := RoutingEvaluationRecord{
		EvaluationID: ComputeEvaluationID(snap, championDesc(), challengerDesc()),
		Tenant:       "tenant-a", TaskID: "T1", TaskClass: TaskClassSecurityReview,
		ChampionPolicy: championDesc(), ChallengerPolicy: challengerDesc(),
		CandidateAgentIDs: snap.AgentIDs, ChampionAgentID: "agent-A",
		Status: StatusSameRecommendation,
	}
	evalStore.Save(context.Background(), rec)
	// Execution completed but review has not yet reached a terminal
	// verdict - the caller must attach QualityUnreviewed, never
	// fabricate QualityVerified just because execution succeeded.
	if err := evaluator.AttachChampionOutcome(context.Background(), rec.EvaluationID, "T1@E1", ExecutionCompleted, QualityUnreviewed); err != nil {
		t.Fatalf("AttachChampionOutcome: %v", err)
	}
	final, _ := evalStore.Get(context.Background(), rec.EvaluationID)
	if final.ActualQualityOutcome == QualityVerified {
		t.Fatalf("expected an unreviewed outcome to never be attached as verified")
	}
}

func TestNegative_MissingPolicyVersionDistinctFromSet(t *testing.T) {
	withVersion := RoutingPolicyDescriptor{ID: "performance", Version: "v1"}
	withoutVersion := RoutingPolicyDescriptor{ID: "performance", Version: ""}
	if withVersion == withoutVersion {
		t.Fatalf("expected a missing Version to be a DISTINCT identity from an explicit one")
	}
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	if ComputeEvaluationID(snap, championDesc(), withVersion) == ComputeEvaluationID(snap, championDesc(), withoutVersion) {
		t.Fatalf("expected different EvaluationIDs for a missing vs. explicit policy version")
	}
}

// TestConcurrency_FullEvaluationLifecycle exercises ADR 0008 Part 27's
// required sequence under -race: champion selects (given, static),
// multiple challengers evaluate concurrently, execution/finalization
// happens concurrently on the underlying performance store, outcome
// attachment races against a concurrent summary query.
func TestConcurrency_FullEvaluationLifecycle(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	seedFinalized(t, pstore, "tenant-a", "agent-A", TaskClassSecurityReview, 15, 12, 2)
	seedFinalized(t, pstore, "tenant-a", "agent-B", TaskClassSecurityReview, 15, 10, 4)

	snapshot := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())
	evalStore := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)

	challengerA := NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: NewStoreDataset(pstore), Tenant: "tenant-a"}, RoutingPolicyDescriptor{ID: "performance", Version: "v1"})
	challengerB := NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: NewStoreDataset(pstore), Tenant: "tenant-a"}, RoutingPolicyDescriptor{ID: "pair-aware", Version: "v1"})

	var wg sync.WaitGroup
	wg.Add(4)

	// Two concurrent challenger evaluations for the same decision.
	go func() {
		defer wg.Done()
		if _, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challengerA}); err != nil {
			t.Errorf("Evaluate A: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := evaluator.Evaluate(context.Background(), snapshot, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challengerB}); err != nil {
			t.Errorf("Evaluate B: %v", err)
		}
	}()

	// Concurrent finalization of unrelated observations on the same
	// underlying store.
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			seedObservationWithUsages(t, pstore, "tenant-a", "agent-A", TaskClassSecurityReview, "conc"+string(rune('a'+i)), ExecutionCompleted, QualityVerified, nil)
		}
	}()

	// Concurrent summary/query reads.
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			if _, err := evalStore.Query(context.Background(), EvaluationFilter{Tenant: "tenant-a"}); err != nil {
				t.Errorf("Query: %v", err)
			}
			if _, err := Aggregate(context.Background(), NewStoreDataset(pstore), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview); err != nil {
				t.Errorf("Aggregate: %v", err)
			}
		}
	}()
	wg.Wait()

	all, err := evalStore.Query(context.Background(), EvaluationFilter{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("final Query: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected exactly 2 evaluation records (one per challenger), got %d", len(all))
	}

	// Now attach the champion's outcome concurrently with a summary
	// query racing against it.
	var wg2 sync.WaitGroup
	wg2.Add(2)
	go func() {
		defer wg2.Done()
		for _, rec := range all {
			if err := evaluator.AttachChampionOutcome(context.Background(), rec.EvaluationID, "T1@E1", ExecutionCompleted, QualityVerified); err != nil {
				t.Errorf("AttachChampionOutcome: %v", err)
			}
		}
	}()
	go func() {
		defer wg2.Done()
		for i := 0; i < 10; i++ {
			if _, err := evalStore.Query(context.Background(), EvaluationFilter{Tenant: "tenant-a"}); err != nil {
				t.Errorf("Query during attach: %v", err)
			}
		}
	}()
	wg2.Wait()

	finalRecords, err := evalStore.Query(context.Background(), EvaluationFilter{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, rec := range finalRecords {
		if !rec.OutcomeAttached {
			t.Fatalf("expected every record's outcome to be attached, got %+v", rec)
		}
		if rec.ChampionAgentID != "agent-A" {
			t.Fatalf("expected outcome attached to champion agent-A, got %q", rec.ChampionAgentID)
		}
	}
}
