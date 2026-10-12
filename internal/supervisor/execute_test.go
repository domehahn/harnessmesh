package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// setupAssignedTask creates and assigns a task to agentID, returning the
// CoreState, directory, and Executor ready to run it.
func setupAssignedTask(t *testing.T, dir *fakeDirectory, task Task, agentID AgentID, limits SupervisorLimits, provider ExecutionProvider) (*CoreState, *Executor) {
	t.Helper()
	v := NewValidator(dir, nil, limits)
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)

	if _, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: task, RationaleSummary: "create"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: task.ID, CandidateAgentID: agentID, RequiredCaps: task.RequiredCapabilities, RationaleSummary: "assign"},
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	exec := NewExecutor(dir, AllowAllAuthorizer{}, provider, limits, state)
	return state, exec
}

// setupAssignedTaskWithExecutorAuthorizer is like setupAssignedTask but lets
// the caller use a different Authorizer for the Executor than the one used
// at Apply time, so tests can simulate a policy change that happens after
// assignment but before execution.
func setupAssignedTaskWithExecutorAuthorizer(t *testing.T, dir *fakeDirectory, task Task, agentID AgentID, limits SupervisorLimits, provider ExecutionProvider, execAuthz Authorizer) (*CoreState, *Executor) {
	t.Helper()
	v := NewValidator(dir, nil, limits)
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state)

	if _, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: task, RationaleSummary: "create"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: task.ID, CandidateAgentID: agentID, RequiredCaps: task.RequiredCapabilities, RationaleSummary: "assign"},
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	exec := NewExecutor(dir, execAuthz, provider, limits, state)
	return state, exec
}

func TestExecute_HappyPath(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"codex": {"review"}})
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted, Summary: "ok"}, nil
	}}
	state, exec := setupAssignedTask(t, dir, Task{ID: "T1", RequiredCapabilities: []string{"review"}}, "codex", DefaultSupervisorLimits(), provider)

	result, err := exec.Execute(context.Background(), "T1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TaskStatusCompleted {
		t.Fatalf("expected completed, got %q", result.Status)
	}
	after := state.Snapshot()
	t1, _ := after.Tasks.ByID("T1")
	if t1.Status != TaskStatusCompleted {
		t.Fatalf("expected task status completed in state, got %q", t1.Status)
	}
}

// TestExecute_AgentDisappearedBetweenValidationAndExecution proves
// requirement 10/11: an agent that existed at assignment time but was
// removed from the live directory before execution is caught at the
// execution boundary, not silently executed.
func TestExecute_AgentDisappearedBetweenValidationAndExecution(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"codex": {"review"}})
	called := false
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		called = true
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	_, exec := setupAssignedTask(t, dir, Task{ID: "T1", RequiredCapabilities: []string{"review"}}, "codex", DefaultSupervisorLimits(), provider)

	dir.removeAgent("codex") // TOCTOU: agent vanishes after assignment

	_, err := exec.Execute(context.Background(), "T1")
	var routingErr *AgentRoutingFailure
	if !errors.As(err, &routingErr) {
		t.Fatalf("expected AgentRoutingFailure, got %v (%T)", err, err)
	}
	if called {
		t.Fatalf("the execution provider must never be called once the agent has disappeared")
	}
}

// TestExecute_CapabilityRevokedBetweenValidationAndExecution proves
// requirement 10/11: an agent that loses a required capability after
// assignment but before execution is blocked at the execution boundary.
func TestExecute_CapabilityRevokedBetweenValidationAndExecution(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"codex": {"review"}})
	called := false
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		called = true
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	_, exec := setupAssignedTask(t, dir, Task{ID: "T1", RequiredCapabilities: []string{"review"}}, "codex", DefaultSupervisorLimits(), provider)

	dir.setCapabilities("codex", []string{}) // TOCTOU: capability revoked after assignment

	_, err := exec.Execute(context.Background(), "T1")
	var authErr *AuthorizationFailure
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthorizationFailure, got %v (%T)", err, err)
	}
	if called {
		t.Fatalf("the execution provider must never be called once the capability is gone")
	}
}

// TestExecute_CrossTenantAtExecutionBoundary proves tenant scope is
// re-checked at the execution boundary even if it somehow passed
// authorization earlier (e.g. the agent's tenant changed in between).
func TestExecute_CrossTenantAtExecutionBoundary(t *testing.T) {
	dir := newFakeDirectoryWithTenants(map[AgentID][]string{"codex": {"review"}}, map[AgentID]string{"codex": "tenant-a"})
	called := false
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		called = true
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	_, exec := setupAssignedTask(t, dir, Task{ID: "T1", RequiredCapabilities: []string{"review"}, Tenant: "tenant-a"}, "codex", DefaultSupervisorLimits(), provider)

	dir.setTenant("codex", "tenant-b") // TOCTOU: agent moved to a different tenant

	_, err := exec.Execute(context.Background(), "T1")
	var authErr *AuthorizationFailure
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthorizationFailure for cross-tenant execution, got %v (%T)", err, err)
	}
	if called {
		t.Fatalf("the execution provider must never be called across a tenant boundary")
	}
}

// TestExecute_TenantScopedTaskFailsClosedWithoutTenantDirectory proves the
// same fail-closed tenant behavior as
// TestApply_TenantScopedTaskFailsClosedWithoutTenantDirectory, but at the
// execution boundary (defense in depth: even if a tenant-scoped task
// somehow got assigned, e.g. by an Authorizer with no tenant checks
// configured, the Executor independently refuses to run it against a
// directory that cannot resolve tenancy).
func TestExecute_TenantScopedTaskFailsClosedWithoutTenantDirectory(t *testing.T) {
	dir := &bareDirectory{agents: map[AgentID][]string{"codex": {"review"}}}
	called := false
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		called = true
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	v := NewValidator(dir, nil, DefaultSupervisorLimits())
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, AllowAllAuthorizer{}, state) // no tenant checks at authorization time

	if _, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1", Tenant: "tenant-a", RequiredCapabilities: []string{"review"}}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RequiredCaps: []string{"review"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("setup assign: %v", err)
	}

	exec := NewExecutor(dir, AllowAllAuthorizer{}, provider, DefaultSupervisorLimits(), state)
	_, err := exec.Execute(context.Background(), "T1")
	var authErr *AuthorizationFailure
	if !errors.As(err, &authErr) {
		t.Fatalf("expected a fail-closed AuthorizationFailure at the execution boundary, got %v (%T)", err, err)
	}
	if called {
		t.Fatalf("the execution provider must never run a tenant-scoped task when tenancy cannot be resolved")
	}
}

// TestExecute_StalePolicyRevokedBeforeExecutionIsReAuthorized is the
// closure-review finding: a policy decision made at Apply time (the
// agent was allowed) can become stale by the time the task actually
// executes, if an operator revokes that agent in the interim. Before this
// test was added, Executor never re-ran Authorizer at all, so a revoked
// DeniedAgents entry would have had no effect on an already-assigned
// task. This proves it now does.
func TestExecute_StalePolicyRevokedBeforeExecutionIsReAuthorized(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"codex": {"review"}})
	called := false
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		called = true
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}}
	// Simulates an operator revoking "codex" via policy AFTER the task was
	// already assigned (assignment happened under AllowAllAuthorizer{}).
	revoking := &CoreAuthorizer{DeniedAgents: map[AgentID]bool{"codex": true}}
	_, exec := setupAssignedTaskWithExecutorAuthorizer(t, dir, Task{ID: "T1", RequiredCapabilities: []string{"review"}}, "codex", DefaultSupervisorLimits(), provider, revoking)

	_, err := exec.Execute(context.Background(), "T1")
	var policyErr *PolicyRejection
	if !errors.As(err, &policyErr) {
		t.Fatalf("expected the revoked policy to be caught at the execution boundary, got %v (%T)", err, err)
	}
	if called {
		t.Fatalf("the execution provider must never run once the agent is denied by policy, even if assignment predates the denial")
	}
}

// TestExecute_Timeout proves MaxExecutionTime is enforced at the execution
// boundary, not during proposal validation.
func TestExecute_Timeout(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"codex": {"review"}})
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		<-ctx.Done()
		return ExecutionResult{}, ctx.Err()
	}}
	limits := DefaultSupervisorLimits()
	limits.MaxExecutionTime = 50 * time.Millisecond
	_, exec := setupAssignedTask(t, dir, Task{ID: "T1", RequiredCapabilities: []string{"review"}}, "codex", limits, provider)

	_, err := exec.Execute(context.Background(), "T1")
	var timeoutErr *ExecutionTimeout
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected ExecutionTimeout, got %v (%T)", err, err)
	}
}

// TestExecute_AgentExecutionFailure proves a non-timeout provider error
// surfaces as AgentExecutionFailure and marks the task failed.
func TestExecute_AgentExecutionFailure(t *testing.T) {
	dir := newFakeDirectory(map[AgentID][]string{"codex": {"review"}})
	provider := &FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
		return ExecutionResult{}, errors.New("process crashed")
	}}
	state, exec := setupAssignedTask(t, dir, Task{ID: "T1", RequiredCapabilities: []string{"review"}}, "codex", DefaultSupervisorLimits(), provider)

	_, err := exec.Execute(context.Background(), "T1")
	var execErr *AgentExecutionFailure
	if !errors.As(err, &execErr) {
		t.Fatalf("expected AgentExecutionFailure, got %v (%T)", err, err)
	}
	after := state.Snapshot()
	t1, _ := after.Tasks.ByID("T1")
	if t1.Status != TaskStatusFailed {
		t.Fatalf("expected task marked failed, got %q", t1.Status)
	}
}
