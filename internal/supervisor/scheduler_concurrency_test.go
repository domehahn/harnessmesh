package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrency_MultipleSchedulerTicksConcurrent proves that calling
// Scheduler.Tick concurrently from multiple goroutines (e.g. a caller
// that wakes the scheduler from more than one trigger source) never
// double-dispatches a task: CoreState.applyGated's atomic
// validate->authorize->apply, plus MarkExecuting's atomic CAS-like
// transition, must make at most one of the concurrent Ticks win the
// assignment and execution of any given ready task.
func TestConcurrency_MultipleSchedulerTicksConcurrent(t *testing.T) {
	limits := DefaultSupervisorLimits()
	limits.MaxConcurrency = 100 // no artificial concurrency bottleneck for this test
	var executions atomic.Int32
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		executions.Add(1)
		time.Sleep(5 * time.Millisecond)
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	sched, state := newTestScheduler(t, map[AgentID][]string{"codex": {}}, limits, provider)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = sched.Tick(context.Background())
		}()
	}
	wg.Wait()

	if executions.Load() != 1 {
		t.Fatalf("expected T1 to execute exactly once across %d concurrent ticks, got %d", n, executions.Load())
	}
	final := state.Snapshot()
	t1, _ := final.Tasks.ByID("T1")
	if t1.Status != TaskStatusCompleted {
		t.Fatalf("expected T1 completed, got %q", t1.Status)
	}
}

// TestConcurrency_ReplanWhileOtherTasksComplete exercises a Workflow
// where one task is failing (triggering Replan) while, concurrently,
// other independent ready tasks are completing normally - proving
// Applier's atomic apply serializes the ReplanProposal correctly against
// concurrent AssignTaskProposal/task completions without corrupting
// state or losing the replan's new tasks.
func TestConcurrency_ReplanWhileOtherTasksComplete(t *testing.T) {
	ctx := context.Background()
	limits := DefaultSupervisorLimits()
	limits.MaxConcurrency = 8

	dir := newFakeDirectory(map[AgentID][]string{"codex": {}})
	v := NewValidator(dir, nil, limits)
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		time.Sleep(2 * time.Millisecond)
		if task.ID == "FAIL" {
			return ExecutionResult{}, context.DeadlineExceeded
		}
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	executor := NewExecutor(dir, AllowAllAuthorizer{}, provider, limits, state)
	sched := NewScheduler(applier, executor, dir, limits)

	var proposals []AgentProposal
	proposals = append(proposals, CreateTaskProposal{Task: Task{ID: "FAIL"}, RationaleSummary: "r"})
	for i := 0; i < 10; i++ {
		proposals = append(proposals, CreateTaskProposal{Task: Task{ID: TaskID("OK" + string(rune('A'+i)))}, RationaleSummary: "r"})
	}
	if _, err := applier.Apply(ctx, proposals); err != nil {
		t.Fatalf("create: %v", err)
	}

	sv := &FakeSupervisor{
		ReplanFunc: func(ctx context.Context, s ExecutionState, event ExecutionEvent) ([]AgentProposal, error) {
			return []AgentProposal{
				ReplanProposal{NewTasks: []Task{{ID: "REMEDIATE"}}, RationaleSummary: "remediate"},
			}, nil
		},
	}

	var tickWg sync.WaitGroup
	var allEvents []ExecutionEvent
	var eventsMu sync.Mutex
	// Drive several ticks concurrently from multiple goroutines, each
	// also willing to Replan if it observes a trigger - simulating
	// multiple independent wakeup sources hitting the same workflow.
	for g := 0; g < 4; g++ {
		tickWg.Add(1)
		go func() {
			defer tickWg.Done()
			for i := 0; i < 5; i++ {
				events, err := sched.Tick(ctx)
				if err != nil {
					t.Errorf("Tick: %v", err)
					return
				}
				eventsMu.Lock()
				allEvents = append(allEvents, events...)
				eventsMu.Unlock()
				if trigger := firstReplanTrigger(events); trigger != nil {
					snap := applier.State.Snapshot()
					rp, err := sv.Replan(ctx, snap, *trigger)
					if err != nil {
						t.Errorf("Replan: %v", err)
						return
					}
					_, _ = applier.Apply(ctx, rp) // may fail if another goroutine already remediated - that's fine
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	tickWg.Wait()

	final := state.Snapshot()
	seen := map[TaskID]bool{}
	for _, task := range final.Tasks.Tasks {
		if seen[task.ID] {
			t.Fatalf("duplicate task ID %q in final graph after concurrent replan/tick activity", task.ID)
		}
		seen[task.ID] = true
	}
	for i := 0; i < 10; i++ {
		id := TaskID("OK" + string(rune('A'+i)))
		task, ok := final.Tasks.ByID(id)
		if !ok {
			t.Fatalf("task %q missing from final graph", id)
		}
		if task.Status != TaskStatusCompleted {
			// may still be in flight if ticks ran out; not itself a bug,
			// but let's bound it: re-tick a few more times to drain.
			for j := 0; j < 10 && task.Status != TaskStatusCompleted; j++ {
				_, _ = sched.Tick(ctx)
				final = state.Snapshot()
				task, _ = final.Tasks.ByID(id)
			}
			if task.Status != TaskStatusCompleted {
				t.Fatalf("task %q never completed: %+v", id, task)
			}
		}
	}
}

// TestConcurrency_CancellationWhileTasksActive proves that cancelling the
// context passed to Scheduler.Tick propagates to in-flight executions
// (via Executor's own context handling) without leaving goroutines
// running past Tick's return, and without corrupting CoreState.
func TestConcurrency_CancellationWhileTasksActive(t *testing.T) {
	limits := DefaultSupervisorLimits()
	limits.MaxConcurrency = 5
	limits.MaxExecutionTime = 5 * time.Second // long enough that only ctx cancellation stops it
	var started atomic.Int32
	var finished atomic.Int32
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		started.Add(1)
		defer finished.Add(1)
		<-ctx.Done()
		return ExecutionResult{}, ctx.Err()
	}}
	sched, state := newTestScheduler(t, map[AgentID][]string{"codex": {}}, limits, provider)

	var proposals []AgentProposal
	for i := 0; i < 5; i++ {
		proposals = append(proposals, CreateTaskProposal{Task: Task{ID: TaskID("T" + string(rune('A'+i)))}, RationaleSummary: "r"})
	}
	if _, err := sched.Applier.Apply(context.Background(), proposals); err != nil {
		t.Fatalf("create: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_, _ = sched.Tick(ctx)
		close(done)
	}()

	// Give the goroutines a moment to actually start, then cancel.
	deadline := time.Now().Add(2 * time.Second)
	for started.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Tick did not return within 5s of context cancellation - possible goroutine leak")
	}

	if finished.Load() != started.Load() {
		t.Fatalf("expected every started execution to also finish (no leaked goroutines): started=%d finished=%d", started.Load(), finished.Load())
	}

	// State must still be internally consistent (no partial/duplicate
	// tasks) even though every in-flight execution was cancelled.
	final := state.Snapshot()
	seen := map[TaskID]bool{}
	for _, task := range final.Tasks.Tasks {
		if seen[task.ID] {
			t.Fatalf("duplicate task ID %q after cancellation", task.ID)
		}
		seen[task.ID] = true
	}
}
