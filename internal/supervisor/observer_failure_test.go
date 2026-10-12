package supervisor

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// TestObserverFailure_SinkInterfaceCarriesNoPayload is a structural
// proof (not just convention) that a recovered panic's raw value can
// never reach telemetry: ExecutionObserverFailureSink's method takes no
// parameters at all.
func TestObserverFailure_SinkInterfaceCarriesNoPayload(t *testing.T) {
	typ := reflect.TypeOf((*ExecutionObserverFailureSink)(nil)).Elem()
	method, ok := typ.MethodByName("RecordExecutionObserverPanic")
	if !ok {
		t.Fatalf("expected RecordExecutionObserverPanic to exist")
	}
	if method.Type.NumIn() != 0 {
		t.Fatalf("expected RecordExecutionObserverPanic to take no arguments (so a panic payload can never be passed through), got %d", method.Type.NumIn())
	}
}

// --- ADR 0006 Phase 7C: Observer Failure Visibility ---

type panickingObserver struct {
	payload any
}

func (p *panickingObserver) ObserveExecution(ctx context.Context, task Task, agentID AgentID, executionID ExecutionID, result ExecutionResult, execErr error, startedAt, finishedAt time.Time) {
	panic(p.payload)
}

type countingFailureSink struct {
	count int
}

func (s *countingFailureSink) RecordExecutionObserverPanic() { s.count++ }

func TestObserverFailure_PanicDoesNotAffectExecutionResult(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"agent-a": {}})
	v := NewValidator(dir, nil, DefaultSupervisorLimits())
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted, Summary: "real"}, nil
	}}
	exec := NewExecutor(dir, AllowAllAuthorizer{}, provider, DefaultSupervisorLimits(), state)
	sink := &countingFailureSink{}
	exec = exec.WithObserver(&panickingObserver{payload: "boom"})
	exec.FailureSink = sink // observer itself doesn't implement the sink interface here

	if _, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "agent-a", RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	result, err := exec.Execute(context.Background(), "T1")
	if err != nil {
		t.Fatalf("execution must succeed despite an observer panic, got error: %v", err)
	}
	if result.Status != TaskStatusCompleted || result.Summary != "real" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if sink.count != 1 {
		t.Fatalf("expected the panic to be recorded exactly once, got %d", sink.count)
	}
}

type panickingFailureSink struct {
	calls int
}

func (s *panickingFailureSink) RecordExecutionObserverPanic() {
	s.calls++
	panic("sink itself panics")
}

func TestObserverFailure_FailureSinkItselfPanickingDoesNotCrashExecute(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"agent-a": {}})
	v := NewValidator(dir, nil, DefaultSupervisorLimits())
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	exec := NewExecutor(dir, AllowAllAuthorizer{}, provider, DefaultSupervisorLimits(), state)
	exec = exec.WithObserver(&panickingObserver{payload: "boom"})
	sink := &panickingFailureSink{}
	exec.FailureSink = sink

	if _, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "agent-a", RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	// Must not panic/crash the test process - the recursive recover()
	// must contain even a panicking FailureSink.
	result, err := exec.Execute(context.Background(), "T1")
	if err != nil {
		t.Fatalf("execution must still succeed, got error: %v", err)
	}
	if result.Status != TaskStatusCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}
	if sink.calls != 1 {
		t.Fatalf("expected the sink to have been invoked exactly once, got %d", sink.calls)
	}
}

func TestObserverFailure_MultiplePanicsCountedDeterministically(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"agent-a": {}})
	v := NewValidator(dir, nil, DefaultSupervisorLimits())
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	sink := &countingFailureSink{}

	for i, id := range []TaskID{"T1", "T2", "T3"} {
		exec := NewExecutor(dir, AllowAllAuthorizer{}, provider, DefaultSupervisorLimits(), state)
		exec = exec.WithObserver(&panickingObserver{payload: i})
		exec.FailureSink = sink
		if _, err := applier.Apply(context.Background(), []AgentProposal{
			CreateTaskProposal{Task: Task{ID: id}, RationaleSummary: "r"},
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if _, err := applier.Apply(context.Background(), []AgentProposal{
			AssignTaskProposal{TaskID: id, CandidateAgentID: "agent-a", RationaleSummary: "r"},
		}); err != nil {
			t.Fatalf("assign %s: %v", id, err)
		}
		if _, err := exec.Execute(context.Background(), id); err != nil {
			t.Fatalf("Execute %s: %v", id, err)
		}
	}

	if sink.count != 3 {
		t.Fatalf("expected exactly 3 recorded panics, got %d", sink.count)
	}
}
