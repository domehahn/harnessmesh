package supervisor

import (
	"context"
	"testing"
)

type fakeExperimentAdmission struct {
	AdmitFunc func(ctx context.Context, task Task, decisionID DecisionID, candidates []AgentCandidate, champion AgentID, policy PolicyProvenance) (AgentID, bool, error)
	calls     int
}

func (f *fakeExperimentAdmission) Admit(ctx context.Context, task Task, decisionID DecisionID, candidates []AgentCandidate, champion AgentID, policy PolicyProvenance) (AgentID, bool, error) {
	f.calls++
	return f.AdmitFunc(ctx, task, decisionID, candidates, champion, policy)
}

// TestExperimentAdmission_AdmittedChallengerActuallyDispatches is the
// critical regression test for ADR 0011 Phase 10's central authority
// change: a prior version of selectAgent computed the admitted
// Challenger but never propagated it to its own return value, meaning
// admission had ZERO effect on production routing. This test fails
// against that defect and passes against the fix.
func TestExperimentAdmission_AdmittedChallengerActuallyDispatches(t *testing.T) {
	sched, state := newTestScheduler(t, map[AgentID][]string{"alpha": {}, "beta": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	admission := &fakeExperimentAdmission{AdmitFunc: func(ctx context.Context, task Task, decisionID DecisionID, candidates []AgentCandidate, champion AgentID, policy PolicyProvenance) (AgentID, bool, error) {
		return "beta", true, nil // admit the challenger "beta" instead of the champion
	}}
	sched.WithExperimentAdmission(admission)

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
		t.Fatalf("expected the admitted challenger (beta) to actually be dispatched, got %q - admission must affect the real selection, not just be computed and discarded", final.Assignments["T1"])
	}
	found := false
	for _, e := range events {
		if e.Kind == EventChallengerAdmitted {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an EventChallengerAdmitted event recording the override")
	}
	if admission.calls != 1 {
		t.Fatalf("expected the admission gate to be consulted exactly once, got %d", admission.calls)
	}
}

func TestExperimentAdmission_NotAdmittedKeepsChampion(t *testing.T) {
	sched, state := newTestScheduler(t, map[AgentID][]string{"alpha": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	admission := &fakeExperimentAdmission{AdmitFunc: func(ctx context.Context, task Task, decisionID DecisionID, candidates []AgentCandidate, champion AgentID, policy PolicyProvenance) (AgentID, bool, error) {
		return "", false, nil
	}}
	sched.WithExperimentAdmission(admission)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	final := state.Snapshot()
	if final.Assignments["T1"] != "alpha" {
		t.Fatalf("expected the champion (alpha) when not admitted, got %q", final.Assignments["T1"])
	}
}

func TestNegative_ExperimentAdmissionCannotNameIneligibleAgent(t *testing.T) {
	sched, state := newTestScheduler(t, map[AgentID][]string{"alpha": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	admission := &fakeExperimentAdmission{AdmitFunc: func(ctx context.Context, task Task, decisionID DecisionID, candidates []AgentCandidate, champion AgentID, policy PolicyProvenance) (AgentID, bool, error) {
		return "not-a-real-eligible-agent", true, nil
	}}
	sched.WithExperimentAdmission(admission)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	final := state.Snapshot()
	if final.Assignments["T1"] != "alpha" {
		t.Fatalf("expected the TOCTOU re-check to reject an ineligible admission and fall back to champion, got %q", final.Assignments["T1"])
	}
}

type panickingExperimentAdmission struct{ calls int }

func (p *panickingExperimentAdmission) Admit(ctx context.Context, task Task, decisionID DecisionID, candidates []AgentCandidate, champion AgentID, policy PolicyProvenance) (AgentID, bool, error) {
	p.calls++
	panic("experiment admission boom")
}

func TestNegative_ExperimentAdmissionPanicFallsBackToChampion(t *testing.T) {
	sched, state := newTestScheduler(t, map[AgentID][]string{"alpha": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	admission := &panickingExperimentAdmission{}
	sched.WithExperimentAdmission(admission)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick must succeed despite a panicking admission gate: %v", err)
	}
	final := state.Snapshot()
	if final.Assignments["T1"] != "alpha" {
		t.Fatalf("expected champion fallback after a panicking admission gate, got %q", final.Assignments["T1"])
	}
	if admission.calls != 1 {
		t.Fatalf("expected the admission gate to have been called exactly once, got %d", admission.calls)
	}
}
