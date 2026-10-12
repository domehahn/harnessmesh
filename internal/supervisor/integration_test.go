package supervisor

import (
	"context"
	"errors"
	"testing"
)

// TestIntegration_GoalToSynthesisWithRemediation is the end-to-end flow
// required by Phase 2A:
//
//	Goal -> FakeSupervisor.Plan -> validation -> application -> TaskGraph
//	-> fake agent execution -> execution result -> FakeSupervisor.Replan
//	-> remediation task -> re-execution -> FakeSupervisor.Synthesize
//	-> final state
//
// It runs entirely without a real LLM or model provider: FakeSupervisor
// and FakeExecutionProvider are deterministic, injected test doubles.
func TestIntegration_GoalToSynthesisWithRemediation(t *testing.T) {
	ctx := context.Background()

	dir := newFakeDirectory(map[AgentID][]string{
		"codex":  {"review"},
		"claude": {"write_repository"},
	})
	limits := DefaultSupervisorLimits()
	v := NewValidator(dir, nil, limits)
	state := NewCoreState(TaskGraph{}, nil)
	authz := AllowAllAuthorizer{}
	applier := NewApplier(v, authz, state)

	// --- Goal -> Plan ---
	sv := &FakeSupervisor{
		PlanFunc: func(ctx context.Context, goal Goal, s ExecutionState) ([]AgentProposal, error) {
			return []AgentProposal{
				CreateTaskProposal{Task: Task{ID: "T1", Type: "security-review", RequiredCapabilities: []string{"review"}}, RationaleSummary: "decompose goal into a security review"},
				AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RequiredCaps: []string{"review"}, RationaleSummary: "codex has review capability"},
			}, nil
		},
		ReplanFunc: func(ctx context.Context, s ExecutionState, event ExecutionEvent) ([]AgentProposal, error) {
			if event.Kind != EventAgentFailed {
				t.Fatalf("unexpected replan trigger: %v", event.Kind)
			}
			return []AgentProposal{
				ReplanProposal{
					NewTasks:         []Task{{ID: "T2", Type: "remediation", RequiredCapabilities: []string{"write_repository"}, Dependencies: []TaskID{event.TaskID}}},
					RationaleSummary: "T1 failed; propose a remediation task",
				},
			}, nil
		},
		SynthesizeFunc: func(ctx context.Context, s ExecutionState) (Synthesis, error) {
			t1, _ := s.Tasks.ByID("T1")
			t2, _ := s.Tasks.ByID("T2")
			return Synthesis{
				TaskIDs:          []TaskID{"T1", "T2"},
				Summary:          "T1 " + string(t1.Status) + ", remediated by T2 " + string(t2.Status),
				RationaleSummary: "cross-task synthesis after remediation",
			}, nil
		},
	}

	proposals, err := sv.Plan(ctx, Goal{ID: "G1", Description: "review this release"}, state.Snapshot())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// --- validation + application (Core, not Supervisor) ---
	if _, err := applier.Apply(ctx, proposals); err != nil {
		t.Fatalf("Apply(plan proposals): %v", err)
	}

	// --- fake agent execution: T1 fails ---
	exec := NewExecutor(dir, AllowAllAuthorizer{}, &FakeExecutionProvider{
		ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
			if task.ID == "T1" {
				return ExecutionResult{}, errors.New("review found a blocking issue")
			}
			return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted, Summary: "fix applied"}, nil
		},
	}, limits, state)

	_, err = exec.Execute(ctx, "T1")
	var execErr *AgentExecutionFailure
	if !errors.As(err, &execErr) {
		t.Fatalf("expected T1 to fail with AgentExecutionFailure, got %v (%T)", err, err)
	}
	afterFail := state.Snapshot()
	t1, _ := afterFail.Tasks.ByID("T1")
	if t1.Status != TaskStatusFailed {
		t.Fatalf("expected T1 status failed, got %q", t1.Status)
	}

	// --- Replan in response to the failure ---
	replanProposals, err := sv.Replan(ctx, afterFail, ExecutionEvent{Kind: EventAgentFailed, TaskID: "T1"})
	if err != nil {
		t.Fatalf("Replan: %v", err)
	}
	if _, err := applier.Apply(ctx, replanProposals); err != nil {
		t.Fatalf("Apply(replan proposals): %v", err)
	}
	if _, err := applier.Apply(ctx, []AgentProposal{
		AssignTaskProposal{TaskID: "T2", CandidateAgentID: "claude", RequiredCaps: []string{"write_repository"}, RationaleSummary: "claude has write_repository"},
	}); err != nil {
		t.Fatalf("Apply(assign remediation): %v", err)
	}

	afterReplan := state.Snapshot()
	if afterReplan.ReplanCount != 1 {
		t.Fatalf("expected ReplanCount=1 after one replan, got %d", afterReplan.ReplanCount)
	}

	// --- re-execution: T2 succeeds ---
	result, err := exec.Execute(ctx, "T2")
	if err != nil {
		t.Fatalf("T2 execution: %v", err)
	}
	if result.Status != TaskStatusCompleted {
		t.Fatalf("expected T2 completed, got %q", result.Status)
	}

	// --- Synthesize ---
	finalState := state.Snapshot()
	synth, err := sv.Synthesize(ctx, finalState)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	wantSummary := "T1 failed, remediated by T2 completed"
	if synth.Summary != wantSummary {
		t.Fatalf("unexpected synthesis summary: got %q want %q", synth.Summary, wantSummary)
	}

	t1Final, _ := finalState.Tasks.ByID("T1")
	t2Final, _ := finalState.Tasks.ByID("T2")
	if t1Final.Status != TaskStatusFailed || t2Final.Status != TaskStatusCompleted {
		t.Fatalf("unexpected final task statuses: T1=%q T2=%q", t1Final.Status, t2Final.Status)
	}
}

// TestIntegration_ReplanLimitExhaustion proves requirement 10: repeated
// Replan proposals are rejected once MaxReplanCount is reached, ending the
// loop deterministically rather than allowing unbounded replanning.
func TestIntegration_ReplanLimitExhaustion(t *testing.T) {
	ctx := context.Background()
	limits := DefaultSupervisorLimits()
	limits.MaxReplanCount = 2

	dir := newFakeDirectory(map[AgentID][]string{})
	v := NewValidator(dir, nil, limits)
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)

	for i := 0; i < limits.MaxReplanCount; i++ {
		if _, err := applier.Apply(ctx, []AgentProposal{
			ReplanProposal{RationaleSummary: "replan iteration"},
		}); err != nil {
			t.Fatalf("replan %d should succeed under the limit: %v", i, err)
		}
	}

	_, err := applier.Apply(ctx, []AgentProposal{
		ReplanProposal{RationaleSummary: "one too many"},
	})
	var limitErr *ReplanLimitExceeded
	if !errors.As(err, &limitErr) {
		t.Fatalf("expected ReplanLimitExceeded once the limit is exhausted, got %v (%T)", err, err)
	}

	after := state.Snapshot()
	if after.ReplanCount != limits.MaxReplanCount {
		t.Fatalf("replan count must not advance past the limit; got %d want %d", after.ReplanCount, limits.MaxReplanCount)
	}
}
