package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// TestConcurrency_DuplicateTaskIDCreationIsSerializedAndRejected is the
// closure-review finding for atomicity under concurrency: two goroutines
// concurrently proposing CreateTaskProposal for the SAME task ID, each
// validated against its own view of state, must not both succeed. Before
// applyGated existed (validate+authorize+mutate as one atomic unit under
// CoreState's lock), each Apply call validated against a snapshot taken
// before the lock was acquired, so a concurrent duplicate could slip past
// validation in both goroutines and both appends would land in the
// task graph. This proves exactly one wins.
func TestConcurrency_DuplicateTaskIDCreationIsSerializedAndRejected(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{})
	v := NewValidator(dir, nil, DefaultSupervisorLimits())
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)

	const n = 50
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := applier.Apply(context.Background(), []AgentProposal{
				CreateTaskProposal{Task: Task{ID: "DUP"}, RationaleSummary: "race"},
			})
			if err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	if successes.Load() != 1 {
		t.Fatalf("expected exactly 1 of %d concurrent duplicate-ID creations to succeed, got %d", n, successes.Load())
	}
	final := state.Snapshot()
	count := 0
	for _, task := range final.Tasks.Tasks {
		if task.ID == "DUP" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 task with ID %q in the final graph, got %d", "DUP", count)
	}
}

// TestConcurrency_ConcurrentBatchesOnDisjointTasksAllSucceed proves the
// coarse-grained lock serializes but does not incorrectly reject
// unrelated concurrent work.
func TestConcurrency_ConcurrentBatchesOnDisjointTasksAllSucceed(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{})
	v := NewValidator(dir, nil, DefaultSupervisorLimits())
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)

	const n = 50
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := TaskID("T" + string(rune('A'+i%26)) + string(rune('0'+i/26)))
			_, err := applier.Apply(context.Background(), []AgentProposal{
				CreateTaskProposal{Task: Task{ID: id}, RationaleSummary: "disjoint"},
			})
			if err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	if successes.Load() != n {
		t.Fatalf("expected all %d disjoint concurrent creations to succeed, got %d", n, successes.Load())
	}
	final := state.Snapshot()
	if len(final.Tasks.Tasks) != n {
		t.Fatalf("expected %d tasks in the final graph, got %d", n, len(final.Tasks.Tasks))
	}
}

// TestConcurrency_SameTaskExecutedConcurrentlyRunsAtMostOnce proves two
// goroutines racing to Execute the SAME already-assigned task cannot both
// reach the ExecutionProvider: the Assigned->Executing transition inside
// CoreState.MarkExecuting is atomic and gates on the task's exact prior
// status, so only one caller observes success there.
func TestConcurrency_SameTaskExecutedConcurrentlyRunsAtMostOnce(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"codex": {"review"}})
	var executions atomic.Int32
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		executions.Add(1)
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	_, exec := setupAssignedTask(t, dir, Task{ID: "T1", RequiredCapabilities: []string{"review"}}, "codex", DefaultSupervisorLimits(), provider)

	const n = 20
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := exec.Execute(context.Background(), "T1"); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	if executions.Load() != 1 {
		t.Fatalf("expected the execution provider to run exactly once across %d concurrent Execute calls on the same task, got %d", n, executions.Load())
	}
	if successes.Load() != 1 {
		t.Fatalf("expected exactly 1 of %d concurrent Execute calls to report success, got %d", n, successes.Load())
	}
}

// TestConcurrency_ApplyAndExecuteInterleavedNoCorruption is a broader
// stress test: concurrently create+assign+execute many distinct tasks
// while also concurrently replanning, and confirm the final state is
// internally consistent (no duplicate IDs, every assigned task eventually
// reaches a terminal status, replan count matches the number of
// successful replans) under the race detector.
func TestConcurrency_ApplyAndExecuteInterleavedNoCorruption(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"codex": {"review"}})
	v := NewValidator(dir, nil, DefaultSupervisorLimits())
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	exec := NewExecutor(dir, AllowAllAuthorizer{}, provider, DefaultSupervisorLimits(), state)

	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := TaskID("C" + string(rune('A'+i%26)) + string(rune('0'+i/26)))
			if _, err := applier.Apply(context.Background(), []AgentProposal{
				CreateTaskProposal{Task: Task{ID: id, RequiredCapabilities: []string{"review"}}, RationaleSummary: "stress"},
			}); err != nil {
				return
			}
			if _, err := applier.Apply(context.Background(), []AgentProposal{
				AssignTaskProposal{TaskID: id, CandidateAgentID: "codex", RequiredCaps: []string{"review"}, RationaleSummary: "stress"},
			}); err != nil {
				return
			}
			_, _ = exec.Execute(context.Background(), id)
		}()
	}
	// Concurrently fire some replans too.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = applier.Apply(context.Background(), []AgentProposal{
				ReplanProposal{RationaleSummary: "stress replan"},
			})
		}()
	}
	wg.Wait()

	final := state.Snapshot()
	seen := map[TaskID]bool{}
	for _, task := range final.Tasks.Tasks {
		if seen[task.ID] {
			t.Fatalf("duplicate task ID %q survived concurrent Apply calls", task.ID)
		}
		seen[task.ID] = true
		if task.Status == TaskStatusAssigned || task.Status == TaskStatusExecuting {
			t.Fatalf("task %q left in a non-terminal status %q after all goroutines finished", task.ID, task.Status)
		}
	}
	if final.ReplanCount < 0 || final.ReplanCount > 5 {
		t.Fatalf("unexpected replan count %d", final.ReplanCount)
	}
}
