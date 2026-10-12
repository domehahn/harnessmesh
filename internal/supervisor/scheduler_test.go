package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestComputeReadiness_Basic(t *testing.T) {
	graph := TaskGraph{Tasks: []Task{
		{ID: "A", Status: TaskStatusCompleted},
		{ID: "B", Dependencies: []TaskID{"A"}}, // ready: A completed
		{ID: "C", Status: TaskStatusFailed},
		{ID: "D", Dependencies: []TaskID{"C"}}, // blocked: C failed
		{ID: "E", Dependencies: []TaskID{"B"}}, // waiting: B not yet terminal
		{ID: "F", Status: TaskStatusAssigned},
		{ID: "G"}, // ready: no dependencies
	}}
	got := computeReadiness(graph)
	want := map[TaskID]ReadinessState{
		"A": ReadinessDone,
		"B": ReadinessReady,
		"C": ReadinessDone,
		"D": ReadinessBlocked,
		"E": ReadinessWaiting,
		"F": ReadinessActive,
		"G": ReadinessReady,
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("task %q: expected readiness %q, got %q", id, w, got[id])
		}
	}
}

func TestRouteAgent_DeterministicAmongMultipleEligible(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{
		"zeta":  {"review"},
		"alpha": {"review"},
		"beta":  {"review"},
	})
	task := Task{ID: "T1", RequiredCapabilities: []string{"review"}}
	agentID, err := routeAgent(context.Background(), dir, task)
	if err != nil {
		t.Fatalf("routeAgent: %v", err)
	}
	if agentID != "alpha" {
		t.Fatalf("expected deterministic lexicographically-smallest eligible agent %q, got %q", "alpha", agentID)
	}
}

func TestRouteAgent_NoEligibleAgent(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"codex": {"review"}})
	task := Task{ID: "T1", RequiredCapabilities: []string{"write_repository"}}
	_, err := routeAgent(context.Background(), dir, task)
	if _, ok := err.(*AgentRoutingFailure); !ok {
		t.Fatalf("expected AgentRoutingFailure, got %v (%T)", err, err)
	}
}

func newTestScheduler(t *testing.T, agents map[AgentID][]string, limits SupervisorLimits, provider ExecutionProvider) (*Scheduler, *CoreState) {
	t.Helper()
	dir := newFakeDirectory(agents)
	v := NewValidator(dir, nil, limits)
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)
	executor := NewExecutor(dir, AllowAllAuthorizer{}, provider, limits, state)
	sched := NewScheduler(applier, executor, dir, limits)
	return sched, state
}

func TestScheduler_Tick_DispatchesReadyTasksAndRoutesDeterministically(t *testing.T) {
	limits := DefaultSupervisorLimits()
	var executed []AgentID
	var mu sync.Mutex
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		mu.Lock()
		executed = append(executed, agentID)
		mu.Unlock()
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	sched, state := newTestScheduler(t, map[AgentID][]string{"codex": {"review"}}, limits, provider)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1", RequiredCapabilities: []string{"review"}}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(executed) != 1 || executed[0] != "codex" {
		t.Fatalf("expected codex to execute T1, got %v", executed)
	}
	after := state.Snapshot()
	t1, _ := after.Tasks.ByID("T1")
	if t1.Status != TaskStatusCompleted {
		t.Fatalf("expected T1 completed, got %q", t1.Status)
	}

	var kinds []ExecutionEventKind
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	hasKind := func(k ExecutionEventKind) bool {
		for _, kk := range kinds {
			if kk == k {
				return true
			}
		}
		return false
	}
	for _, want := range []ExecutionEventKind{EventTaskReady, EventTaskAssigned, EventTaskStarted, EventTaskSucceeded} {
		if !hasKind(want) {
			t.Fatalf("expected event kind %q in %v", want, kinds)
		}
	}
}

func TestScheduler_Tick_DependencyOrderingRespected(t *testing.T) {
	limits := DefaultSupervisorLimits()
	var order []TaskID
	var mu sync.Mutex
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		mu.Lock()
		order = append(order, task.ID)
		mu.Unlock()
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	sched, _ := newTestScheduler(t, map[AgentID][]string{"codex": {}}, limits, provider)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
		CreateTaskProposal{Task: Task{ID: "T2", Dependencies: []TaskID{"T1"}}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Tick 1: only T1 is ready (T2 depends on it).
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if len(order) != 1 || order[0] != "T1" {
		t.Fatalf("expected only T1 to execute on tick 1, got %v", order)
	}

	// Tick 2: T2 is now ready since T1 completed.
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(order) != 2 || order[1] != "T2" {
		t.Fatalf("expected T2 to execute on tick 2 after T1, got %v", order)
	}
}

func TestScheduler_Tick_NeverExecutesBlockedTask(t *testing.T) {
	limits := DefaultSupervisorLimits()
	var executed []TaskID
	var mu sync.Mutex
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		mu.Lock()
		executed = append(executed, task.ID)
		mu.Unlock()
		if task.ID == "T1" {
			return ExecutionResult{}, context.DeadlineExceeded // force a non-timeout failure path is fine; just needs to fail
		}
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	sched, state := newTestScheduler(t, map[AgentID][]string{"codex": {}}, limits, provider)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
		CreateTaskProposal{Task: Task{ID: "T2", Dependencies: []TaskID{"T1"}}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	after := state.Snapshot()
	t1, _ := after.Tasks.ByID("T1")
	if t1.Status != TaskStatusFailed {
		t.Fatalf("expected T1 failed, got %q", t1.Status)
	}

	// Tick 2: T2 must be reported Blocked, and T2 must never execute.
	events2, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	allEvents := append(events, events2...)
	foundBlocked := false
	for _, e := range allEvents {
		if e.Kind == EventTaskBlocked && e.TaskID == "T2" {
			foundBlocked = true
		}
	}
	if !foundBlocked {
		t.Fatalf("expected a TaskBlocked event for T2, got %+v", allEvents)
	}
	for _, id := range executed {
		if id == "T2" {
			t.Fatalf("T2 must never execute while blocked")
		}
	}
}

func TestScheduler_Tick_ConcurrencyLimitRespected(t *testing.T) {
	limits := DefaultSupervisorLimits()
	limits.MaxConcurrency = 2
	var concurrent atomic.Int32
	var maxObserved atomic.Int32
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		cur := concurrent.Add(1)
		defer concurrent.Add(-1)
		for {
			m := maxObserved.Load()
			if cur <= m || maxObserved.CompareAndSwap(m, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	sched, _ := newTestScheduler(t, map[AgentID][]string{"codex": {}}, limits, provider)

	var proposals []AgentProposal
	for i := 0; i < 10; i++ {
		proposals = append(proposals, CreateTaskProposal{Task: Task{ID: TaskID("T" + string(rune('A'+i)))}, RationaleSummary: "r"})
	}
	if _, err := sched.Applier.Apply(context.Background(), proposals); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if maxObserved.Load() > int32(limits.MaxConcurrency) {
		t.Fatalf("observed %d concurrent executions, exceeding MaxConcurrency=%d", maxObserved.Load(), limits.MaxConcurrency)
	}
	if maxObserved.Load() < 1 {
		t.Fatalf("expected at least 1 concurrent execution, got %d", maxObserved.Load())
	}
}

func TestWorkflowStatus_CompletionSemantics(t *testing.T) {
	// Not complete: a task still pending.
	complete, succeeded, _ := WorkflowStatus(TaskGraph{Tasks: []Task{
		{ID: "A", Status: TaskStatusCompleted},
		{ID: "B"},
	}})
	if complete || succeeded {
		t.Fatalf("expected incomplete workflow, got complete=%v succeeded=%v", complete, succeeded)
	}

	// Complete and succeeded: everything Completed.
	complete, succeeded, _ = WorkflowStatus(TaskGraph{Tasks: []Task{
		{ID: "A", Status: TaskStatusCompleted},
		{ID: "B", Status: TaskStatusCompleted},
	}})
	if !complete || !succeeded {
		t.Fatalf("expected complete+succeeded, got complete=%v succeeded=%v", complete, succeeded)
	}

	// Complete but not succeeded: a task Failed.
	complete, succeeded, _ = WorkflowStatus(TaskGraph{Tasks: []Task{
		{ID: "A", Status: TaskStatusCompleted},
		{ID: "B", Status: TaskStatusFailed},
	}})
	if !complete || succeeded {
		t.Fatalf("expected complete but not succeeded, got complete=%v succeeded=%v", complete, succeeded)
	}

	// Blocked forever: never complete.
	complete, succeeded, blocked := WorkflowStatus(TaskGraph{Tasks: []Task{
		{ID: "A", Status: TaskStatusFailed},
		{ID: "B", Dependencies: []TaskID{"A"}},
	}})
	if complete || succeeded {
		t.Fatalf("expected incomplete+unsucceeded workflow with a blocked task, got complete=%v succeeded=%v", complete, succeeded)
	}
	if len(blocked) != 1 || blocked[0] != "B" {
		t.Fatalf("expected B reported as blocked, got %v", blocked)
	}
}
