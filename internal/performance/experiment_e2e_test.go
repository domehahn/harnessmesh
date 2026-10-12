package performance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// newExperimentProductionSystem wires the real Scheduler/Executor chain
// with BOTH the shadow evaluation path (ADR 0009) AND the durable
// experiment admission gate (ADR 0011) through the SAME
// ProductionEvaluationCoordinator - "one authoritative path."
func newExperimentProductionSystem(t *testing.T, agents map[supervisor.AgentID][]string, expStore ExperimentStore, experimentID ExperimentID) (*supervisor.Scheduler, *supervisor.CoreState, *ProductionEvaluationCoordinator, EvaluationStore, CandidateSnapshotStore) {
	t.Helper()
	dir := newTestDirectory(agents)
	limits := supervisor.DefaultSupervisorLimits()
	v := supervisor.NewValidator(dir, nil, limits)
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	applier := supervisor.NewApplier(v, supervisor.AllowAllAuthorizer{}, state)
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}

	pstore := NewMemoryPerformanceStore()
	evalStore := NewMemoryEvaluationStore()
	snapStore := NewMemoryCandidateSnapshotStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	execObserver := NewSupervisorExecutionObserver(pstore, nil)
	reviewHook := NewReviewFinalizationHook(pstore, nil, nil)
	challenger := NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: NewStoreDataset(pstore), Tenant: "tenant-a"}, challengerDesc())
	coordinator := NewProductionEvaluationCoordinator(execObserver, reviewHook, snapStore, evaluator, []RoutingPolicy{challenger}, DefaultEvaluationLimits(), nil)

	controller := &ExperimentController{Store: expStore, SnapshotStore: snapStore, EvaluationStore: evalStore, ExperimentID: experimentID, Limits: DefaultEvaluationLimits()}
	coordinator.WithExperiments(controller)

	executor := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, limits, state).WithObserver(coordinator)
	sched := supervisor.NewScheduler(applier, executor, dir, limits).
		WithEvaluation(coordinator, supervisor.EvaluationShadow).
		WithExperimentAdmission(coordinator)

	return sched, state, coordinator, evalStore, snapStore
}

func dispatchOneTask(t *testing.T, sched *supervisor.Scheduler, taskID supervisor.TaskID) {
	t.Helper()
	if _, err := sched.Applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: taskID, Type: "security_review", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create %s: %v", taskID, err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick %s: %v", taskID, err)
	}
}

// TestE2E_BudgetedCanaryExecution is ADR 0011 Part 42's exact scenario:
// a 100%-allocation experiment admits its Challenger, which then
// ACTUALLY executes through the real Executor, gets a real
// Core-owned ExecutionID, and is distinguishable from a Champion-only
// decision in the evaluation record.
func TestE2E_BudgetedCanaryExecution(t *testing.T) {
	expStore := NewMemoryExperimentStore()
	exp := Experiment{
		Definition: ExperimentDefinition{
			ID: "exp-canary", Tenant: "tenant-a", TaskClass: "security_review",
			ChallengerPolicy:  NewRoutingPolicyDescriptor("performance", "v2", "s1", nil),
			ChallengerAgentID: "agent-B",
			MaxExecutions:     5, MaxConcurrent: 1, AllocationPercent: 100, AllocationSemantics: AllocationSemanticsV1,
		},
		State: ExperimentEnabled, UpdatedAt: time.Now().UTC(),
	}
	if err := expStore.Create(context.Background(), exp); err != nil {
		t.Fatalf("Create: %v", err)
	}

	sched, state, _, evalStore, _ := newExperimentProductionSystem(t, map[supervisor.AgentID][]string{"agent-A": {}, "agent-B": {}}, expStore, exp.Definition.ID)
	dispatchOneTask(t, sched, "T1")

	final := state.Snapshot()
	if final.Assignments["T1"] != "agent-B" {
		t.Fatalf("expected the admitted challenger (agent-B) to actually be the selected AND executed agent, got %q", final.Assignments["T1"])
	}
	task1, _ := final.Tasks.ByID("T1")
	if task1.Status != supervisor.TaskStatusCompleted {
		t.Fatalf("expected the admitted challenger's execution to actually complete, got %q", task1.Status)
	}

	admission, err := expStore.GetAdmission(context.Background(), exp.Definition.ID, mustFindDecisionID(t, evalStore, "tenant-a"))
	if err != nil {
		t.Fatalf("GetAdmission: %v", err)
	}
	if !admission.IsChallenger || admission.Selected != "agent-B" {
		t.Fatalf("expected a durable admission record naming agent-B as the executed selection, got %+v", admission)
	}

	// Budget must show exactly 1 consumed (the execution genuinely
	// completed, converting the reservation to a consumption).
	finalExp, err := expStore.Get(context.Background(), exp.Definition.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if finalExp.Consumed != 1 {
		t.Fatalf("expected 1 consumed execution, got %d", finalExp.Consumed)
	}
	if finalExp.Reserved-finalExp.Consumed != 0 {
		t.Fatalf("expected the concurrency slot to be released after completion, got active=%d", finalExp.Reserved-finalExp.Consumed)
	}
}

func mustFindDecisionID(t *testing.T, evalStore EvaluationStore, tenant string) supervisor.DecisionID {
	t.Helper()
	all, err := evalStore.Query(context.Background(), EvaluationFilter{Tenant: tenant})
	if err != nil || len(all) == 0 {
		t.Fatalf("expected at least one evaluation record to locate the DecisionID, got %d err=%v", len(all), err)
	}
	return all[0].DecisionID
}

// TestE2E_BudgetExhaustionFallsBackToChampion is ADR 0011 Part 43's
// exact scenario: MaxExecutions=2, three decisions, the third must fall
// back to Champion once the budget is exhausted.
func TestE2E_BudgetExhaustionFallsBackToChampion(t *testing.T) {
	expStore := NewMemoryExperimentStore()
	exp := Experiment{
		Definition: ExperimentDefinition{
			ID: "exp-exhaust", Tenant: "tenant-a", TaskClass: "security_review",
			ChallengerPolicy:  NewRoutingPolicyDescriptor("performance", "v2", "s1", nil),
			ChallengerAgentID: "agent-B",
			MaxExecutions:     2, MaxConcurrent: 2, AllocationPercent: 100, AllocationSemantics: AllocationSemanticsV1,
		},
		State: ExperimentEnabled, UpdatedAt: time.Now().UTC(),
	}
	if err := expStore.Create(context.Background(), exp); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sched, state, _, _, _ := newExperimentProductionSystem(t, map[supervisor.AgentID][]string{"agent-A": {}, "agent-B": {}}, expStore, exp.Definition.ID)

	dispatchOneTask(t, sched, "T1")
	dispatchOneTask(t, sched, "T2")
	dispatchOneTask(t, sched, "T3")

	final := state.Snapshot()
	if final.Assignments["T1"] != "agent-B" || final.Assignments["T2"] != "agent-B" {
		t.Fatalf("expected the first 2 decisions admitted to the challenger, got T1=%q T2=%q", final.Assignments["T1"], final.Assignments["T2"])
	}
	if final.Assignments["T3"] != "agent-A" {
		t.Fatalf("expected the 3rd decision to fall back to champion (agent-A) once budget is exhausted, got %q", final.Assignments["T3"])
	}
}

// TestE2E_ConcurrentBudgetNeverOversubscribed is ADR 0011 Part 44: 20
// simultaneous eligible decisions, MaxExecutions=1 - exactly 1 admitted,
// under -race.
func TestE2E_ConcurrentBudgetNeverOversubscribed(t *testing.T) {
	expStore := NewMemoryExperimentStore()
	exp := Experiment{
		Definition: ExperimentDefinition{
			ID: "exp-concurrent", Tenant: "tenant-a", TaskClass: "security_review",
			ChallengerPolicy:  NewRoutingPolicyDescriptor("performance", "v2", "s1", nil),
			ChallengerAgentID: "agent-B",
			MaxExecutions:     1, MaxConcurrent: 1, AllocationPercent: 100, AllocationSemantics: AllocationSemanticsV1,
		},
		State: ExperimentEnabled, UpdatedAt: time.Now().UTC(),
	}
	if err := expStore.Create(context.Background(), exp); err != nil {
		t.Fatalf("Create: %v", err)
	}
	c := NewExperimentController(expStore)

	const n = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	admittedCount := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			decisionID := supervisor.DecisionID("d" + string(rune('a'+i)))
			snap := CandidateSnapshot{DecisionID: decisionID, Tenant: "tenant-a", TaskID: supervisor.TaskID("T" + string(rune('a'+i))), AgentIDs: []string{"agent-A", "agent-B"}}
			r := AdmissionRequest{
				ExperimentID: exp.Definition.ID, DecisionID: decisionID, Tenant: "tenant-a",
				Task: supervisor.Task{Tenant: "tenant-a", Type: "security_review"}, Snapshot: snap,
				ChampionAgentID: "agent-A", ChallengerAgentID: "agent-B", ChallengerPolicy: exp.Definition.ChallengerPolicy,
				Now: time.Now().UTC(),
			}
			a, err := c.Admit(context.Background(), r)
			if err != nil {
				t.Errorf("Admit: %v", err)
				return
			}
			if a.IsChallenger {
				mu.Lock()
				admittedCount++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if admittedCount != 1 {
		t.Fatalf("expected exactly 1 admitted challenger out of %d concurrent decisions, got %d", n, admittedCount)
	}
}

// TestE2E_KillSwitchBlocksFutureAdmissions is ADR 0011 Part 45.
func TestE2E_KillSwitchBlocksFutureAdmissions(t *testing.T) {
	expStore := NewMemoryExperimentStore()
	exp := Experiment{
		Definition: ExperimentDefinition{
			ID: "exp-kill", Tenant: "tenant-a", TaskClass: "security_review",
			ChallengerPolicy:  NewRoutingPolicyDescriptor("performance", "v2", "s1", nil),
			ChallengerAgentID: "agent-B",
			MaxExecutions:     10, MaxConcurrent: 10, AllocationPercent: 100, AllocationSemantics: AllocationSemanticsV1,
		},
		State: ExperimentEnabled, UpdatedAt: time.Now().UTC(),
	}
	if err := expStore.Create(context.Background(), exp); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sched, state, _, _, _ := newExperimentProductionSystem(t, map[supervisor.AgentID][]string{"agent-A": {}, "agent-B": {}}, expStore, exp.Definition.ID)

	dispatchOneTask(t, sched, "T1")
	final := state.Snapshot()
	if final.Assignments["T1"] != "agent-B" {
		t.Fatalf("expected the challenger admitted before kill, got %q", final.Assignments["T1"])
	}

	// Admin/Core kills the experiment.
	killedExp, _ := expStore.Get(context.Background(), exp.Definition.ID)
	killedExp.State = ExperimentKilled
	if err := expStore.Save(context.Background(), killedExp); err != nil {
		t.Fatalf("Save (kill): %v", err)
	}

	dispatchOneTask(t, sched, "T2")
	final = state.Snapshot()
	if final.Assignments["T2"] != "agent-A" {
		t.Fatalf("expected champion (agent-A) after kill, got %q", final.Assignments["T2"])
	}
}

// TestE2E_LiveRevalidation_AdmittedAgentOutsideSnapshotFallsBack proves
// an admitted challenger that is NOT actually in the live candidate set
// (e.g. capability lost) is rejected by the TOCTOU re-check in
// Scheduler.selectAgent and falls back to the champion.
func TestE2E_LiveRevalidation_AdmittedAgentOutsideSnapshotFallsBack(t *testing.T) {
	expStore := NewMemoryExperimentStore()
	exp := Experiment{
		Definition: ExperimentDefinition{
			ID: "exp-toctou", Tenant: "tenant-a", TaskClass: "security_review",
			ChallengerPolicy:  NewRoutingPolicyDescriptor("performance", "v2", "s1", nil),
			ChallengerAgentID: "agent-NOT-REGISTERED", // never a real, eligible agent
			MaxExecutions:     10, MaxConcurrent: 10, AllocationPercent: 100, AllocationSemantics: AllocationSemanticsV1,
		},
		State: ExperimentEnabled, UpdatedAt: time.Now().UTC(),
	}
	if err := expStore.Create(context.Background(), exp); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sched, state, _, _, _ := newExperimentProductionSystem(t, map[supervisor.AgentID][]string{"agent-A": {}}, expStore, exp.Definition.ID)

	dispatchOneTask(t, sched, "T1")
	final := state.Snapshot()
	if final.Assignments["T1"] != "agent-A" {
		t.Fatalf("expected champion fallback since the configured challenger is not a real eligible candidate, got %q", final.Assignments["T1"])
	}
}
