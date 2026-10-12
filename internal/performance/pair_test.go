package performance

import (
	"context"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

func seedObservationWithUsages(t *testing.T, store PerformanceStore, tenant string, agent supervisor.AgentID, taskClass TaskClass, executionSuffix string, outcome ExecutionOutcome, quality QualityOutcome, usages []ModelUsageRef) PerformanceObservation {
	t.Helper()
	in := NewObservationInput{
		Tenant: tenant, TaskID: supervisor.TaskID(string(agent) + "-" + executionSuffix),
		ExecutionID: string(agent) + "-" + executionSuffix + "#exec",
		TaskClass:   taskClass, AgentID: agent, ExecutionOutcome: outcome,
		ModelUsages: usages,
		StartedAt:   time.Now(), FinishedAt: time.Now().Add(time.Second),
		Metrics: PerformanceMetrics{Duration: time.Second},
	}
	obs, err := NewObservation(in)
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	obs.Status = ObservationFinalized
	obs.QualityOutcome = quality
	if err := store.Save(context.Background(), obs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return obs
}

func TestPair_AggregatesByAgentTaskClassModel(t *testing.T) {
	store := NewMemoryPerformanceStore()
	for i := 0; i < 5; i++ {
		seedObservationWithUsages(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, "x"+string(rune('a'+i)), ExecutionCompleted, QualityVerified,
			[]ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}})
	}
	// Unrelated observation using a different model - must not count.
	seedObservationWithUsages(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, "other", ExecutionCompleted, QualityVerified,
		[]ModelUsageRef{{RouteID: "R2", Provider: "switchyard", Model: "model-y", Count: 1}})

	summary, err := AggregatePair(context.Background(), NewStoreDataset(store), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "model-x")
	if err != nil {
		t.Fatalf("AggregatePair: %v", err)
	}
	if summary.ObservationCount != 5 {
		t.Fatalf("expected 5 matching observations, got %d", summary.ObservationCount)
	}
	if summary.VerifiedCount != 5 {
		t.Fatalf("expected 5 verified, got %d", summary.VerifiedCount)
	}
}

func TestPair_ExecutionLevelPresenceVsInferenceUsageCount(t *testing.T) {
	store := NewMemoryPerformanceStore()
	// E2: Call1 R1->ModelX, Call2 R1->ModelX (merged to Count=2 by
	// convertModelUsages' dedup - but here we construct NewObservationInput
	// directly, bypassing that merge, to prove AggregatePair itself sums
	// Count correctly even across multiple distinct ModelUsageRef entries
	// for the same model), Call3 R2->ModelY.
	seedObservationWithUsages(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, "e2", ExecutionCompleted, QualityVerified,
		[]ModelUsageRef{
			{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 2},
			{RouteID: "R2", Provider: "switchyard", Model: "model-y", Count: 1},
		})

	summaryX, err := AggregatePair(context.Background(), NewStoreDataset(store), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "model-x")
	if err != nil {
		t.Fatalf("AggregatePair(model-x): %v", err)
	}
	if summaryX.ObservationCount != 1 {
		t.Fatalf("expected execution-level presence = 1 execution involving model-x, got %d", summaryX.ObservationCount)
	}
	if summaryX.InferenceUsageCount != 2 {
		t.Fatalf("expected inference-usage count = 2 for model-x, got %d", summaryX.InferenceUsageCount)
	}

	summaryY, err := AggregatePair(context.Background(), NewStoreDataset(store), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "model-y")
	if err != nil {
		t.Fatalf("AggregatePair(model-y): %v", err)
	}
	if summaryY.ObservationCount != 1 {
		t.Fatalf("expected execution-level presence = 1 for model-y, got %d", summaryY.ObservationCount)
	}
	if summaryY.InferenceUsageCount != 1 {
		t.Fatalf("expected inference-usage count = 1 for model-y, got %d", summaryY.InferenceUsageCount)
	}
}

func TestNegative_MultiModelExecutionNeverCollapsedToSingleModel(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedObservationWithUsages(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, "e2", ExecutionCompleted, QualityVerified,
		[]ModelUsageRef{
			{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 2},
			{RouteID: "R2", Provider: "switchyard", Model: "model-y", Count: 1},
		})
	all, _ := NewStoreDataset(store).Observations(context.Background(), ObservationFilter{Tenant: "tenant-a"})
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 observation (not 3, one per call), got %d", len(all))
	}
	involvingX, usagesX := CountModelUsage(all, "", "", "model-x")
	involvingY, usagesY := CountModelUsage(all, "", "", "model-y")
	if involvingX != 1 || usagesX != 2 {
		t.Fatalf("expected 1 execution / 2 usages for model-x, got %d/%d", involvingX, usagesX)
	}
	if involvingY != 1 || usagesY != 1 {
		t.Fatalf("expected 1 execution / 1 usage for model-y, got %d/%d", involvingY, usagesY)
	}
	// Quality (verified) must be counted ONCE for this execution, not
	// once per model call.
	quality, _ := AggregatePair(context.Background(), NewStoreDataset(store), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "model-x")
	if quality.VerifiedCount != 1 {
		t.Fatalf("expected verified count 1 (execution-level), got %d", quality.VerifiedCount)
	}
}

func TestPair_SampleSizeInsufficientWhenSparse(t *testing.T) {
	store := NewMemoryPerformanceStore()
	// Agent-level: 12 observations (sufficient, >= DefaultMinimumObservationCount).
	for i := 0; i < 12; i++ {
		usages := []ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-common", Count: 1}}
		if i < 4 {
			// Only 4 of them used model-x - pair-level sparse.
			usages = []ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}}
		}
		seedObservationWithUsages(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, "o"+string(rune('a'+i)), ExecutionCompleted, QualityVerified, usages)
	}

	agentLevel, err := Aggregate(context.Background(), NewStoreDataset(store), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if agentLevel.InsufficientData {
		t.Fatalf("expected agent-level evidence (n=12) to be sufficient")
	}

	pairLevel, err := AggregatePair(context.Background(), NewStoreDataset(store), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "model-x")
	if err != nil {
		t.Fatalf("AggregatePair: %v", err)
	}
	if !pairLevel.InsufficientData {
		t.Fatalf("expected pair-level evidence (n=4) to be marked insufficient, got sufficient with count=%d", pairLevel.ObservationCount)
	}
}

func TestNegative_CrossTenantPairQueryDenied(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedObservationWithUsages(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, "e1", ExecutionCompleted, QualityVerified,
		[]ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}})

	_, err := AggregatePair(context.Background(), NewStoreDataset(store), ObservationFilter{}, "agent-A", TaskClassSecurityReview, "", "", "model-x")
	if err == nil {
		t.Fatalf("expected a fail-closed error for an unscoped (no tenant) pair query")
	}
}

func TestNegative_CrossTaskClassPairComparisonNeverHappens(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedObservationWithUsages(t, store, "tenant-a", "agent-A", TaskClassDocumentation, "e1", ExecutionCompleted, QualityVerified,
		[]ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}})

	summary, err := AggregatePair(context.Background(), NewStoreDataset(store), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "model-x")
	if err != nil {
		t.Fatalf("AggregatePair: %v", err)
	}
	if summary.ObservationCount != 0 {
		t.Fatalf("expected a documentation-task-class observation to never count toward a security_review pair cohort, got %d", summary.ObservationCount)
	}
}

func TestNegative_FailedAndTimedOutExecutionsCountedInPairMetrics(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedObservationWithUsages(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, "fail1", ExecutionFailed, QualityUnreviewed,
		[]ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}})
	seedObservationWithUsages(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, "timeout1", ExecutionTimedOut, QualityUnreviewed,
		[]ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}})

	summary, err := AggregatePair(context.Background(), NewStoreDataset(store), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "model-x")
	if err != nil {
		t.Fatalf("AggregatePair: %v", err)
	}
	if summary.FailedCount != 1 || summary.TimeoutCount != 1 {
		t.Fatalf("expected failed/timeout executions counted, not survivorship-biased away, got %+v", summary)
	}
	if summary.ObservationCount != 2 {
		t.Fatalf("expected both non-successful executions still counted, got %d", summary.ObservationCount)
	}
}

func TestNegative_PendingObservationNotCountedAsVerifiedInPairMetrics(t *testing.T) {
	store := NewMemoryPerformanceStore()
	in := NewObservationInput{
		Tenant: "tenant-a", TaskID: "agent-A-pending1", ExecutionID: "agent-A-pending1#exec",
		TaskClass: TaskClassSecurityReview, AgentID: "agent-A", ExecutionOutcome: ExecutionCompleted,
		ModelUsages: []ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}},
		StartedAt:   time.Now(), FinishedAt: time.Now().Add(time.Second),
		Metrics: PerformanceMetrics{Duration: time.Second},
	}
	obs, err := NewObservation(in) // left Pending (QualityUnreviewed) - never finalized
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	if err := store.Save(context.Background(), obs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	summary, err := AggregatePair(context.Background(), NewStoreDataset(store), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "model-x")
	if err != nil {
		t.Fatalf("AggregatePair: %v", err)
	}
	if summary.VerifiedCount != 0 {
		t.Fatalf("expected a pending/unreviewed observation to never count as verified, got %d", summary.VerifiedCount)
	}
	if summary.FinalizedCount != 0 {
		t.Fatalf("expected the pending observation to not count as finalized, got %d", summary.FinalizedCount)
	}
}

func TestModelUsageDedup_DuplicateRefsMergedBySummingCount(t *testing.T) {
	in := []supervisor.ModelUsageRef{
		{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1},
		{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}, // duplicate report of the same call/route/model
		{RouteID: "R2", Provider: "switchyard", Model: "model-y", Count: 1},
	}
	out := convertModelUsages(in)
	if len(out) != 2 {
		t.Fatalf("expected duplicate (RouteID,Provider,Model) entries merged into 1, got %d entries: %+v", len(out), out)
	}
	for _, u := range out {
		if u.RouteID == "R1" && u.Count != 2 {
			t.Fatalf("expected merged Count=2 for the duplicated model-x usage, got %d", u.Count)
		}
	}
}
