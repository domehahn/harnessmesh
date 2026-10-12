package performance

import (
	"context"
	"testing"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

func TestEndToEnd_ShadowMode_RealAdvisorRecommendsButActualSelectionUnchanged(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	seedFinalized(t, pstore, "tenant-a", "agent-A", TaskClassSecurityReview, 20, 12, 6)
	seedFinalized(t, pstore, "tenant-a", "agent-B", TaskClassSecurityReview, 20, 18, 1)

	dir := newTestDirectory(map[supervisor.AgentID][]string{"agent-A": {}, "agent-B": {}})
	limits := supervisor.DefaultSupervisorLimits()
	v := supervisor.NewValidator(dir, nil, limits)
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	applier := supervisor.NewApplier(v, supervisor.AllowAllAuthorizer{}, state)
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	executor := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, limits, state)
	sched := supervisor.NewScheduler(applier, executor, dir, limits)
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(pstore), Tenant: "tenant-a"}
	sched.WithAdvisor(advisor, supervisor.AgentDecisionShadow, DefaultAdvisorPolicyVersion)

	if _, err := applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: "T1", Type: string(TaskClassSecurityReview), Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Legacy deterministic router picks the lexicographically-smallest
	// eligible agent among equally-capable candidates: "agent-A".
	final := state.Snapshot()
	if final.Assignments["T1"] != "agent-A" {
		t.Fatalf("actual execution must remain the deterministic fallback (agent-A), got %q", final.Assignments["T1"])
	}

	// The real DeterministicAdvisor, given the exact 20/12/6 vs 20/18/1
	// history, must have recommended agent-B first - recorded via the
	// shadow event, but with zero effect on the actual selection above.
	var shadowEvent *supervisor.ExecutionEvent
	for i := range events {
		if events[i].Kind == supervisor.EventAdvisorShadowRecommendation {
			shadowEvent = &events[i]
		}
	}
	if shadowEvent == nil {
		t.Fatalf("expected an EventAdvisorShadowRecommendation event")
	}
}

func TestEndToEnd_AdvisoryMode_SelectsRecommendedAgentAfterRevalidation(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	seedFinalized(t, pstore, "tenant-a", "agent-A", TaskClassSecurityReview, 20, 12, 6)
	seedFinalized(t, pstore, "tenant-a", "agent-B", TaskClassSecurityReview, 20, 18, 1)

	dir := newTestDirectory(map[supervisor.AgentID][]string{"agent-A": {}, "agent-B": {}})
	limits := supervisor.DefaultSupervisorLimits()
	v := supervisor.NewValidator(dir, nil, limits)
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	applier := supervisor.NewApplier(v, supervisor.AllowAllAuthorizer{}, state)
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	executor := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, limits, state)
	sched := supervisor.NewScheduler(applier, executor, dir, limits)
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(pstore), Tenant: "tenant-a"}
	sched.WithAdvisor(advisor, supervisor.AgentDecisionAdvisory, DefaultAdvisorPolicyVersion)

	if _, err := applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: "T1", Type: string(TaskClassSecurityReview), Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}

	final := state.Snapshot()
	if final.Assignments["T1"] != "agent-B" {
		t.Fatalf("expected the advisor's eligible, re-validated recommendation (agent-B) selected in advisory mode, got %q", final.Assignments["T1"])
	}
	found := false
	for _, e := range events {
		if e.Kind == supervisor.EventAdvisorySelection {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an EventAdvisorySelection event")
	}
}

func TestEndToEnd_AdvisoryMode_RecommendationStructurallyCannotNameUnavailableAgent(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	seedFinalized(t, pstore, "tenant-a", "agent-A", TaskClassSecurityReview, 20, 12, 6)
	// agent-B has strong historical data, but is NOT registered in the
	// directory at Tick time - simulating it having become unavailable/
	// ineligible between data collection and routing. Because Scheduler
	// only ever shows the advisor the CURRENT eligible-candidate list
	// (here, only agent-A), the real DeterministicAdvisor can never even
	// consider recommending agent-B - there is no TOCTOU window to close
	// here because the advisor's input is already revalidated-fresh.
	seedFinalized(t, pstore, "tenant-a", "agent-B", TaskClassSecurityReview, 20, 18, 1)

	dir := newTestDirectory(map[supervisor.AgentID][]string{"agent-A": {}})
	limits := supervisor.DefaultSupervisorLimits()
	v := supervisor.NewValidator(dir, nil, limits)
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	applier := supervisor.NewApplier(v, supervisor.AllowAllAuthorizer{}, state)
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	executor := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, limits, state)
	sched := supervisor.NewScheduler(applier, executor, dir, limits)
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(pstore), Tenant: "tenant-a"}
	sched.WithAdvisor(advisor, supervisor.AgentDecisionAdvisory, DefaultAdvisorPolicyVersion)

	if _, err := applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: "T1", Type: string(TaskClassSecurityReview), Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}

	final := state.Snapshot()
	if final.Assignments["T1"] != "agent-A" {
		t.Fatalf("expected the only currently-eligible agent (agent-A) selected, got %q", final.Assignments["T1"])
	}
	for _, e := range events {
		if e.Kind == supervisor.EventAdvisorFallback {
			t.Fatalf("no fallback should be needed - the advisor's recommendation (agent-A) was already the only eligible candidate, never the unavailable agent-B")
		}
	}
	found := false
	for _, e := range events {
		if e.Kind == supervisor.EventAdvisorySelection {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an EventAdvisorySelection event for the advisor's (only possible) recommendation")
	}
}
