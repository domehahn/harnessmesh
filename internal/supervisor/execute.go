package supervisor

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ModelUsageRef attributes one route/provider/model combination an
// ExecutionProvider actually used while running a task - populated only
// by the provider itself from real routing/transport context (e.g. a
// ModelRoutingDecision plus the served-model identity an
// InferenceTransport echoed back), never parsed from an agent's own
// output text. Left empty by providers (like FakeExecutionProvider) that
// have no such data to report - an empty slice is honest, not an error.
type ModelUsageRef struct {
	RouteID  string
	Provider string
	Model    string
	Count    int
}

// ExecutionObserver is an optional, Core-owned hook Executor calls after
// it has determined an execution's AUTHORITATIVE outcome - never before,
// and never from anything the agent itself claimed. Its method returns
// no error and must not be allowed to affect Execute's own return value:
// this is the structural guarantee that a performance-observation
// failure can never roll back or alter execution truth (see
// internal/performance's ADR 0006 for the concrete implementation).
// nil is safe - Executor works identically with no observer attached.
type ExecutionObserver interface {
	ObserveExecution(ctx context.Context, task Task, agentID AgentID, executionID ExecutionID, result ExecutionResult, execErr error, startedAt, finishedAt time.Time)
}

// ExecutionObserverFailureSink is an optional, narrow hook Executor
// calls when ExecutionObserver.ObserveExecution itself panics - the
// observer call is already recover()-wrapped so a panic can never affect
// Execute's own return value, but prior to this hook that recovery was
// silent. Only ever receives a bounded classification, never the
// recovered value itself, so a secret or arbitrary payload trapped in a
// panic can never reach telemetry unfiltered.
type ExecutionObserverFailureSink interface {
	RecordExecutionObserverPanic()
}

// Executor is the execution boundary: it routes an already-assigned task
// to its agent and runs it through an ExecutionProvider, enforcing
// MaxExecutionTime and re-validating every authoritative constraint
// against the LIVE AgentDirectory before dispatching - never trusting the
// ExecutionState snapshot that was true at validation/authorization time.
//
// This is the explicit TOCTOU mitigation: an agent can disappear, lose a
// capability, or change tenant between when a proposal was validated and
// when its task actually runs. Executor re-checks all three immediately
// before calling the provider.
//
// It also re-runs Authorizer.Authorize immediately before dispatch.
// Authorization performed at Apply time can become stale by the time a
// task actually executes - an operator may revoke an agent (DeniedAgents)
// or a capability (DeniedCapabilities) after a task was assigned but
// before it runs, especially since execution can happen asynchronously,
// arbitrarily later than the Apply call that produced the assignment.
// Without this re-check, a revoked policy decision would have no effect
// on work already queued, which is exactly the kind of authorization
// staleness this package must not permit.
type Executor struct {
	Directory  AgentDirectory
	Authorizer Authorizer
	Provider   ExecutionProvider
	Limits     SupervisorLimits
	State      *CoreState
	// Observer, if non-nil, is notified of every execution's
	// authoritative outcome - see ExecutionObserver's doc comment for
	// the failure-isolation guarantee this provides.
	Observer ExecutionObserver
	// FailureSink, if non-nil, is notified when Observer itself panics -
	// see ExecutionObserverFailureSink. Wired automatically by
	// WithObserver when the attached observer also implements the sink
	// interface, so most callers never need to set this directly.
	FailureSink ExecutionObserverFailureSink
}

// NewExecutor constructs an Executor with explicit, required dependencies.
// Authorizer must not be nil - pass AllowAllAuthorizer{} explicitly if no
// policy re-check is intended, so that choice is visible at the call site
// rather than implied by a nil default.
func NewExecutor(directory AgentDirectory, authorizer Authorizer, provider ExecutionProvider, limits SupervisorLimits, state *CoreState) *Executor {
	return &Executor{Directory: directory, Authorizer: authorizer, Provider: provider, Limits: limits, State: state}
}

// WithObserver returns a copy of e configured to notify observer of
// every execution's authoritative outcome. If observer also implements
// ExecutionObserverFailureSink, it is automatically wired as the
// FailureSink too - one call configures both roles for the common case
// of a single implementation handling both.
func (e *Executor) WithObserver(observer ExecutionObserver) *Executor {
	out := *e
	out.Observer = observer
	if sink, ok := observer.(ExecutionObserverFailureSink); ok {
		out.FailureSink = sink
	}
	return &out
}

// Execute runs the task currently assigned to taskID. It re-validates
// agent existence, capability possession, and tenant scope against the
// live directory immediately before dispatch (TOCTOU revalidation),
// enforces MaxExecutionTime via context deadline, and records the
// resulting status transition in CoreState.
func (e *Executor) Execute(ctx context.Context, taskID TaskID) (ExecutionResult, error) {
	state := e.State.Snapshot()

	task, ok := state.Tasks.ByID(taskID)
	if !ok {
		return ExecutionResult{}, &StateTransitionFailure{TaskID: taskID, Reason: "unknown task at execution boundary"}
	}
	agentID, assigned := state.Assignments[taskID]
	if !assigned {
		return ExecutionResult{}, &AgentRoutingFailure{TaskID: taskID, Reason: "no agent assigned"}
	}

	// --- TOCTOU revalidation against the LIVE directory, not the snapshot ---
	if !e.Directory.AgentExists(agentID) {
		return ExecutionResult{}, &AgentRoutingFailure{
			AgentID: agentID, TaskID: taskID,
			Reason: "agent no longer registered (removed after assignment)",
		}
	}
	declared, _ := e.Directory.Capabilities(agentID)
	if missing := MissingCapabilities(declared, task.RequiredCapabilities); len(missing) > 0 {
		return ExecutionResult{}, &AuthorizationFailure{
			AgentID: agentID, TaskID: taskID,
			Reason: fmt.Sprintf("required capability revoked before execution: %v", missing),
		}
	}
	if task.Tenant != "" {
		// Fail closed if tenancy cannot even be resolved - see the
		// identical rationale in CoreAuthorizer.Authorize (authorize.go).
		td, ok := e.Directory.(TenantDirectory)
		if !ok {
			return ExecutionResult{}, &AuthorizationFailure{
				AgentID: agentID, TaskID: taskID,
				Reason: fmt.Sprintf("task requires tenant %q but no TenantDirectory is configured - failing closed", task.Tenant),
			}
		}
		tenant, known := td.TenantOf(agentID)
		if !known || tenant != task.Tenant {
			return ExecutionResult{}, &AuthorizationFailure{
				AgentID: agentID, TaskID: taskID,
				Reason: fmt.Sprintf("cross-tenant execution blocked: task requires tenant %q", task.Tenant),
			}
		}
	}
	// --- end TOCTOU revalidation ---

	// Re-authorize immediately before dispatch: a policy decision made at
	// Apply time is not assumed to still hold now. Reconstruct the
	// equivalent of the AssignTaskProposal that put this task into its
	// current Assigned state and run it past the SAME Authorizer again,
	// against the freshly re-read state above.
	reauth := AssignTaskProposal{
		TaskID:           taskID,
		CandidateAgentID: agentID,
		RequiredCaps:     task.RequiredCapabilities,
		RationaleSummary: "execution-boundary re-authorization",
	}
	if err := e.Authorizer.Authorize(ctx, state, reauth); err != nil {
		return ExecutionResult{}, fmt.Errorf("re-authorization at execution boundary: %w", err)
	}

	if err := e.State.MarkExecuting(taskID); err != nil {
		return ExecutionResult{}, err
	}

	limit := e.Limits.MaxExecutionTime
	if limit <= 0 {
		limit = DefaultSupervisorLimits().MaxExecutionTime
	}
	execCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	// startedAt/finishedAt bound exactly the authoritative execution
	// window - after TOCTOU/re-authorization (which are not part of
	// "the execution" itself) and up to the final, authoritative
	// outcome. The observer call happens via a deferred closure over
	// named results, so EVERY return path below (including early
	// MarkCompleted/MarkFailed errors) is observed exactly once, with
	// whatever result/execErr Execute is actually about to return - the
	// observer can never see, and can never influence, anything other
	// than the already-decided outcome.
	// executionID is generated HERE, by Core, at the actual execution
	// boundary - before the provider is ever called, so it exists
	// regardless of how the execution turns out, and it is never
	// derived from or overwritable by anything the provider/agent
	// returns (see ExecutionID's doc comment).
	executionID := NewExecutionID(taskID)
	startedAt := time.Now().UTC()
	var result ExecutionResult
	var execErr error
	defer func() {
		finishedAt := time.Now().UTC()
		if e.Observer == nil {
			return
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					// An observer panic must never affect Execute's own
					// return value (already guaranteed, since the panic
					// is recovered here, after result/execErr were
					// already set below) - but it must not vanish either.
					// Record a bounded, content-free signal only; the
					// recovered value itself is never passed onward,
					// since it could contain arbitrary/sensitive data.
					if e.FailureSink != nil {
						func() {
							defer func() { _ = recover() }() // the sink itself must never be able to crash Execute, recursively
							e.FailureSink.RecordExecutionObserverPanic()
						}()
					}
				}
			}()
			e.Observer.ObserveExecution(ctx, task, agentID, executionID, result, execErr, startedAt, finishedAt)
		}()
	}()

	result, execErr = e.Provider.Execute(execCtx, agentID, task)
	// ExecutionID is Core-owned: set unconditionally here, overwriting
	// anything the provider may have populated.
	result.ExecutionID = executionID
	if execErr != nil {
		_ = e.State.MarkFailed(taskID)
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			execErr = &ExecutionTimeout{TaskID: taskID, Limit: limit}
			result = ExecutionResult{ExecutionID: executionID}
			return result, execErr
		}
		execErr = &AgentExecutionFailure{AgentID: agentID, TaskID: taskID, Reason: execErr.Error()}
		result = ExecutionResult{ExecutionID: executionID}
		return result, execErr
	}

	if result.Status == TaskStatusFailed {
		_ = e.State.MarkFailed(taskID)
		return result, nil
	}
	if err := e.State.MarkCompleted(taskID); err != nil {
		execErr = err
		return result, execErr
	}
	return result, nil
}
