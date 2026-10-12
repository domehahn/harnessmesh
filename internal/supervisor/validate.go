package supervisor

import (
	"context"
	"fmt"
)

// AgentDirectory resolves agent identity and declared capabilities. The
// validator never trusts a Supervisor's claim that an agent exists or holds
// a capability - it always asks the directory, which in later phases is
// backed by collaboration.Engine's live registry / config.AgentCapabilities.
// Phase 1 defines only this interface and a fake implementation for tests,
// keeping the validation boundary independent of any concrete registry.
type AgentDirectory interface {
	// AgentExists reports whether id is a known, live agent.
	AgentExists(id AgentID) bool
	// Capabilities returns the capabilities id has actually declared, and
	// whether id is known at all.
	Capabilities(id AgentID) ([]string, bool)
}

// AgentCatalog extends AgentDirectory with enumeration, needed for
// capability-based agent ROUTING (Phase 3's Scheduler) rather than mere
// point-wise existence/capability re-checks (Phase 2A's TOCTOU
// revalidation, which only ever needs to ask about one already-known
// agent ID). Introduced as a separate interface, not a change to
// AgentDirectory, so every existing Validator/Authorizer/Executor caller
// - which never needs to list agents - is unaffected.
type AgentCatalog interface {
	AgentDirectory
	// ListAgents returns every known agent and its declared capabilities,
	// for deterministic capability-based routing. Order is not
	// significant - Scheduler sorts deterministically itself.
	ListAgents(ctx context.Context) ([]AgentContextSummary, error)
}

// DefaultKnownCapabilities is the closed vocabulary of capability names the
// validator accepts, mirroring config.AgentCapabilities' JSON field names
// plus the two supervisor-specific capabilities named in ADR 0002. Callers
// may supply a different set via Validator.AllowedCapabilities.
func DefaultKnownCapabilities() map[string]bool {
	return map[string]bool{
		"read_repository":    true,
		"write_repository":   true,
		"run_commands":       true,
		"review":             true,
		"answer_questions":   true,
		"submit_evidence":    true,
		"propose_plan":       true,
		"propose_task_graph": true,
	}
}

// Validator is HarnessMesh Core's fail-closed validation boundary for
// Supervisor proposals. It performs no I/O and calls no LLM; every check is
// deterministic and reproducible.
type Validator struct {
	Agents              AgentDirectory
	AllowedCapabilities map[string]bool
	Limits              SupervisorLimits
}

// NewValidator constructs a Validator with explicit, non-nil dependencies.
// A nil AllowedCapabilities map defaults to DefaultKnownCapabilities(); a
// zero-value Limits is never silently treated as "unlimited" - callers must
// pass DefaultSupervisorLimits() or their own explicit limits.
func NewValidator(agents AgentDirectory, allowedCapabilities map[string]bool, limits SupervisorLimits) *Validator {
	if allowedCapabilities == nil {
		allowedCapabilities = DefaultKnownCapabilities()
	}
	return &Validator{Agents: agents, AllowedCapabilities: allowedCapabilities, Limits: limits}
}

// ValidateBatch validates an entire proposal batch against the current
// ExecutionState. It is all-or-nothing: on the first invalid proposal it
// returns that error and applies nothing, so Core never partially executes
// a rejected batch.
func (v *Validator) ValidateBatch(state ExecutionState, proposals []AgentProposal) error {
	knownTasks := map[TaskID]Task{}
	for _, t := range state.Tasks.Tasks {
		knownTasks[t.ID] = t
	}

	fanout := map[AgentID]bool{}
	replanCount := 0
	reviewCount := 0

	for i, p := range proposals {
		if p == nil {
			return &ProposalValidationFailure{ProposalIndex: i, Reason: "nil proposal"}
		}
		if !knownProposalTypes[p.ProposalType()] {
			return &ProposalValidationFailure{ProposalIndex: i, Reason: fmt.Sprintf("unknown proposal type %q", p.ProposalType())}
		}

		switch prop := p.(type) {
		case CreateTaskProposal:
			if err := v.validateRationale(i, prop.RationaleSummary); err != nil {
				return err
			}
			if err := v.validateNewTask(i, prop.Task, knownTasks); err != nil {
				return err
			}
			knownTasks[prop.Task.ID] = prop.Task

		case AssignTaskProposal:
			if err := v.validateRationale(i, prop.RationaleSummary); err != nil {
				return err
			}
			if prop.TaskID == "" || prop.CandidateAgentID == "" {
				return &ProposalValidationFailure{ProposalIndex: i, Reason: "assign_task requires task_id and candidate_agent_id"}
			}
			if _, ok := knownTasks[prop.TaskID]; !ok {
				return &UnknownTaskError{TaskID: prop.TaskID}
			}
			if prop.CandidateAgentID == SupervisorAgentID {
				return &SelfAssignmentForbiddenError{AgentID: prop.CandidateAgentID, TaskID: prop.TaskID, Reason: "supervisor cannot assign itself as an executor"}
			}
			if !v.Agents.AgentExists(prop.CandidateAgentID) {
				return &UnknownAgentError{AgentID: prop.CandidateAgentID}
			}
			for _, c := range prop.RequiredCaps {
				if !v.AllowedCapabilities[c] {
					return &UnknownCapabilityError{Capability: c}
				}
			}
			declared, _ := v.Agents.Capabilities(prop.CandidateAgentID)
			if missing := MissingCapabilities(declared, prop.RequiredCaps); len(missing) > 0 {
				return &CapabilityMismatchError{AgentID: prop.CandidateAgentID, Missing: missing}
			}
			fanout[prop.CandidateAgentID] = true

		case RequestReviewProposal:
			if err := v.validateRationale(i, prop.RationaleSummary); err != nil {
				return err
			}
			if prop.TaskID == "" || prop.ReviewerAgentID == "" {
				return &ProposalValidationFailure{ProposalIndex: i, Reason: "request_review requires task_id and reviewer_agent_id"}
			}
			if _, ok := knownTasks[prop.TaskID]; !ok {
				return &UnknownTaskError{TaskID: prop.TaskID}
			}
			if prop.ReviewerAgentID == SupervisorAgentID {
				return &SelfAssignmentForbiddenError{AgentID: prop.ReviewerAgentID, TaskID: prop.TaskID, Reason: "supervisor cannot review as itself"}
			}
			if !v.Agents.AgentExists(prop.ReviewerAgentID) {
				return &UnknownAgentError{AgentID: prop.ReviewerAgentID}
			}
			if executor, ok := state.Assignments[prop.TaskID]; ok && executor == prop.ReviewerAgentID {
				return &SelfAssignmentForbiddenError{AgentID: prop.ReviewerAgentID, TaskID: prop.TaskID, Reason: "reviewer cannot be the task's own executor (self-review forbidden)"}
			}
			reviewCount++

		case RequestEvidenceProposal:
			if err := v.validateRationale(i, prop.RationaleSummary); err != nil {
				return err
			}
			if prop.TaskID == "" || prop.FromAgentID == "" {
				return &ProposalValidationFailure{ProposalIndex: i, Reason: "request_evidence requires task_id and from_agent_id"}
			}
			if _, ok := knownTasks[prop.TaskID]; !ok {
				return &UnknownTaskError{TaskID: prop.TaskID}
			}
			if !v.Agents.AgentExists(prop.FromAgentID) {
				return &UnknownAgentError{AgentID: prop.FromAgentID}
			}

		case RetryTaskProposal:
			if err := v.validateRationale(i, prop.RationaleSummary); err != nil {
				return err
			}
			if prop.TaskID == "" {
				return &ProposalValidationFailure{ProposalIndex: i, Reason: "retry_task requires task_id"}
			}
			if _, ok := knownTasks[prop.TaskID]; !ok {
				return &UnknownTaskError{TaskID: prop.TaskID}
			}

		case ReplanProposal:
			if err := v.validateRationale(i, prop.RationaleSummary); err != nil {
				return err
			}
			replanCount++
			for _, nt := range prop.NewTasks {
				if err := v.validateNewTask(i, nt, knownTasks); err != nil {
					return err
				}
				knownTasks[nt.ID] = nt
			}
			for _, ut := range prop.UpdatedTasks {
				existing, ok := knownTasks[ut.ID]
				if !ok {
					return &UnknownTaskError{TaskID: ut.ID}
				}
				for _, c := range ut.RequiredCapabilities {
					if !v.AllowedCapabilities[c] {
						return &UnknownCapabilityError{Capability: c}
					}
				}
				if existing.Locked {
					if !isSuperset(ut.RequiredCapabilities, existing.RequiredCapabilities) {
						return &PolicyDowngradeForbiddenError{
							TaskID: string(ut.ID),
							Reason: "replan may not remove a required capability from a locked task",
						}
					}
				}
				knownTasks[ut.ID] = ut
			}

		case SynthesizeProposal:
			if err := v.validateRationale(i, prop.RationaleSummary); err != nil {
				return err
			}
			for _, id := range prop.TaskIDs {
				if _, ok := knownTasks[id]; !ok {
					return &UnknownTaskError{TaskID: id}
				}
			}

		case EscalateProposal:
			if err := v.validateRationale(i, prop.RationaleSummary); err != nil {
				return err
			}
			if prop.TaskID == "" {
				return &ProposalValidationFailure{ProposalIndex: i, Reason: "escalate requires task_id"}
			}
			if _, ok := knownTasks[prop.TaskID]; !ok {
				return &UnknownTaskError{TaskID: prop.TaskID}
			}

		default:
			return &ProposalValidationFailure{ProposalIndex: i, Reason: fmt.Sprintf("unhandled proposal concrete type %T", prop)}
		}
	}

	if err := v.checkGuardrails(state, knownTasks, fanout, replanCount, reviewCount); err != nil {
		return err
	}
	if cycle := findCycle(knownTasks); cycle != nil {
		return &DependencyCycleError{Cycle: cycle}
	}
	return nil
}

func (v *Validator) validateRationale(index int, r RationaleSummary) error {
	if len(r) > MaxRationaleLength {
		return &RationaleTooLongError{Length: len(r), Max: MaxRationaleLength}
	}
	return nil
}

func (v *Validator) validateNewTask(index int, t Task, knownTasks map[TaskID]Task) error {
	if t.ID == "" {
		return &ProposalValidationFailure{ProposalIndex: index, Reason: "task id must not be empty"}
	}
	if _, exists := knownTasks[t.ID]; exists {
		return &ProposalValidationFailure{ProposalIndex: index, Reason: fmt.Sprintf("task %q already exists", t.ID)}
	}
	for _, c := range t.RequiredCapabilities {
		if !v.AllowedCapabilities[c] {
			return &UnknownCapabilityError{Capability: c}
		}
	}
	for _, dep := range t.Dependencies {
		if _, ok := knownTasks[dep]; !ok {
			return &UnknownTaskError{TaskID: dep}
		}
	}
	if v.Limits.MaxCost > 0 && t.EstimatedCost > v.Limits.MaxCost {
		return &GuardrailExceededError{Guardrail: "max_cost", Limit: int(v.Limits.MaxCost), Actual: int(t.EstimatedCost)}
	}
	return nil
}

func (v *Validator) checkGuardrails(state ExecutionState, knownTasks map[TaskID]Task, fanout map[AgentID]bool, replanCount, reviewCount int) error {
	if v.Limits.MaxAgentFanout > 0 && len(fanout) > v.Limits.MaxAgentFanout {
		return &GuardrailExceededError{Guardrail: "max_agent_fanout", Limit: v.Limits.MaxAgentFanout, Actual: len(fanout)}
	}
	if replanCount > 0 {
		total := state.ReplanCount + replanCount
		if v.Limits.MaxReplanCount > 0 && total > v.Limits.MaxReplanCount {
			return &ReplanLimitExceeded{Limit: v.Limits.MaxReplanCount, Actual: total}
		}
	}
	if reviewCount > 0 {
		total := state.ReviewRounds + reviewCount
		if v.Limits.MaxReviewRounds > 0 && total > v.Limits.MaxReviewRounds {
			return &GuardrailExceededError{Guardrail: "max_review_rounds", Limit: v.Limits.MaxReviewRounds, Actual: total}
		}
	}
	if v.Limits.MaxTaskDepth > 0 {
		depth := graphDepth(knownTasks)
		if depth > v.Limits.MaxTaskDepth {
			return &GuardrailExceededError{Guardrail: "max_task_depth", Limit: v.Limits.MaxTaskDepth, Actual: depth}
		}
	}
	return nil
}

func MissingCapabilities(declared, required []string) []string {
	have := map[string]bool{}
	for _, d := range declared {
		have[d] = true
	}
	var missing []string
	for _, r := range required {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

func isSuperset(candidate, required []string) bool {
	have := map[string]bool{}
	for _, c := range candidate {
		have[c] = true
	}
	for _, r := range required {
		if !have[r] {
			return false
		}
	}
	return true
}

// findCycle runs a DFS over the dependency graph and returns the cycle path
// if one exists, or nil if the graph is acyclic.
func findCycle(tasks map[TaskID]Task) []TaskID {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := map[TaskID]int{}
	var path []TaskID

	var visit func(id TaskID) []TaskID
	visit = func(id TaskID) []TaskID {
		switch state[id] {
		case visiting:
			// Found the cycle: return the path from id's first occurrence.
			cycleStart := 0
			for idx, p := range path {
				if p == id {
					cycleStart = idx
					break
				}
			}
			cycle := append([]TaskID{}, path[cycleStart:]...)
			cycle = append(cycle, id)
			return cycle
		case done:
			return nil
		}
		state[id] = visiting
		path = append(path, id)
		if t, ok := tasks[id]; ok {
			for _, dep := range t.Dependencies {
				if cycle := visit(dep); cycle != nil {
					return cycle
				}
			}
		}
		path = path[:len(path)-1]
		state[id] = done
		return nil
	}

	for id := range tasks {
		if state[id] == unvisited {
			if cycle := visit(id); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

// graphDepth returns the longest dependency chain length (in edges) across
// the given task set. Assumes the graph is acyclic; callers should run
// findCycle first if that is not already guaranteed.
func graphDepth(tasks map[TaskID]Task) int {
	memo := map[TaskID]int{}
	var depth func(id TaskID, visiting map[TaskID]bool) int
	depth = func(id TaskID, visiting map[TaskID]bool) int {
		if d, ok := memo[id]; ok {
			return d
		}
		if visiting[id] {
			// Cyclic; bail out to avoid infinite recursion. findCycle is
			// responsible for rejecting cycles before depth matters.
			return 0
		}
		t, ok := tasks[id]
		if !ok || len(t.Dependencies) == 0 {
			memo[id] = 1
			return 1
		}
		visiting[id] = true
		max := 0
		for _, dep := range t.Dependencies {
			if d := depth(dep, visiting); d > max {
				max = d
			}
		}
		delete(visiting, id)
		memo[id] = max + 1
		return max + 1
	}

	best := 0
	for id := range tasks {
		if d := depth(id, map[TaskID]bool{}); d > best {
			best = d
		}
	}
	return best
}
