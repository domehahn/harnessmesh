package supervisor

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeDirectory is a minimal, explicit AgentDirectory (and, optionally,
// TenantDirectory) for tests - it never trusts anything from a proposal,
// only what's registered here. It is safe for concurrent mutation so tests
// can simulate TOCTOU scenarios (an agent disappearing, losing a
// capability, or changing tenant between validation and execution).
type fakeDirectory struct {
	mu      sync.Mutex
	agents  map[AgentID][]string
	tenants map[AgentID]string
}

func newFakeDirectory(agents map[AgentID][]string) *fakeDirectory {
	return &fakeDirectory{agents: agents, tenants: map[AgentID]string{}}
}

func newFakeDirectoryWithTenants(agents map[AgentID][]string, tenants map[AgentID]string) *fakeDirectory {
	return &fakeDirectory{agents: agents, tenants: tenants}
}

func (f *fakeDirectory) AgentExists(id AgentID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.agents[id]
	return ok
}

func (f *fakeDirectory) Capabilities(id AgentID) ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	caps, ok := f.agents[id]
	return caps, ok
}

func (f *fakeDirectory) TenantOf(id AgentID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tenants[id]
	return t, ok
}

func (f *fakeDirectory) ListAgents(ctx context.Context) ([]AgentContextSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]AgentContextSummary, 0, len(f.agents))
	for id, caps := range f.agents {
		out = append(out, AgentContextSummary{AgentID: id, Capabilities: append([]string(nil), caps...)})
	}
	return out, nil
}

var _ AgentCatalog = (*fakeDirectory)(nil)

// removeAgent simulates an agent disappearing from the live registry after
// a proposal referencing it was already validated (TOCTOU).
func (f *fakeDirectory) removeAgent(id AgentID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.agents, id)
}

// setCapabilities simulates an agent's declared capabilities changing
// after a proposal referencing it was already validated (TOCTOU).
func (f *fakeDirectory) setCapabilities(id AgentID, caps []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents[id] = caps
}

// setTenant simulates an agent's tenant changing after a proposal
// referencing it was already validated/authorized (TOCTOU).
func (f *fakeDirectory) setTenant(id AgentID, tenant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tenants[id] = tenant
}

// bareDirectory implements AgentDirectory but deliberately NOT
// TenantDirectory - it stands in for a real directory (like
// collaboration.SupervisorAgentDirectory today) that has no tenant model
// at all, so tests can prove tenant-scoped tasks fail closed against it
// rather than silently being treated as unrestricted.
type bareDirectory struct {
	agents map[AgentID][]string
}

func (b *bareDirectory) AgentExists(id AgentID) bool {
	_, ok := b.agents[id]
	return ok
}

func (b *bareDirectory) Capabilities(id AgentID) ([]string, bool) {
	caps, ok := b.agents[id]
	return caps, ok
}

func testValidator(agents map[AgentID][]string, limits SupervisorLimits) *Validator {
	return NewValidator(newFakeDirectory(agents), nil, limits)
}

func TestValidateBatch_ValidAssignment(t *testing.T) {
	v := testValidator(map[AgentID][]string{
		"codex": {"code_modification", "test_execution"},
	}, DefaultSupervisorLimits())
	v.AllowedCapabilities["code_modification"] = true
	v.AllowedCapabilities["test_execution"] = true

	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{{ID: "T1"}}}}
	proposals := []AgentProposal{
		AssignTaskProposal{
			TaskID:           "T1",
			CandidateAgentID: "codex",
			RequiredCaps:     []string{"code_modification", "test_execution"},
			RationaleSummary: "codex has required capabilities",
		},
	}

	if err := v.ValidateBatch(state, proposals); err != nil {
		t.Fatalf("expected valid assignment to pass, got: %v", err)
	}
}

func TestValidateBatch_UnknownAgent(t *testing.T) {
	v := testValidator(map[AgentID][]string{"codex": {"review"}}, DefaultSupervisorLimits())
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{{ID: "T1"}}}}
	err := v.ValidateBatch(state, []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "ghost-agent", RequiredCaps: nil, RationaleSummary: "r"},
	})
	var want *UnknownAgentError
	if !errors.As(err, &want) {
		t.Fatalf("expected UnknownAgentError, got %v (%T)", err, err)
	}
}

func TestValidateBatch_UnknownTask(t *testing.T) {
	v := testValidator(map[AgentID][]string{"codex": {"review"}}, DefaultSupervisorLimits())
	state := ExecutionState{Tasks: TaskGraph{}}
	err := v.ValidateBatch(state, []AgentProposal{
		AssignTaskProposal{TaskID: "ghost-task", CandidateAgentID: "codex", RationaleSummary: "r"},
	})
	var want *UnknownTaskError
	if !errors.As(err, &want) {
		t.Fatalf("expected UnknownTaskError, got %v (%T)", err, err)
	}
}

func TestValidateBatch_InvalidCapability(t *testing.T) {
	v := testValidator(map[AgentID][]string{"codex": {"review"}}, DefaultSupervisorLimits())
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{{ID: "T1"}}}}
	err := v.ValidateBatch(state, []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RequiredCaps: []string{"nonexistent_capability"}, RationaleSummary: "r"},
	})
	var want *UnknownCapabilityError
	if !errors.As(err, &want) {
		t.Fatalf("expected UnknownCapabilityError, got %v (%T)", err, err)
	}
}

func TestValidateBatch_CapabilityMismatch(t *testing.T) {
	v := testValidator(map[AgentID][]string{"codex": {"review"}}, DefaultSupervisorLimits())
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{{ID: "T1"}}}}
	err := v.ValidateBatch(state, []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RequiredCaps: []string{"write_repository"}, RationaleSummary: "r"},
	})
	var want *CapabilityMismatchError
	if !errors.As(err, &want) {
		t.Fatalf("expected CapabilityMismatchError (codex lacks write_repository), got %v (%T)", err, err)
	}
}

func TestValidateBatch_MalformedProposal(t *testing.T) {
	v := testValidator(map[AgentID][]string{"codex": {"review"}}, DefaultSupervisorLimits())
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{{ID: "T1"}}}}
	err := v.ValidateBatch(state, []AgentProposal{
		AssignTaskProposal{TaskID: "", CandidateAgentID: "", RationaleSummary: "r"},
	})
	var want *ProposalValidationFailure
	if !errors.As(err, &want) {
		t.Fatalf("expected ProposalValidationFailure for empty task/agent id, got %v (%T)", err, err)
	}
}

func TestValidateBatch_NilProposalRejectedClosed(t *testing.T) {
	v := testValidator(map[AgentID][]string{}, DefaultSupervisorLimits())
	err := v.ValidateBatch(ExecutionState{}, []AgentProposal{nil})
	var want *ProposalValidationFailure
	if !errors.As(err, &want) {
		t.Fatalf("expected ProposalValidationFailure for nil proposal, got %v (%T)", err, err)
	}
}

func TestValidateBatch_DependencyCycle(t *testing.T) {
	v := testValidator(map[AgentID][]string{}, DefaultSupervisorLimits())
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{
		{ID: "A", Dependencies: []TaskID{"B"}},
		{ID: "B", Dependencies: []TaskID{"A"}},
	}}}
	err := v.ValidateBatch(state, nil)
	var want *DependencyCycleError
	if !errors.As(err, &want) {
		t.Fatalf("expected DependencyCycleError, got %v (%T)", err, err)
	}
}

func TestValidateBatch_CreateTaskForwardReferenceWithinBatch(t *testing.T) {
	v := testValidator(map[AgentID][]string{"codex": {"review"}}, DefaultSupervisorLimits())
	state := ExecutionState{Tasks: TaskGraph{}}
	err := v.ValidateBatch(state, []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "first"},
		CreateTaskProposal{Task: Task{ID: "T2", Dependencies: []TaskID{"T1"}}, RationaleSummary: "second"},
		AssignTaskProposal{TaskID: "T2", CandidateAgentID: "codex", RationaleSummary: "r"},
	})
	if err != nil {
		t.Fatalf("expected forward-reference-within-batch to validate, got: %v", err)
	}
}

func TestValidateBatch_CreateTaskDuplicateRejected(t *testing.T) {
	v := testValidator(map[AgentID][]string{}, DefaultSupervisorLimits())
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{{ID: "T1"}}}}
	err := v.ValidateBatch(state, []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "dup"},
	})
	var want *ProposalValidationFailure
	if !errors.As(err, &want) {
		t.Fatalf("expected ProposalValidationFailure for duplicate task id, got %v (%T)", err, err)
	}
}

func TestValidateBatch_GuardrailMaxAgentFanout(t *testing.T) {
	limits := DefaultSupervisorLimits()
	limits.MaxAgentFanout = 1
	v := testValidator(map[AgentID][]string{"a1": {}, "a2": {}}, limits)
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{{ID: "T1"}, {ID: "T2"}}}}
	err := v.ValidateBatch(state, []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "a1", RationaleSummary: "r"},
		AssignTaskProposal{TaskID: "T2", CandidateAgentID: "a2", RationaleSummary: "r"},
	})
	var want *GuardrailExceededError
	if !errors.As(err, &want) {
		t.Fatalf("expected GuardrailExceededError (max_agent_fanout), got %v (%T)", err, err)
	}
	if want.Guardrail != "max_agent_fanout" {
		t.Fatalf("expected max_agent_fanout guardrail, got %q", want.Guardrail)
	}
}

func TestValidateBatch_GuardrailMaxTaskDepth(t *testing.T) {
	limits := DefaultSupervisorLimits()
	limits.MaxTaskDepth = 2
	v := testValidator(map[AgentID][]string{}, limits)
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{
		{ID: "A"},
		{ID: "B", Dependencies: []TaskID{"A"}},
		{ID: "C", Dependencies: []TaskID{"B"}},
		{ID: "D", Dependencies: []TaskID{"C"}},
	}}}
	err := v.ValidateBatch(state, nil)
	var want *GuardrailExceededError
	if !errors.As(err, &want) {
		t.Fatalf("expected GuardrailExceededError (max_task_depth), got %v (%T)", err, err)
	}
	if want.Guardrail != "max_task_depth" {
		t.Fatalf("expected max_task_depth guardrail, got %q", want.Guardrail)
	}
}

func TestValidateBatch_ReplanLimitExceeded(t *testing.T) {
	limits := DefaultSupervisorLimits()
	limits.MaxReplanCount = 1
	v := testValidator(map[AgentID][]string{}, limits)
	state := ExecutionState{ReplanCount: 1, Tasks: TaskGraph{}}
	err := v.ValidateBatch(state, []AgentProposal{
		ReplanProposal{RationaleSummary: "one more replan"},
	})
	var want *ReplanLimitExceeded
	if !errors.As(err, &want) {
		t.Fatalf("expected ReplanLimitExceeded, got %v (%T)", err, err)
	}
}

func TestValidateBatch_ReviewRoundsLimitExceeded(t *testing.T) {
	limits := DefaultSupervisorLimits()
	limits.MaxReviewRounds = 1
	v := testValidator(map[AgentID][]string{"reviewer": {}}, limits)
	state := ExecutionState{ReviewRounds: 1, Tasks: TaskGraph{Tasks: []Task{{ID: "T1"}}}}
	err := v.ValidateBatch(state, []AgentProposal{
		RequestReviewProposal{TaskID: "T1", ReviewerAgentID: "reviewer", RationaleSummary: "r"},
	})
	var want *GuardrailExceededError
	if !errors.As(err, &want) {
		t.Fatalf("expected GuardrailExceededError (max_review_rounds), got %v (%T)", err, err)
	}
}

func TestValidateBatch_RationaleTooLong(t *testing.T) {
	v := testValidator(map[AgentID][]string{"codex": {}}, DefaultSupervisorLimits())
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{{ID: "T1"}}}}
	long := make([]byte, MaxRationaleLength+1)
	for i := range long {
		long[i] = 'x'
	}
	err := v.ValidateBatch(state, []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RationaleSummary: RationaleSummary(long)},
	})
	var want *RationaleTooLongError
	if !errors.As(err, &want) {
		t.Fatalf("expected RationaleTooLongError, got %v (%T)", err, err)
	}
}

func TestValidateBatch_PolicyDowngradeForbidden(t *testing.T) {
	v := testValidator(map[AgentID][]string{}, DefaultSupervisorLimits())
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{
		{ID: "T1", RequiredCapabilities: []string{"review", "run_commands"}, Locked: true},
	}}}
	err := v.ValidateBatch(state, []AgentProposal{
		ReplanProposal{
			UpdatedTasks: []Task{
				{ID: "T1", RequiredCapabilities: []string{"review"}}, // drops run_commands
			},
			RationaleSummary: "weaken requirement",
		},
	})
	var want *PolicyDowngradeForbiddenError
	if !errors.As(err, &want) {
		t.Fatalf("expected PolicyDowngradeForbiddenError, got %v (%T)", err, err)
	}
}

func TestValidateBatch_PolicyUpgradeAllowed(t *testing.T) {
	v := testValidator(map[AgentID][]string{}, DefaultSupervisorLimits())
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{
		{ID: "T1", RequiredCapabilities: []string{"review"}, Locked: true},
	}}}
	err := v.ValidateBatch(state, []AgentProposal{
		ReplanProposal{
			UpdatedTasks: []Task{
				{ID: "T1", RequiredCapabilities: []string{"review", "run_commands"}}, // adds, doesn't remove
			},
			RationaleSummary: "strengthen requirement",
		},
	})
	if err != nil {
		t.Fatalf("expected capability superset (upgrade) to be allowed, got: %v", err)
	}
}

func TestValidateBatch_UnauthorizedProposalEmptyBatchOnAgentMismatch(t *testing.T) {
	// "Unauthorized proposal" in the sense that a batch referencing a mix of
	// one valid and one invalid proposal must reject the whole batch - no
	// partial application.
	v := testValidator(map[AgentID][]string{"codex": {"review"}}, DefaultSupervisorLimits())
	v.AllowedCapabilities["review"] = true
	state := ExecutionState{Tasks: TaskGraph{Tasks: []Task{{ID: "T1"}, {ID: "T2"}}}}
	err := v.ValidateBatch(state, []AgentProposal{
		AssignTaskProposal{TaskID: "T1", CandidateAgentID: "codex", RequiredCaps: []string{"review"}, RationaleSummary: "ok"},
		AssignTaskProposal{TaskID: "T2", CandidateAgentID: "ghost", RationaleSummary: "not ok"},
	})
	if err == nil {
		t.Fatalf("expected the whole batch to be rejected when any proposal is invalid")
	}
}
