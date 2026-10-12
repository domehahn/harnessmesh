package supervisor

import (
	"fmt"
	"time"
)

// FailureClass groups the typed errors this package can return, matching
// ADR 0002's "Failure Classes" section. Core must branch on failure class,
// not on error message text.
type FailureClass string

const (
	FailureClassProposalValidation FailureClass = "proposal_validation_failure"
	FailureClassPolicyRejection    FailureClass = "policy_rejection"
	FailureClassUnauthorized       FailureClass = "unauthorized_proposal"
	FailureClassReplanLimit        FailureClass = "replan_limit_exceeded"
	FailureClassGuardrail          FailureClass = "guardrail_exceeded"
	FailureClassAuthorization      FailureClass = "authorization_failure"
	FailureClassStateTransition    FailureClass = "state_transition_failure"
	FailureClassAgentRouting       FailureClass = "agent_routing_failure"
	FailureClassAgentExecution     FailureClass = "agent_execution_failure"
	FailureClassExecutionTimeout   FailureClass = "execution_timeout"
	FailureClassModelRouting       FailureClass = "model_routing_failure"
	FailureClassInference          FailureClass = "inference_failure"
	FailureClassDecode             FailureClass = "structured_output_decode_failure"
	FailureClassSchema             FailureClass = "structured_output_schema_failure"
	FailureClassResponseTooLarge   FailureClass = "supervisor_response_too_large"
	FailureClassContextTooLarge    FailureClass = "supervisor_context_too_large"
	FailureClassTaskScheduling     FailureClass = "task_scheduling_failure"
	FailureClassDependencyBlocked  FailureClass = "dependency_blocked"
	FailureClassWorkflowLimit      FailureClass = "workflow_limit_exceeded"
)

// ProposalValidationFailure covers structurally invalid proposals: unknown
// enum values, unknown task/agent references, malformed fields, dependency
// cycles. Always fail-closed - never partially applied.
type ProposalValidationFailure struct {
	ProposalIndex int
	Reason        string
}

func (e *ProposalValidationFailure) Error() string {
	return fmt.Sprintf("proposal validation failure at index %d: %s", e.ProposalIndex, e.Reason)
}

func (e *ProposalValidationFailure) Class() FailureClass { return FailureClassProposalValidation }

// UnknownTaskError is returned when a proposal references a TaskID that
// does not exist in the current graph or the same validation batch.
type UnknownTaskError struct {
	TaskID TaskID
}

func (e *UnknownTaskError) Error() string {
	return fmt.Sprintf("unknown task %q", e.TaskID)
}

func (e *UnknownTaskError) Class() FailureClass { return FailureClassProposalValidation }

// UnknownAgentError is returned when a proposal references an AgentID the
// live agent directory does not recognize. The Supervisor's claim that an
// agent exists is never trusted on its own.
type UnknownAgentError struct {
	AgentID AgentID
}

func (e *UnknownAgentError) Error() string {
	return fmt.Sprintf("unknown agent %q", e.AgentID)
}

func (e *UnknownAgentError) Class() FailureClass { return FailureClassProposalValidation }

// UnknownCapabilityError is returned when a proposal references a
// capability outside the validator's known capability vocabulary.
type UnknownCapabilityError struct {
	Capability string
}

func (e *UnknownCapabilityError) Error() string {
	return fmt.Sprintf("unknown capability %q", e.Capability)
}

func (e *UnknownCapabilityError) Class() FailureClass { return FailureClassProposalValidation }

// CapabilityMismatchError is returned when a candidate agent does not
// actually possess a capability the proposal requires. Capabilities are
// always checked against the agent directory's declared set, never against
// what the Supervisor merely claims is required.
type CapabilityMismatchError struct {
	AgentID AgentID
	Missing []string
}

func (e *CapabilityMismatchError) Error() string {
	return fmt.Sprintf("agent %q is missing required capabilities: %v", e.AgentID, e.Missing)
}

func (e *CapabilityMismatchError) Class() FailureClass { return FailureClassUnauthorized }

// DependencyCycleError is returned when a proposed or existing task graph
// contains a dependency cycle.
type DependencyCycleError struct {
	Cycle []TaskID
}

func (e *DependencyCycleError) Error() string {
	return fmt.Sprintf("dependency cycle detected: %v", e.Cycle)
}

func (e *DependencyCycleError) Class() FailureClass { return FailureClassProposalValidation }

// SelfAssignmentForbiddenError is returned when the Supervisor proposes
// itself as an executor or reviewer, or proposes an agent as the reviewer
// of its own assigned task (self-review), mirroring MeshCommit's
// SelfReviewForbiddenError.
type SelfAssignmentForbiddenError struct {
	AgentID AgentID
	TaskID  TaskID
	Reason  string
}

func (e *SelfAssignmentForbiddenError) Error() string {
	return fmt.Sprintf("self-assignment forbidden for agent %q on task %q: %s", e.AgentID, e.TaskID, e.Reason)
}

func (e *SelfAssignmentForbiddenError) Class() FailureClass { return FailureClassUnauthorized }

// PolicyDowngradeForbiddenError is returned when a Replan would remove a
// required capability from a locked task, mirroring MeshCommit's
// PolicyDowngradeForbiddenError applied to task policy instead of change
// proof-obligation policy.
type PolicyDowngradeForbiddenError struct {
	TaskID string
	Reason string
}

func (e *PolicyDowngradeForbiddenError) Error() string {
	return fmt.Sprintf("policy downgrade forbidden on task %q: %s", e.TaskID, e.Reason)
}

func (e *PolicyDowngradeForbiddenError) Class() FailureClass { return FailureClassPolicyRejection }

// GuardrailExceededError is returned when a proposal batch would exceed a
// deterministic, Core-owned limit (task depth, agent fanout, cost, replan
// count, review rounds). Limits are never read from Supervisor output.
type GuardrailExceededError struct {
	Guardrail string
	Limit     int
	Actual    int
}

func (e *GuardrailExceededError) Error() string {
	return fmt.Sprintf("guardrail %q exceeded: limit=%d actual=%d", e.Guardrail, e.Limit, e.Actual)
}

func (e *GuardrailExceededError) Class() FailureClass { return FailureClassGuardrail }

// ReplanLimitExceeded is a specialization of GuardrailExceededError for
// MaxReplanCount, kept distinct per ADR 0002's failure-class list.
type ReplanLimitExceeded struct {
	Limit  int
	Actual int
}

func (e *ReplanLimitExceeded) Error() string {
	return fmt.Sprintf("replan limit exceeded: limit=%d actual=%d", e.Limit, e.Actual)
}

func (e *ReplanLimitExceeded) Class() FailureClass { return FailureClassReplanLimit }

// PolicyRejection is returned by the Authorizer when a proposal, though
// structurally valid, violates a Core policy (e.g. a denied agent, a
// capability that requires separate authorization). Distinct from
// ProposalValidationFailure: the proposal was well-formed, but Core's
// policy layer - which a successful validation never implies passing -
// refused it.
type PolicyRejection struct {
	ProposalIndex int
	Reason        string
}

func (e *PolicyRejection) Error() string {
	return fmt.Sprintf("policy rejection at proposal %d: %s", e.ProposalIndex, e.Reason)
}

func (e *PolicyRejection) Class() FailureClass { return FailureClassPolicyRejection }

// AuthorizationFailure is returned when a specific actor (agent) is not
// authorized for the action a proposal requests, including re-checks
// performed at the execution boundary after a TOCTOU-relevant state change
// (an agent losing a capability or crossing a tenant boundary between
// validation and execution).
type AuthorizationFailure struct {
	AgentID AgentID
	TaskID  TaskID
	Reason  string
}

func (e *AuthorizationFailure) Error() string {
	return fmt.Sprintf("authorization failure for agent %q on task %q: %s", e.AgentID, e.TaskID, e.Reason)
}

func (e *AuthorizationFailure) Class() FailureClass { return FailureClassAuthorization }

// StateTransitionFailure is returned when a proposal, though validated and
// authorized, would produce an invalid task-graph state transition (e.g.
// assigning a task that is not pending, retrying a task that never
// failed), or when the atomic apply step itself fails (e.g. a simulated
// persistence failure). The authoritative state is left completely
// unchanged when this is returned.
type StateTransitionFailure struct {
	TaskID TaskID
	Reason string
}

func (e *StateTransitionFailure) Error() string {
	return fmt.Sprintf("state transition failure on task %q: %s", e.TaskID, e.Reason)
}

func (e *StateTransitionFailure) Class() FailureClass { return FailureClassStateTransition }

// AgentRoutingFailure is returned when an assigned agent cannot actually be
// routed to at execution time - most notably when the agent existed at
// validation time but has since disappeared from the live directory
// (TOCTOU).
type AgentRoutingFailure struct {
	AgentID AgentID
	TaskID  TaskID
	Reason  string
}

func (e *AgentRoutingFailure) Error() string {
	return fmt.Sprintf("agent routing failure for agent %q on task %q: %s", e.AgentID, e.TaskID, e.Reason)
}

func (e *AgentRoutingFailure) Class() FailureClass { return FailureClassAgentRouting }

// AgentExecutionFailure is returned when the routed agent's execution
// provider returns an error that is not a timeout.
type AgentExecutionFailure struct {
	AgentID AgentID
	TaskID  TaskID
	Reason  string
}

func (e *AgentExecutionFailure) Error() string {
	return fmt.Sprintf("agent execution failure for agent %q on task %q: %s", e.AgentID, e.TaskID, e.Reason)
}

func (e *AgentExecutionFailure) Class() FailureClass { return FailureClassAgentExecution }

// ExecutionTimeout is returned when a task's execution exceeds
// SupervisorLimits.MaxExecutionTime. This limit is enforced at the
// execution boundary (Executor), never inside proposal validation.
type ExecutionTimeout struct {
	TaskID TaskID
	Limit  time.Duration
}

func (e *ExecutionTimeout) Error() string {
	return fmt.Sprintf("execution of task %q exceeded max execution time %s", e.TaskID, e.Limit)
}

func (e *ExecutionTimeout) Class() FailureClass { return FailureClassExecutionTimeout }

// ModelRoutingFailure is returned when ModelRouter.Route fails - Switchyard
// or the routing layer could not resolve a destination for this call. This
// is distinct from InferenceFailure (routing succeeded, the call itself
// failed) and never implies anything about agent/task state.
type ModelRoutingFailure struct {
	Stage  string
	Reason string
}

func (e *ModelRoutingFailure) Error() string {
	return fmt.Sprintf("model routing failure for stage %q: %s", e.Stage, e.Reason)
}

func (e *ModelRoutingFailure) Class() FailureClass { return FailureClassModelRouting }

// InferenceFailure is returned when InferenceTransport.Complete fails
// after a route was successfully resolved.
type InferenceFailure struct {
	Stage  string
	Reason string
}

func (e *InferenceFailure) Error() string {
	return fmt.Sprintf("inference failure for stage %q: %s", e.Stage, e.Reason)
}

func (e *InferenceFailure) Class() FailureClass { return FailureClassInference }

// StructuredOutputDecodeFailure covers syntactic problems with model
// output: invalid JSON, unknown fields, duplicate keys, trailing data.
// The model's raw text is never "repaired" into a valid action - this
// error is always returned instead.
type StructuredOutputDecodeFailure struct {
	Stage  string
	Reason string
}

func (e *StructuredOutputDecodeFailure) Error() string {
	return fmt.Sprintf("structured output decode failure for stage %q: %s", e.Stage, e.Reason)
}

func (e *StructuredOutputDecodeFailure) Class() FailureClass { return FailureClassDecode }

// StructuredOutputSchemaFailure covers semantically invalid but
// syntactically valid model output: unknown proposal/enum values,
// malformed IDs, oversized arrays/fields.
type StructuredOutputSchemaFailure struct {
	Stage  string
	Reason string
}

func (e *StructuredOutputSchemaFailure) Error() string {
	return fmt.Sprintf("structured output schema failure for stage %q: %s", e.Stage, e.Reason)
}

func (e *StructuredOutputSchemaFailure) Class() FailureClass { return FailureClassSchema }

// SupervisorResponseTooLarge is returned when a model's raw response
// exceeds the configured byte limit, before any decoding is attempted.
type SupervisorResponseTooLarge struct {
	Size  int
	Limit int
}

func (e *SupervisorResponseTooLarge) Error() string {
	return fmt.Sprintf("supervisor response too large: %d > %d bytes", e.Size, e.Limit)
}

func (e *SupervisorResponseTooLarge) Class() FailureClass { return FailureClassResponseTooLarge }

// SupervisorContextTooLarge is returned when the constructed prompt
// (system + user) would exceed the configured byte limit, before any
// inference call is made.
type SupervisorContextTooLarge struct {
	Size  int
	Limit int
}

func (e *SupervisorContextTooLarge) Error() string {
	return fmt.Sprintf("supervisor context too large: %d > %d bytes", e.Size, e.Limit)
}

func (e *SupervisorContextTooLarge) Class() FailureClass { return FailureClassContextTooLarge }

// TaskSchedulingFailure is returned by Scheduler when it cannot even
// attempt to schedule a ready task - distinct from AgentRoutingFailure
// (no eligible agent found) and from any Validator/Authorizer rejection
// (those are reported via their own typed errors unchanged). This covers
// scheduler-internal problems: e.g. a ready task whose Dependencies
// reference a task no longer in the graph at scheduling time.
type TaskSchedulingFailure struct {
	TaskID TaskID
	Reason string
}

func (e *TaskSchedulingFailure) Error() string {
	return fmt.Sprintf("task scheduling failure for %q: %s", e.TaskID, e.Reason)
}

func (e *TaskSchedulingFailure) Class() FailureClass { return FailureClassTaskScheduling }

// DependencyBlocked is returned/reported when a task can never become
// ready because a dependency terminated in TaskStatusFailed. It is
// informational (Scheduler reports it as an event, not an error that
// aborts anything) - a Supervisor Replan can still unblock the workflow
// by creating a remediation path.
type DependencyBlocked struct {
	TaskID       TaskID
	BlockingTask TaskID
}

func (e *DependencyBlocked) Error() string {
	return fmt.Sprintf("task %q blocked: dependency %q failed", e.TaskID, e.BlockingTask)
}

func (e *DependencyBlocked) Class() FailureClass { return FailureClassDependencyBlocked }

// WorkflowLimitExceeded is returned when a workflow-level guardrail -
// distinct from a single batch's GuardrailExceededError - is hit: e.g.
// the scheduler has run more ticks than a configured maximum without the
// workflow reaching a terminal state, guarding against a stuck
// Plan<->Replan loop that never trips MaxReplanCount because each
// individual Replan call stays under it while still never converging.
type WorkflowLimitExceeded struct {
	Guardrail string
	Limit     int
	Actual    int
}

func (e *WorkflowLimitExceeded) Error() string {
	return fmt.Sprintf("workflow guardrail %q exceeded: limit=%d actual=%d", e.Guardrail, e.Limit, e.Actual)
}

func (e *WorkflowLimitExceeded) Class() FailureClass { return FailureClassWorkflowLimit }

// RationaleTooLongError is returned when a RationaleSummary exceeds
// MaxRationaleLength, preventing hidden reasoning from being smuggled into
// the audit trail as an oversized "summary".
type RationaleTooLongError struct {
	Length int
	Max    int
}

func (e *RationaleTooLongError) Error() string {
	return fmt.Sprintf("rationale summary too long: %d > %d", e.Length, e.Max)
}

func (e *RationaleTooLongError) Class() FailureClass { return FailureClassProposalValidation }
