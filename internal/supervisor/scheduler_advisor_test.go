package supervisor

import (
	"context"
	"testing"
)

type fakeAdvisor struct {
	AdviseFunc func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error)
	calls      int
}

func (f *fakeAdvisor) Advise(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
	f.calls++
	if f.AdviseFunc == nil {
		return AgentAdvice{}, nil
	}
	return f.AdviseFunc(ctx, task, candidates)
}

func TestAdvisor_DisabledModeNeverInvokesAdvisor(t *testing.T) {
	sched, state := newTestScheduler(t, map[AgentID][]string{"alpha": {}, "beta": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	advisor := &fakeAdvisor{}
	sched.WithAdvisor(advisor, AgentDecisionDisabled, "performance-v1")

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if advisor.calls != 0 {
		t.Fatalf("expected the advisor to never be invoked in disabled mode, got %d calls", advisor.calls)
	}
	final := state.Snapshot()
	task, _ := final.Tasks.ByID("T1")
	if task.Status != TaskStatusCompleted {
		t.Fatalf("expected the task to still complete via the deterministic fallback, got %q", task.Status)
	}
}

func TestAdvisor_ShadowModeNeverAltersSelection(t *testing.T) {
	sched, state := newTestScheduler(t, map[AgentID][]string{"alpha": {}, "beta": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	advisor := &fakeAdvisor{AdviseFunc: func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
		return AgentAdvice{Ranked: []AgentRecommendation{{AgentID: "beta", Confidence: ConfidenceStrong, Reasons: []string{"strong history"}}}, Basis: "performance-v1"}, nil
	}}
	sched.WithAdvisor(advisor, AgentDecisionShadow, "performance-v1")

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if advisor.calls != 1 {
		t.Fatalf("expected the advisor to be called exactly once in shadow mode, got %d", advisor.calls)
	}

	final := state.Snapshot()
	if final.Assignments["T1"] != "alpha" {
		t.Fatalf("expected the deterministic fallback (alpha) to still be selected in shadow mode, got %q", final.Assignments["T1"])
	}

	foundShadowEvent := false
	for _, e := range events {
		if e.Kind == EventAdvisorShadowRecommendation {
			foundShadowEvent = true
		}
	}
	if !foundShadowEvent {
		t.Fatalf("expected a shadow-recommendation event to be recorded")
	}
}

func TestAdvisor_AdvisoryModeSelectsRecommendedAgentWhenEligible(t *testing.T) {
	sched, state := newTestScheduler(t, map[AgentID][]string{"alpha": {}, "beta": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	advisor := &fakeAdvisor{AdviseFunc: func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
		return AgentAdvice{Ranked: []AgentRecommendation{{AgentID: "beta", Confidence: ConfidenceStrong}}, Basis: "performance-v1"}, nil
	}}
	sched.WithAdvisor(advisor, AgentDecisionAdvisory, "performance-v1")

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}

	final := state.Snapshot()
	if final.Assignments["T1"] != "beta" {
		t.Fatalf("expected the advisor's eligible recommendation (beta) to be selected, got %q", final.Assignments["T1"])
	}
	found := false
	for _, e := range events {
		if e.Kind == EventAdvisorySelection {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an advisory-selection event")
	}
}

func TestAdvisor_AdvisoryModeFallsBackWhenRecommendedAgentIneligible(t *testing.T) {
	// The advisor recommends an agent that is NOT in the eligible
	// candidate list at all (e.g. it became unavailable, or the advisor
	// is simply wrong/malicious) - Core must fall back deterministically,
	// never propose an ineligible agent.
	sched, state := newTestScheduler(t, map[AgentID][]string{"alpha": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	advisor := &fakeAdvisor{AdviseFunc: func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
		return AgentAdvice{Ranked: []AgentRecommendation{{AgentID: "nonexistent-agent", Confidence: ConfidenceStrong}}, Basis: "performance-v1"}, nil
	}}
	sched.WithAdvisor(advisor, AgentDecisionAdvisory, "performance-v1")

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}

	final := state.Snapshot()
	if final.Assignments["T1"] != "alpha" {
		t.Fatalf("expected a deterministic fallback to the only real eligible agent (alpha), got %q", final.Assignments["T1"])
	}
	found := false
	for _, e := range events {
		if e.Kind == EventAdvisorFallback {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an explicit advisor-fallback event")
	}
}

func TestAdvisor_FailureFallsBackDeterministically(t *testing.T) {
	sched, state := newTestScheduler(t, map[AgentID][]string{"alpha": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	advisor := &fakeAdvisor{AdviseFunc: func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
		return AgentAdvice{}, errUnavailable
	}}
	sched.WithAdvisor(advisor, AgentDecisionAdvisory, "performance-v1")

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	final := state.Snapshot()
	if final.Assignments["T1"] != "alpha" {
		t.Fatalf("expected fallback on advisor failure, got %q", final.Assignments["T1"])
	}
	found := false
	for _, e := range events {
		if e.Kind == EventAdvisorFailure {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an explicit advisor-failure event, not silent fallback")
	}
}

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

const errUnavailable = simpleErr("advisor unavailable")
