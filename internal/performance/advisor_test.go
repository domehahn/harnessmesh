package performance

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

func seedFinalized(t *testing.T, store PerformanceStore, tenant string, agent supervisor.AgentID, taskClass TaskClass, n, verified, rejected int) {
	t.Helper()
	ctx := context.Background()
	k := 0
	for i := 0; i < verified; i++ {
		obs := mustObs(t, tenant, agent, taskClass, k)
		obs.Status = ObservationFinalized
		obs.QualityOutcome = QualityVerified
		store.Save(ctx, obs)
		k++
	}
	for i := 0; i < rejected; i++ {
		obs := mustObs(t, tenant, agent, taskClass, k)
		obs.Status = ObservationFinalized
		obs.QualityOutcome = QualityRejected
		store.Save(ctx, obs)
		k++
	}
	for k < n {
		obs := mustObs(t, tenant, agent, taskClass, k)
		obs.Status = ObservationFinalized
		obs.QualityOutcome = QualityVerified
		store.Save(ctx, obs)
		k++
	}
}

func mustObs(t *testing.T, tenant string, agent supervisor.AgentID, taskClass TaskClass, k int) PerformanceObservation {
	t.Helper()
	in := NewObservationInput{
		Tenant: tenant, TaskID: supervisor.TaskID(string(agent) + "-" + taskIDSuffix(k)), ExecutionID: string(agent) + "-" + taskIDSuffix(k) + "#1",
		TaskClass: taskClass, AgentID: agent, ExecutionOutcome: ExecutionCompleted,
		StartedAt: time.Now(), FinishedAt: time.Now().Add(time.Second),
		Metrics: PerformanceMetrics{Duration: time.Second},
	}
	obs, err := NewObservation(in)
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	return obs
}

func taskIDSuffix(k int) string {
	return string(rune('A' + k%26))
}

func TestAdvisor_ShadowScenario_RanksHistoricallyStrongerAgentHigher(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedFinalized(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, 20, 12, 6)
	seedFinalized(t, store, "tenant-a", "agent-B", TaskClassSecurityReview, 20, 18, 1)

	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	advice, err := advisor.Advise(context.Background(), supervisor.Task{Type: string(TaskClassSecurityReview)}, []supervisor.AgentCandidate{
		{AgentID: "agent-A"}, {AgentID: "agent-B"},
	})
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	if len(advice.Ranked) != 2 {
		t.Fatalf("expected 2 ranked candidates, got %d", len(advice.Ranked))
	}
	if advice.Ranked[0].AgentID != "agent-B" {
		t.Fatalf("expected agent-B (18 verified / 1 rejected) ranked first, got %q", advice.Ranked[0].AgentID)
	}
}

func TestAdvisor_ColdStart_AllInsufficientFallsBack(t *testing.T) {
	store := NewMemoryPerformanceStore()
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	advice, err := advisor.Advise(context.Background(), supervisor.Task{Type: string(TaskClassSecurityReview)}, []supervisor.AgentCandidate{
		{AgentID: "new-agent-1"}, {AgentID: "new-agent-2"},
	})
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	if len(advice.Ranked) != 0 {
		t.Fatalf("expected no ranking when all candidates are cold-start, got %+v", advice.Ranked)
	}
	if advice.Basis != "all_cold_start_fallback" {
		t.Fatalf("expected the all_cold_start_fallback basis, got %q", advice.Basis)
	}
}

func TestAdvisor_ColdStart_MixedNotPenalized(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedFinalized(t, store, "tenant-a", "established-agent", TaskClassSecurityReview, 15, 14, 1)

	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	advice, err := advisor.Advise(context.Background(), supervisor.Task{Type: string(TaskClassSecurityReview)}, []supervisor.AgentCandidate{
		{AgentID: "established-agent"}, {AgentID: "new-agent"},
	})
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	if len(advice.Ranked) != 2 {
		t.Fatalf("expected both candidates present, got %d", len(advice.Ranked))
	}
	if advice.Ranked[0].AgentID != "established-agent" {
		t.Fatalf("expected the established agent ranked first, got %q", advice.Ranked[0].AgentID)
	}
	for _, r := range advice.Ranked {
		if r.AgentID == "new-agent" && r.Confidence != supervisor.ConfidenceInsufficient {
			t.Fatalf("expected the cold-start agent marked insufficient (unknown), not scored, got %q", r.Confidence)
		}
	}
}

func TestNegative_InsufficientSampleSizeMarkedLowNotFabricatedHigh(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedFinalized(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, DefaultMinimumObservationCount+1, 1, 0) // sufficient count, but only 1 verified
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	advice, err := advisor.Advise(context.Background(), supervisor.Task{Type: string(TaskClassSecurityReview)}, []supervisor.AgentCandidate{{AgentID: "agent-A"}})
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	if len(advice.Ranked) != 1 {
		t.Fatalf("expected 1 ranked candidate, got %d", len(advice.Ranked))
	}
	if advice.Ranked[0].Confidence == supervisor.ConfidenceStrong {
		t.Fatalf("a small, barely-finalized sample must never be reported as strong confidence")
	}
}

func TestNegative_CrossTenantAdviceRejected(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedFinalized(t, store, "tenant-b", "agent-A", TaskClassSecurityReview, 20, 18, 1)
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"} // advisor scoped to tenant-a
	advice, err := advisor.Advise(context.Background(), supervisor.Task{Type: string(TaskClassSecurityReview), Tenant: "tenant-b"}, []supervisor.AgentCandidate{{AgentID: "agent-A"}})
	if err == nil {
		t.Fatalf("expected an error for a task tenant that does not match the advisor's configured tenant scope")
	}
	_ = advice
}

func TestNegative_CrossTenantHistoryNotLeakedIntoAdvice(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedFinalized(t, store, "tenant-b", "agent-A", TaskClassSecurityReview, 20, 18, 1) // strong history, but in tenant-b
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	advice, err := advisor.Advise(context.Background(), supervisor.Task{Type: string(TaskClassSecurityReview)}, []supervisor.AgentCandidate{{AgentID: "agent-A"}})
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	// agent-A has NO tenant-a history, so it must be treated as cold-start,
	// never silently credited with tenant-b's strong history.
	if advice.Basis != "all_cold_start_fallback" {
		t.Fatalf("expected cold-start fallback (no tenant-a data for agent-A), got basis %q, ranked %+v", advice.Basis, advice.Ranked)
	}
}

func TestNegative_CrossTaskClassComparisonNeverHappens(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedFinalized(t, store, "tenant-a", "agent-A", TaskClassDocumentation, 20, 20, 0) // perfect at documentation
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	advice, err := advisor.Advise(context.Background(), supervisor.Task{Type: string(TaskClassSecurityReview)}, []supervisor.AgentCandidate{{AgentID: "agent-A"}})
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	// agent-A's documentation history must NOT count toward security_review.
	if advice.Basis != "all_cold_start_fallback" {
		t.Fatalf("expected cold-start fallback (documentation history must not count for security_review), got %+v", advice.Ranked)
	}
}

func TestNegative_UnknownTaskClassRejected(t *testing.T) {
	store := NewMemoryPerformanceStore()
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	_, err := advisor.Advise(context.Background(), supervisor.Task{Type: "made_up_class"}, []supervisor.AgentCandidate{{AgentID: "agent-A"}})
	if err == nil {
		t.Fatalf("expected an error for an unknown task class")
	}
}

func TestNegative_PolicyVersionDefaultsWhenMissing(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedFinalized(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, 20, 18, 1)
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"} // PolicyVersion left empty
	advice, err := advisor.Advise(context.Background(), supervisor.Task{Type: string(TaskClassSecurityReview)}, []supervisor.AgentCandidate{{AgentID: "agent-A"}})
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	if advice.Basis != DefaultAdvisorPolicyVersion {
		t.Fatalf("expected the default policy version to be used when none configured, got %q", advice.Basis)
	}
}

func TestNegative_AdvisorCannotSelectModelOrProvider(t *testing.T) {
	// Structural proof: AgentRecommendation/AgentAdvice have no model or
	// provider field at all - the advisor's output type makes "select a
	// model" impossible to express, not merely discouraged by convention.
	for _, typ := range []reflect.Type{
		reflect.TypeOf(supervisor.AgentRecommendation{}),
		reflect.TypeOf(supervisor.AgentAdvice{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			if strings.Contains(name, "model") || strings.Contains(name, "provider") || strings.Contains(name, "route") {
				t.Fatalf("%s.%s suggests the advisor output can express a model/provider/route choice - it must not", typ.Name(), typ.Field(i).Name)
			}
		}
	}
}

func TestNegative_EmptyCandidateListReturnsNoAdvice(t *testing.T) {
	store := NewMemoryPerformanceStore()
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	advice, err := advisor.Advise(context.Background(), supervisor.Task{Type: string(TaskClassSecurityReview)}, nil)
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	if len(advice.Ranked) != 0 {
		t.Fatalf("expected no ranking for an empty candidate list, got %+v", advice.Ranked)
	}
}

func TestNegative_RecommendationReasonsBounded(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedFinalized(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, 20, 18, 1)
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(store), Tenant: "tenant-a"}
	advice, _ := advisor.Advise(context.Background(), supervisor.Task{Type: string(TaskClassSecurityReview)}, []supervisor.AgentCandidate{{AgentID: "agent-A"}})
	for _, r := range advice.Ranked {
		for _, reason := range r.Reasons {
			if len(reason) > 200 {
				t.Fatalf("expected bounded reason strings, got %d chars: %q", len(reason), reason)
			}
		}
	}
}
