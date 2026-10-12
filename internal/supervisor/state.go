package supervisor

import (
	"context"
	"fmt"
	"sync"
)

// CoreState is HarnessMesh Core's authoritative, in-memory task-graph
// state for Phase 2A. It is the single place that ever mutates a Task's
// Status, Core's assignment map, and the replan/review counters - the
// Supervisor never has a reference to it and cannot mutate it directly.
//
// All mutation goes through apply, which is atomic: it builds the next
// state entirely in a working copy, calls the optional persist hook, and
// only swaps the working copy in as the new authoritative state if every
// step succeeded. Any failure along the way leaves the previously
// committed state completely untouched - no partially applied batch can
// ever be observed.
type CoreState struct {
	mu           sync.Mutex
	graph        TaskGraph
	assignments  map[TaskID]AgentID
	replanCount  int
	reviewRounds int
	// persist simulates a durability boundary (e.g. a database write). A
	// nil persist is a no-op. Tests inject a failing persist to prove
	// atomicity under a "failed atomic apply" scenario.
	persist func(TaskGraph) error
}

// NewCoreState constructs a CoreState seeded with the given initial graph.
// persist may be nil.
func NewCoreState(initial TaskGraph, persist func(TaskGraph) error) *CoreState {
	assignments := make(map[TaskID]AgentID)
	return &CoreState{graph: cloneGraph(initial), assignments: assignments, persist: persist}
}

// Snapshot returns a minimized, defensively-copied ExecutionState - safe
// to hand to a Supervisor (untrusted) or an Authorizer without risking a
// caller mutating Core's real state through aliasing.
func (s *CoreState) Snapshot() ExecutionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// snapshotLocked is Snapshot's body without acquiring the lock - callers
// must already hold s.mu. Exists so applyGated can read a fresh,
// consistent ExecutionState from inside its own critical section without
// deadlocking on s.mu (sync.Mutex is not reentrant).
func (s *CoreState) snapshotLocked() ExecutionState {
	assignments := make(map[TaskID]AgentID, len(s.assignments))
	for k, v := range s.assignments {
		assignments[k] = v
	}
	return ExecutionState{
		Tasks:        cloneGraph(s.graph),
		Assignments:  assignments,
		ReplanCount:  s.replanCount,
		ReviewRounds: s.reviewRounds,
	}
}

// applyGated runs validate -> authorize -> mutate -> persist -> commit as
// ONE atomic unit under CoreState's lock, against state read fresh inside
// the same critical section. This closes a TOCTOU window that existed
// when validation/authorization ran against a snapshot taken before the
// lock was acquired: under concurrent Apply calls, that snapshot could be
// stale by the time the mutation actually committed (e.g. two concurrent
// batches each independently validating a CreateTaskProposal for the same
// task ID against their own pre-lock snapshot, neither aware of the
// other, both then appending a duplicate). Locking across validate and
// authorize means Authorizer implementations must be fast, non-blocking,
// in-memory checks - this package's Authorizer implementations are; a
// future Authorizer backed by a remote policy service would need its own
// bounded, cached fast path rather than blocking here (see
// docs/decisions/0002, "Residual risks").
//
// On any failure, the previously committed state is returned completely
// unchanged - no partially applied batch, and no partially-authorized
// batch, can ever be observed by a concurrent caller.
func (s *CoreState) applyGated(ctx context.Context, v *Validator, authz Authorizer, proposals []AgentProposal) (ExecutionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := s.snapshotLocked()

	if err := v.ValidateBatch(current, proposals); err != nil {
		return current, err
	}
	for i, p := range proposals {
		if err := authz.Authorize(ctx, current, p); err != nil {
			return current, fmt.Errorf("proposal %d: %w", i, err)
		}
	}

	work := cloneGraph(s.graph)
	assignments := make(map[TaskID]AgentID, len(s.assignments))
	for k, v := range s.assignments {
		assignments[k] = v
	}
	replanCount := s.replanCount
	reviewRounds := s.reviewRounds

	for _, p := range proposals {
		switch prop := p.(type) {
		case CreateTaskProposal:
			if indexOf(work.Tasks, prop.Task.ID) >= 0 {
				return current, &StateTransitionFailure{TaskID: prop.Task.ID, Reason: "task already exists at apply time (created concurrently by another batch)"}
			}
			t := prop.Task
			if t.Status == "" {
				t.Status = TaskStatusPending
			}
			work.Tasks = append(work.Tasks, t)

		case AssignTaskProposal:
			idx := indexOf(work.Tasks, prop.TaskID)
			if idx < 0 {
				return current, &StateTransitionFailure{TaskID: prop.TaskID, Reason: "task not found at apply time"}
			}
			cur := work.Tasks[idx].Status
			if cur != "" && cur != TaskStatusPending {
				return current, &StateTransitionFailure{TaskID: prop.TaskID, Reason: fmt.Sprintf("cannot assign a task in status %q", cur)}
			}
			work.Tasks[idx].Status = TaskStatusAssigned
			assignments[prop.TaskID] = prop.CandidateAgentID

		case RetryTaskProposal:
			idx := indexOf(work.Tasks, prop.TaskID)
			if idx < 0 {
				return current, &StateTransitionFailure{TaskID: prop.TaskID, Reason: "task not found at apply time"}
			}
			if work.Tasks[idx].Status != TaskStatusFailed {
				return current, &StateTransitionFailure{TaskID: prop.TaskID, Reason: fmt.Sprintf("retry requires status %q, got %q", TaskStatusFailed, work.Tasks[idx].Status)}
			}
			work.Tasks[idx].Status = TaskStatusPending
			delete(assignments, prop.TaskID)

		case ReplanProposal:
			for _, nt := range prop.NewTasks {
				if indexOf(work.Tasks, nt.ID) >= 0 {
					return current, &StateTransitionFailure{TaskID: nt.ID, Reason: "task already exists at apply time (created concurrently by another batch)"}
				}
				if nt.Status == "" {
					nt.Status = TaskStatusPending
				}
				work.Tasks = append(work.Tasks, nt)
			}
			for _, ut := range prop.UpdatedTasks {
				idx := indexOf(work.Tasks, ut.ID)
				if idx < 0 {
					return current, &StateTransitionFailure{TaskID: ut.ID, Reason: "updated task not found at apply time"}
				}
				ut.Status = work.Tasks[idx].Status
				work.Tasks[idx] = ut
			}
			replanCount++

		case RequestReviewProposal:
			reviewRounds++

		case RequestEvidenceProposal, SynthesizeProposal, EscalateProposal:
			// No task-graph state transition in Phase 2A/2B; these are
			// observed/audited but do not mutate Status/assignments.

		default:
			return current, &StateTransitionFailure{Reason: fmt.Sprintf("unhandled proposal type %T at apply time", p)}
		}
	}

	if s.persist != nil {
		if err := s.persist(work); err != nil {
			return current, &StateTransitionFailure{Reason: fmt.Sprintf("persistence failed, state unchanged: %v", err)}
		}
	}

	s.graph = work
	s.assignments = assignments
	s.replanCount = replanCount
	s.reviewRounds = reviewRounds
	return s.snapshotLocked(), nil
}

// MarkExecuting / MarkCompleted / MarkFailed record execution-boundary
// outcomes. Like apply, each is atomic with respect to the rest of
// CoreState: either the single task's status transitions, or an error is
// returned and nothing changes.
func (s *CoreState) MarkExecuting(taskID TaskID) error {
	return s.transition(taskID, TaskStatusAssigned, TaskStatusExecuting)
}

func (s *CoreState) MarkCompleted(taskID TaskID) error {
	return s.transition(taskID, TaskStatusExecuting, TaskStatusCompleted)
}

func (s *CoreState) MarkFailed(taskID TaskID) error {
	return s.transition(taskID, TaskStatusExecuting, TaskStatusFailed)
}

func (s *CoreState) transition(taskID TaskID, from, to TaskStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := indexOf(s.graph.Tasks, taskID)
	if idx < 0 {
		return &StateTransitionFailure{TaskID: taskID, Reason: "task not found"}
	}
	if s.graph.Tasks[idx].Status != from {
		return &StateTransitionFailure{TaskID: taskID, Reason: fmt.Sprintf("expected status %q, got %q", from, s.graph.Tasks[idx].Status)}
	}
	s.graph.Tasks[idx].Status = to
	return nil
}

func indexOf(tasks []Task, id TaskID) int {
	for i, t := range tasks {
		if t.ID == id {
			return i
		}
	}
	return -1
}

func cloneGraph(g TaskGraph) TaskGraph {
	out := TaskGraph{Tasks: make([]Task, len(g.Tasks))}
	for i, t := range g.Tasks {
		ct := t
		ct.RequiredCapabilities = append([]string(nil), t.RequiredCapabilities...)
		ct.Dependencies = append([]TaskID(nil), t.Dependencies...)
		out.Tasks[i] = ct
	}
	return out
}

// Applier is the bounded proposal-application layer: it runs validation,
// then authorization, then the atomic state transition, strictly in that
// order, and strictly separately. A successfully validated batch that
// fails authorization mutates nothing; a successfully authorized batch
// that fails to apply atomically also mutates nothing.
type Applier struct {
	Validator  *Validator
	Authorizer Authorizer
	State      *CoreState
}

// NewApplier constructs an Applier with explicit, required dependencies.
func NewApplier(validator *Validator, authorizer Authorizer, state *CoreState) *Applier {
	return &Applier{Validator: validator, Authorizer: authorizer, State: state}
}

// Apply runs the full validate -> authorize -> apply pipeline for a
// proposal batch, atomically with respect to any concurrent Apply call
// (see CoreState.applyGated). It returns the (possibly unchanged)
// ExecutionState and, on any failure, a typed error. Nothing is ever
// partially applied: this method either returns a state reflecting every
// proposal in the batch, or the state exactly as it was before the call.
func (a *Applier) Apply(ctx context.Context, proposals []AgentProposal) (ExecutionState, error) {
	return a.State.applyGated(ctx, a.Validator, a.Authorizer, proposals)
}
