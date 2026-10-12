package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestIntegration_LLMSupervisor_GoalToSynthesisWithRemediation is the
// Phase 2B equivalent of TestIntegration_GoalToSynthesisWithRemediation:
// the exact same flow, but the proposal source is now an LLMSupervisor
// driven by a deterministic fake ModelRouter/InferenceTransport instead
// of FakeSupervisor. No network access occurs. The point of this test is
// that swapping FakeSupervisor for LLMSupervisor changes nothing about
// Core's trust or authorization model: Validator, Authorizer, Applier,
// and Executor are exactly the Phase 1/2A types, unmodified.
func TestIntegration_LLMSupervisor_GoalToSynthesisWithRemediation(t *testing.T) {
	ctx := context.Background()

	dir := newFakeDirectory(map[AgentID][]string{
		"codex":  {"review"},
		"claude": {"write_repository"},
	})
	limits := DefaultSupervisorLimits()
	v := NewValidator(dir, nil, limits)
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)

	router := &FakeModelRouter{RouteFunc: func(ctx context.Context, req ModelRoutingRequest) (ModelRoutingDecision, error) {
		return ModelRoutingDecision{Provider: "switchyard", Model: "supervisor-" + req.Stage, RouteID: "supervisor-" + req.Stage}, nil
	}}

	transport := &FakeInferenceTransport{CompleteFunc: func(ctx context.Context, decision ModelRoutingDecision, system, user string, maxResponseBytes int) (InferenceResult, error) {
		// A deterministic fake model: inspect the stage markers that
		// buildPlanPrompt/Synthesize embed in the user prompt (never free-
		// form parsing of natural language) and return the fixed,
		// schema-valid response for that stage. This stands in for a real
		// LLM without requiring one.
		var content string
		switch {
		case strings.Contains(user, "stage: plan"):
			content = mustJSON(t, WirePlanResponse{Proposals: []WireProposal{
				{Type: "create_task", Task: &WireTask{ID: "T1", Type: "security-review", RequiredCapabilities: []string{"review"}}, RationaleSummary: "decompose goal"},
				{Type: "assign_task", TaskID: "T1", AgentID: "codex", RequiredCapabilities: []string{"review"}, RationaleSummary: "codex can review"},
			}})
		case strings.Contains(user, "stage: replan"):
			content = mustJSON(t, WirePlanResponse{Proposals: []WireProposal{
				{Type: "replan", NewTasks: []WireTask{{ID: "T2", Type: "remediation", RequiredCapabilities: []string{"write_repository"}, Dependencies: []string{"T1"}}}, RationaleSummary: "remediate T1 failure"},
			}})
		case strings.Contains(user, "stage: synthesize"):
			content = mustJSON(t, WireSynthesisResponse{TaskIDs: []string{"T1", "T2"}, Summary: "T1 failed, T2 remediated it", RationaleSummary: "cross-task synthesis"})
		default:
			t.Fatalf("unrecognized prompt stage: %q", user)
		}
		return InferenceResult{Content: content, ServedModel: decision.Model, ServedProvider: decision.Provider}, nil
	}}

	sv, err := NewLLMSupervisor(LLMSupervisorConfig{Router: router, Transport: transport, Limits: limits})
	if err != nil {
		t.Fatalf("NewLLMSupervisor: %v", err)
	}

	// --- Plan ---
	proposals, err := sv.Plan(ctx, Goal{ID: "G1", Description: "review this release"}, state.Snapshot())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := applier.Apply(ctx, proposals); err != nil {
		t.Fatalf("Apply(plan): %v", err)
	}

	// --- Execute: T1 fails ---
	exec := NewExecutor(dir, AllowAllAuthorizer{}, &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		if task.ID == "T1" {
			return ExecutionResult{}, errors.New("review found a blocking issue")
		}
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}, limits, state)

	_, err = exec.Execute(ctx, "T1")
	var execErr *AgentExecutionFailure
	if !errors.As(err, &execErr) {
		t.Fatalf("expected T1 to fail, got %v (%T)", err, err)
	}

	// --- Replan ---
	afterFail := state.Snapshot()
	replanProposals, err := sv.Replan(ctx, afterFail, ExecutionEvent{Kind: EventAgentFailed, TaskID: "T1"})
	if err != nil {
		t.Fatalf("Replan: %v", err)
	}
	if _, err := applier.Apply(ctx, replanProposals); err != nil {
		t.Fatalf("Apply(replan): %v", err)
	}
	if _, err := applier.Apply(ctx, []AgentProposal{
		AssignTaskProposal{TaskID: "T2", CandidateAgentID: "claude", RequiredCaps: []string{"write_repository"}, RationaleSummary: "claude can write"},
	}); err != nil {
		t.Fatalf("Apply(assign remediation): %v", err)
	}

	// --- Re-execute: T2 succeeds ---
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
	if synth.Summary != "T1 failed, T2 remediated it" {
		t.Fatalf("unexpected synthesis: %+v", synth)
	}

	t1, _ := finalState.Tasks.ByID("T1")
	t2, _ := finalState.Tasks.ByID("T2")
	if t1.Status != TaskStatusFailed || t2.Status != TaskStatusCompleted {
		t.Fatalf("unexpected final statuses: T1=%q T2=%q", t1.Status, t2.Status)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
