package performance

import (
	"context"
	"testing"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

func seedObservations(t *testing.T, store *MemoryPerformanceStore, n int, agent string, finalize bool) {
	t.Helper()
	ctx := context.Background()
	prefix := "pending-"
	if finalize {
		prefix = "final-"
	}
	for i := 0; i < n; i++ {
		in := baseInput()
		in.TaskID = supervisor.TaskID(prefix + agent + "-" + string(rune('A'+i)))
		in.ExecutionID = string(in.TaskID) + "#1"
		in.AgentID = agentIDLike(agent)
		obs, err := NewObservation(in)
		if err != nil {
			t.Fatalf("NewObservation: %v", err)
		}
		if finalize {
			obs.Status = ObservationFinalized
			obs.QualityOutcome = QualityVerified
		}
		if err := store.Save(ctx, obs); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
}

func TestAggregate_InsufficientDataFlaggedForColdStartAgent(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedObservations(t, store, 2, "new-agent", true) // well below DefaultMinimumObservationCount
	dataset := NewStoreDataset(store)

	summary, err := Aggregate(context.Background(), dataset, ObservationFilter{Tenant: "tenant-a"}, "new-agent", TaskClassSecurityReview)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if !summary.InsufficientData {
		t.Fatalf("expected InsufficientData=true for only 2 observations")
	}
	// Critically: insufficient data must NOT be translated into a bad
	// score - there IS no score field at all, and the honest counts are
	// still reported.
	if summary.VerifiedCount != 2 {
		t.Fatalf("expected the 2 real verified observations still counted honestly, got %d", summary.VerifiedCount)
	}
}

func TestNegative_ZeroSampleAggregationReportsZeroNotError(t *testing.T) {
	store := NewMemoryPerformanceStore()
	dataset := NewStoreDataset(store)
	summary, err := Aggregate(context.Background(), dataset, ObservationFilter{Tenant: "tenant-a"}, "nonexistent-agent", TaskClassSecurityReview)
	if err != nil {
		t.Fatalf("Aggregate should not error on zero samples: %v", err)
	}
	if summary.ObservationCount != 0 || !summary.InsufficientData {
		t.Fatalf("expected 0 observations and InsufficientData=true, got %+v", summary)
	}
}

func TestAggregate_SufficientDataNotFlagged(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedObservations(t, store, DefaultMinimumObservationCount+5, "established-agent", true)
	dataset := NewStoreDataset(store)
	summary, err := Aggregate(context.Background(), dataset, ObservationFilter{Tenant: "tenant-a"}, "established-agent", TaskClassSecurityReview)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if summary.InsufficientData {
		t.Fatalf("expected sufficient data for %d observations", summary.ObservationCount)
	}
}

func TestAggregate_SeparatesPendingAndFinalized(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedObservations(t, store, DefaultMinimumObservationCount, "agent-mixed", true)
	seedObservations(t, store, 3, "agent-mixed", false) // still pending
	dataset := NewStoreDataset(store)
	summary, err := Aggregate(context.Background(), dataset, ObservationFilter{Tenant: "tenant-a"}, "agent-mixed", TaskClassSecurityReview)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if summary.FinalizedCount != DefaultMinimumObservationCount {
		t.Fatalf("expected %d finalized, got %d", DefaultMinimumObservationCount, summary.FinalizedCount)
	}
	if summary.PendingCount != 3 {
		t.Fatalf("expected 3 pending, got %d", summary.PendingCount)
	}
}

func TestNegative_FailedAndCancelledExecutionsNotExcludedFromMetrics(t *testing.T) {
	store := NewMemoryPerformanceStore()
	ctx := context.Background()
	for i, outcome := range []ExecutionOutcome{ExecutionFailed, ExecutionTimedOut, ExecutionCancelled, ExecutionCompleted} {
		in := baseInput()
		in.TaskID = taskIDLike(i)
		in.ExecutionID = string(in.TaskID) + "#1"
		in.ExecutionOutcome = outcome
		obs, err := NewObservation(in)
		if err != nil {
			t.Fatalf("NewObservation: %v", err)
		}
		store.Save(ctx, obs)
	}
	dataset := NewStoreDataset(store)
	summary, err := Aggregate(ctx, dataset, ObservationFilter{Tenant: "tenant-a"}, "security-agent-a", TaskClassSecurityReview)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if summary.ObservationCount != 4 {
		t.Fatalf("expected all 4 outcomes (including failed/timed-out/cancelled) counted, got %d", summary.ObservationCount)
	}
	if summary.FailedCount != 1 || summary.TimedOutCount != 1 || summary.CancelledCount != 1 || summary.CompletedCount != 1 {
		t.Fatalf("expected one of each outcome bucket, got %+v", summary)
	}
}

func TestAggregateModelUsage_SeparateFromAgentPerformance(t *testing.T) {
	store := NewMemoryPerformanceStore()
	ctx := context.Background()
	in := baseInput()
	in.ModelUsages = []ModelUsageRef{{RouteID: "strong-reasoning", Model: "model-x", Count: 3}, {RouteID: "strong-reasoning", Model: "model-y", Count: 1}}
	obs, _ := NewObservation(in)
	store.Save(ctx, obs)

	dataset := NewStoreDataset(store)
	usage, err := AggregateModelUsage(ctx, dataset, ObservationFilter{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("AggregateModelUsage: %v", err)
	}
	if len(usage) != 2 {
		t.Fatalf("expected 2 distinct (route,model) pairs, got %d", len(usage))
	}
}

func taskIDLike(i int) supervisor.TaskID         { return supervisor.TaskID("T" + string(rune('A'+i))) }
func agentIDLike(name string) supervisor.AgentID { return supervisor.AgentID(name) }
