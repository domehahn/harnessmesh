package supervisor

import (
	"context"
	"errors"
	"testing"
)

func newApplier(t *testing.T, agents map[AgentID][]string, authz Authorizer, limits SupervisorLimits, persist func(TaskGraph) error) (*Applier, *CoreState) {
	t.Helper()
	dir := newFakeDirectory(agents)
	v := NewValidator(dir, nil, limits)
	state := NewCoreState(TaskGraph{}, persist)
	if authz == nil {
		authz = AllowAllAuthorizer{}
	}
	return NewApplier(v, authz, state), state
}

// TestApply_ValidatedProposalIsNotImplicitlyAuthorized proves requirement
// 2: a batch that passes ValidateBatch cleanly can still be rejected by
// the Authorizer, and when it is, CoreState is left completely unchanged.
func TestApply_ValidatedProposalIsNotImplicitlyAuthorized(t *testing.T) {
	denyAll := &CoreAuthorizer{DeniedAgents: map[AgentID]bool{"codex": true}}
	applier, state := newApplier(t, map[AgentID][]string{"codex": {"review"}}, denyAll, DefaultSupervisorLimits(), nil)

	before := state.Snapshot()
	_, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "create"},
	})
	if err != nil {
		t.Fatalf("create_task should validate and authorize fine: %v", err)
	}

	_, err = applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RequiredCaps: []string{"review"}, RationaleSummary: "assign"},
	})
	var policyErr *PolicyRejection
	if !errors.As(err, &policyErr) {
		t.Fatalf("expected PolicyRejection despite passing validation, got %v (%T)", err, err)
	}

	after := state.Snapshot()
	beforeT1, _ := before.Tasks.ByID("T1")
	_ = beforeT1 // T1 didn't exist in `before` (it's created mid-test); compare assignment state instead
	if _, assigned := after.Assignments["T1"]; assigned {
		t.Fatalf("task must not be assigned after a policy rejection: %+v", after.Assignments)
	}
	afterT1, ok := after.Tasks.ByID("T1")
	if !ok || afterT1.Status != TaskStatusPending {
		t.Fatalf("task status must remain pending after a policy rejection, got %+v", afterT1)
	}
}

// TestApply_AtomicBatchNoPartialApplication proves requirement 3: a batch
// containing one valid and one invalid proposal leaves state entirely
// unchanged - not even the valid half is applied.
func TestApply_AtomicBatchNoPartialApplication(t *testing.T) {
	applier, state := newApplier(t, map[AgentID][]string{"codex": {"review"}}, nil, DefaultSupervisorLimits(), nil)

	_, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
		CreateTaskProposal{Task: Task{ID: "T2"}, RationaleSummary: "r"},
	})
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	_, err = applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RequiredCaps: []string{"review"}, RationaleSummary: "valid"},
		AssignTaskProposal{TaskID: "T2", CandidateAgentID: SupervisorAgentID, RationaleSummary: "invalid: self-assign"},
	})
	if err == nil {
		t.Fatalf("expected the batch to fail")
	}

	after := state.Snapshot()
	if _, assigned := after.Assignments["T1"]; assigned {
		t.Fatalf("T1 must not be assigned: the batch containing it was rejected as a whole")
	}
	t1, _ := after.Tasks.ByID("T1")
	if t1.Status != TaskStatusPending {
		t.Fatalf("T1 status must remain pending, got %q", t1.Status)
	}
}

// TestApply_FailedPersistenceLeavesStateUnchanged proves the atomic apply
// step itself rolls back cleanly when persistence fails.
func TestApply_FailedPersistenceLeavesStateUnchanged(t *testing.T) {
	failingPersist := func(TaskGraph) error { return errors.New("simulated disk full") }
	applier, state := newApplier(t, map[AgentID][]string{"codex": {"review"}}, nil, DefaultSupervisorLimits(), nil)
	// Swap in a failing persist hook on the already-constructed state by
	// rebuilding it with the same backing - simplest is to construct state
	// directly with the failing hook.
	state = NewCoreState(TaskGraph{}, failingPersist)
	applier.State = state

	_, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	})
	var stateErr *StateTransitionFailure
	if !errors.As(err, &stateErr) {
		t.Fatalf("expected StateTransitionFailure on persistence failure, got %v (%T)", err, err)
	}
	after := state.Snapshot()
	if len(after.Tasks.Tasks) != 0 {
		t.Fatalf("expected no tasks to be committed after a failed persist, got %+v", after.Tasks.Tasks)
	}
}

// TestApply_InvalidStateTransitionRejected proves requirement 3/6:
// assigning an already-assigned (non-pending) task is a
// StateTransitionFailure, and the first assignment survives untouched.
func TestApply_InvalidStateTransitionRejected(t *testing.T) {
	applier, state := newApplier(t, map[AgentID][]string{"codex": {"review"}, "claude": {"review"}}, nil, DefaultSupervisorLimits(), nil)

	_, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err = applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RequiredCaps: []string{"review"}, RationaleSummary: "first assignment"},
	})
	if err != nil {
		t.Fatalf("first assignment should succeed: %v", err)
	}

	_, err = applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "claude", RequiredCaps: []string{"review"}, RationaleSummary: "re-assign while already assigned"},
	})
	var stateErr *StateTransitionFailure
	if !errors.As(err, &stateErr) {
		t.Fatalf("expected StateTransitionFailure for re-assigning a non-pending task, got %v (%T)", err, err)
	}

	after := state.Snapshot()
	if after.Assignments["T1"] != "codex" {
		t.Fatalf("original assignment must survive the rejected re-assignment, got %q", after.Assignments["T1"])
	}
}

// TestApply_TenantScopedTaskFailsClosedWithoutTenantDirectory proves that
// a tenant-scoped task is rejected, not silently permitted, when the
// configured AgentDirectory has no tenant model at all (the real,
// current state of collaboration.SupervisorAgentDirectory). Absence of
// tenant metadata must never be treated as permission.
func TestApply_TenantScopedTaskFailsClosedWithoutTenantDirectory(t *testing.T) {
	dir := &bareDirectory{agents: map[AgentID][]string{"codex": {"review"}}}
	v := NewValidator(dir, nil, DefaultSupervisorLimits())
	authz := &CoreAuthorizer{Directory: dir}
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, authz, state)

	if _, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RequiredCaps: []string{"review"}, RationaleSummary: "r"},
	})
	var authErr *AuthorizationFailure
	if !errors.As(err, &authErr) {
		t.Fatalf("expected a fail-closed AuthorizationFailure when no TenantDirectory exists, got %v (%T)", err, err)
	}

	after := state.Snapshot()
	if _, assigned := after.Assignments["T1"]; assigned {
		t.Fatalf("a tenant-scoped task must not be assignable when tenancy cannot be resolved")
	}
}

// TestApply_UnscopedTaskUnaffectedByMissingTenantDirectory proves the
// fail-closed tenant check does not regress ordinary, non-tenant-scoped
// work when no TenantDirectory is configured.
func TestApply_UnscopedTaskUnaffectedByMissingTenantDirectory(t *testing.T) {
	dir := &bareDirectory{agents: map[AgentID][]string{"codex": {"review"}}}
	v := NewValidator(dir, nil, DefaultSupervisorLimits())
	authz := &CoreAuthorizer{Directory: dir}
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, authz, state)

	if _, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"}, // no Tenant set
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RequiredCaps: []string{"review"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("unscoped task should still be assignable without a TenantDirectory: %v", err)
	}
}

// TestApply_CrossTenantAssignmentRejectedAtAuthorization proves requirement
// 10 (cross-tenant assignment) is caught by the Authorizer before any
// state mutation, using Task.Tenant + TenantDirectory.
func TestApply_CrossTenantAssignmentRejectedAtAuthorization(t *testing.T) {
	dir := newFakeDirectoryWithTenants(
		map[AgentID][]string{"codex": {"review"}},
		map[AgentID]string{"codex": "tenant-b"},
	)
	v := NewValidator(dir, nil, DefaultSupervisorLimits())
	authz := &CoreAuthorizer{Directory: dir}
	state := NewCoreState(TaskGraph{}, nil)
	applier := NewApplier(v, authz, state)

	_, err := applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1", Tenant: "tenant-a"}, RationaleSummary: "r"},
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err = applier.Apply(context.Background(), []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RequiredCaps: []string{"review"}, RationaleSummary: "cross-tenant"},
	})
	var authErr *AuthorizationFailure
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthorizationFailure for cross-tenant assignment, got %v (%T)", err, err)
	}

	after := state.Snapshot()
	if _, assigned := after.Assignments["T1"]; assigned {
		t.Fatalf("cross-tenant assignment must not be committed")
	}
}
