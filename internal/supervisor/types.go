// Package supervisor implements Phase 1 of ADR 0002
// (docs/decisions/0002-supervisor-agent-with-deterministic-control-plane.md):
// the domain types and the pure-Go, provider-independent validation boundary
// a HarnessMesh Supervisor agent's proposals must pass through before
// anything in HarnessMesh Core acts on them.
//
// Nothing in this package calls an LLM, a model provider, or Switchyard.
// Every Supervisor output reaching this package is untrusted input: it is
// schema-validated, checked against the live agent/task state, and either
// accepted or rejected closed. Rejected proposals are never partially
// applied.
package supervisor

import (
	"context"
	"time"
)

// TaskID identifies a task within a TaskGraph.
type TaskID string

// AgentID identifies an agent participant known to HarnessMesh Core's
// existing registry (collaboration.Engine). The Supervisor never creates or
// authorizes agent identities; it only references them.
type AgentID string

// SupervisorAgentID is the reserved identity of the Supervisor itself. It
// must never be accepted as a CandidateAgentID or ReviewerAgentID: the
// Supervisor cannot assign work or review to itself.
const SupervisorAgentID AgentID = "supervisor"

// TaskStatus is the authoritative lifecycle state of a Task, owned
// entirely by Core's CoreState (see state.go). The Supervisor never writes
// Status directly; it only emits proposals that, if validated and
// authorized, cause Core to transition it.
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusAssigned  TaskStatus = "assigned"
	TaskStatusExecuting TaskStatus = "executing"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
)

// Task is a unit of work the Supervisor has proposed, or that already
// exists in the current TaskGraph. Risk mirrors economy.TaskTier's values
// (trivial/routine/complex/critical) rather than introducing a parallel
// risk vocabulary, per ADR 0002.
type Task struct {
	ID                   TaskID
	Type                 string
	RequiredCapabilities []string
	Dependencies         []TaskID
	Risk                 string
	// Tenant scopes the task to a tenant boundary. Empty means
	// "unscoped" and is not tenant-checked. Non-empty values are checked,
	// both at authorization time and again at the execution boundary
	// (TOCTOU revalidation), against the assigned agent's own tenant via
	// TenantDirectory.
	Tenant string
	// Status is the authoritative lifecycle state, owned by CoreState.
	Status TaskStatus
	// EstimatedCost is optional; zero means "unspecified" and is not
	// guardrail-checked. Non-zero values are checked against
	// SupervisorLimits.MaxCost.
	EstimatedCost float64
	// Locked is set by Core once a task graph has been accepted. A locked
	// task's RequiredCapabilities can only grow (via a later Replan), never
	// shrink - this is the policy-downgrade-forbidden invariant from
	// MeshCommit's ProofObligation lock, applied to tasks.
	Locked bool
}

// TaskGraph is the current, Core-held set of tasks and their dependencies.
// The Supervisor never holds the authoritative graph; it only ever sees a
// minimized projection of it (ExecutionState.Tasks) and proposes mutations
// to it.
type TaskGraph struct {
	Tasks []Task
}

// ByID returns the task with the given ID and whether it was found.
func (g TaskGraph) ByID(id TaskID) (Task, bool) {
	for _, t := range g.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

// ProposalType enumerates the kinds of proposals a Supervisor may emit,
// mirroring the enum-constant pattern already used by protocol.MessageType.
type ProposalType string

const (
	ProposalCreateTask      ProposalType = "create_task"
	ProposalAssignTask      ProposalType = "assign_task"
	ProposalRequestReview   ProposalType = "request_review"
	ProposalRequestEvidence ProposalType = "request_evidence"
	ProposalRetryTask       ProposalType = "retry_task"
	ProposalReplan          ProposalType = "replan"
	ProposalSynthesize      ProposalType = "synthesize"
	ProposalEscalate        ProposalType = "escalate"
)

// knownProposalTypes is the closed set of ProposalType values the validator
// accepts. Any other value is a ProposalValidationFailure.
var knownProposalTypes = map[ProposalType]bool{
	ProposalCreateTask:      true,
	ProposalAssignTask:      true,
	ProposalRequestReview:   true,
	ProposalRequestEvidence: true,
	ProposalRetryTask:       true,
	ProposalReplan:          true,
	ProposalSynthesize:      true,
	ProposalEscalate:        true,
}

// AgentProposal is the untrusted union of everything a Supervisor may
// propose. None of these types carry a model or provider field: model
// selection stays entirely inside Switchyard/modelrouting (ADR 0001) and is
// never something the Supervisor requests.
type AgentProposal interface {
	ProposalType() ProposalType
}

// RationaleSummary is a short, auditable justification. It is the only
// free-text field persisted for a proposal - never raw chain-of-thought.
type RationaleSummary string

// MaxRationaleLength bounds RationaleSummary so hidden reasoning cannot be
// smuggled into the audit trail as an oversized "summary".
const MaxRationaleLength = 500

type CreateTaskProposal struct {
	Task             Task
	RationaleSummary RationaleSummary
}

func (CreateTaskProposal) ProposalType() ProposalType { return ProposalCreateTask }

type AssignTaskProposal struct {
	TaskID           TaskID
	CandidateAgentID AgentID
	RequiredCaps     []string
	RationaleSummary RationaleSummary
}

func (AssignTaskProposal) ProposalType() ProposalType { return ProposalAssignTask }

type RequestReviewProposal struct {
	TaskID           TaskID
	ReviewerAgentID  AgentID
	RationaleSummary RationaleSummary
}

func (RequestReviewProposal) ProposalType() ProposalType { return ProposalRequestReview }

type RequestEvidenceProposal struct {
	TaskID           TaskID
	FromAgentID      AgentID
	RationaleSummary RationaleSummary
}

func (RequestEvidenceProposal) ProposalType() ProposalType { return ProposalRequestEvidence }

type RetryTaskProposal struct {
	TaskID           TaskID
	RationaleSummary RationaleSummary
}

func (RetryTaskProposal) ProposalType() ProposalType { return ProposalRetryTask }

// ReplanProposal adds tasks/dependencies in response to an ExecutionEvent.
// It can only grow a locked task's RequiredCapabilities, never remove from
// them - enforced by the validator, not by this type.
type ReplanProposal struct {
	NewTasks         []Task
	UpdatedTasks     []Task
	RationaleSummary RationaleSummary
}

func (ReplanProposal) ProposalType() ProposalType { return ProposalReplan }

type SynthesizeProposal struct {
	TaskIDs          []TaskID
	Summary          RationaleSummary
	RationaleSummary RationaleSummary
}

func (SynthesizeProposal) ProposalType() ProposalType { return ProposalSynthesize }

type EscalateProposal struct {
	TaskID           TaskID
	Reason           string
	RationaleSummary RationaleSummary
}

func (EscalateProposal) ProposalType() ProposalType { return ProposalEscalate }

// Goal is the user intent the Supervisor is asked to plan for.
type Goal struct {
	ID          string
	Description string
}

// ExecutionState is the minimized context projection handed to the
// Supervisor - never the full repository, full agent registry, or full
// tenant data. Building it is Core's responsibility (via contextpack-style
// minimization in later phases); Phase 1 only defines its shape.
type ExecutionState struct {
	Goal            Goal
	Tasks           TaskGraph
	ReplanCount     int
	ReviewRounds    int
	DelegationDepth int
	// Assignments records the agent currently assigned to execute each task,
	// as previously accepted by Core. Used to detect self-review: a
	// RequestReviewProposal naming the same agent as the task's executor is
	// rejected.
	Assignments map[TaskID]AgentID
}

// ExecutionEvent is something that happened during execution that may
// trigger a Replan.
type ExecutionEventKind string

const (
	EventAgentFailed       ExecutionEventKind = "agent_failed"
	EventModelUnavailable  ExecutionEventKind = "model_unavailable"
	EventNewFinding        ExecutionEventKind = "new_finding"
	EventMissingEvidence   ExecutionEventKind = "missing_evidence"
	EventTestRegression    ExecutionEventKind = "test_regression"
	EventPolicyRejection   ExecutionEventKind = "policy_rejection"
	EventAgentDisagreement ExecutionEventKind = "agent_disagreement"
	EventDependencyBlocked ExecutionEventKind = "dependency_blocked"
)

type ExecutionEvent struct {
	Kind      ExecutionEventKind
	TaskID    TaskID
	Detail    string
	Timestamp time.Time
}

// Synthesis is the Supervisor's proposed final cross-agent summary. It is
// advisory: Core decides whether referenced findings may actually be
// treated as verified, per the existing peer.resolve gate.
type Synthesis struct {
	TaskIDs          []TaskID
	Summary          string
	RationaleSummary RationaleSummary
}

// SupervisorLimits are deterministic guardrails enforced by Core - read
// from configuration, never from Supervisor output. MaxTaskDepth,
// MaxAgentFanout, MaxReplanCount, MaxReviewRounds, and MaxCost are enforced
// by the Validator at proposal-validation time. MaxExecutionTime is
// enforced by the Executor at the execution boundary (Phase 2A), since it
// bounds a running task, not a proposal. MaxConcurrency (Phase 3) bounds
// how many tasks Scheduler may dispatch at once - it is Core
// configuration, and the Supervisor has no way to request a different
// value: Scheduler never reads concurrency hints from a proposal.
type SupervisorLimits struct {
	MaxTaskDepth     int
	MaxAgentFanout   int
	MaxReplanCount   int
	MaxReviewRounds  int
	MaxExecutionTime time.Duration
	MaxCost          float64
	MaxConcurrency   int
	// MaxWorkflowTicks bounds how many Scheduler.Tick calls a Workflow
	// driver may perform for one goal, independent of MaxReplanCount (a
	// stuck workflow that never trips the replan limit - e.g. nothing is
	// ready, nothing is blocked, nothing is active, yet the graph is also
	// not complete - must still terminate deterministically).
	MaxWorkflowTicks int
}

// DefaultSupervisorLimits returns conservative, explicit defaults. Core
// configuration may override them, but a zero-value SupervisorLimits is
// never silently treated as "unlimited" by the validator.
func DefaultSupervisorLimits() SupervisorLimits {
	return SupervisorLimits{
		MaxTaskDepth:     8,
		MaxAgentFanout:   16,
		MaxReplanCount:   5,
		MaxReviewRounds:  3,
		MaxExecutionTime: 30 * time.Minute,
		MaxCost:          0, // 0 = cost guardrail not enforced unless explicitly set
		MaxConcurrency:   4,
		MaxWorkflowTicks: 50,
	}
}

// TenantDirectory is an optional capability an AgentDirectory
// implementation may also provide, resolving which tenant an agent
// belongs to. It is checked wherever a Task.Tenant is non-empty, both at
// authorization time and again at the execution boundary, so an agent that
// changes tenant (or a directory that is simply more precise than the one
// available at validation time) cannot be used to cross a tenant boundary
// via a stale snapshot.
type TenantDirectory interface {
	TenantOf(id AgentID) (string, bool)
}

// ExecutionResult is what a real task execution produced. It is advisory
// data about what happened - it never itself constitutes verified
// evidence; only the existing, authoritative evidence/finding path (peer
// submit_evidence / peer.resolve) can mark something verified.
type ExecutionResult struct {
	TaskID TaskID
	// ExecutionID is the Core-owned identity of the execution attempt
	// that produced this result (see NewExecutionID) - set by Executor
	// itself, unconditionally overwriting anything an ExecutionProvider
	// may have populated, never trusted from agent/provider output.
	ExecutionID ExecutionID
	Status      TaskStatus // TaskStatusCompleted or TaskStatusFailed
	Summary     string
	// Findings optionally correlates this result with finding identifiers
	// in the existing, unmodified evidence/finding subsystem
	// (internal/collaboration's FindingPayload). This field never carries
	// finding content itself - only IDs for correlation - and never
	// implies verification; only the existing peer.resolve path can move
	// a Finding to verified.
	Findings []string
	// ModelUsages optionally attributes which model/provider/route
	// combination(s) actually served this execution - populated only by
	// the ExecutionProvider itself from real routing/transport context
	// (see ModelUsageRef's doc comment). Empty is honest for providers
	// with nothing reliable to report; never guessed or backfilled from
	// agent output text.
	ModelUsages []ModelUsageRef
}

// ExecutionProvider actually runs a task on a routed agent. In Phase 2A
// this is always a fake/test implementation (FakeExecutionProvider) - no
// real model/provider call happens here; Phase 2B replaces it with one
// that calls out through modelrouting/Switchyard.
type ExecutionProvider interface {
	Execute(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error)
}

// FakeExecutionProvider is a deterministic, injectable ExecutionProvider
// for tests. ExecuteFunc should select on ctx.Done() if it wants to
// simulate a task that overruns MaxExecutionTime.
type FakeExecutionProvider struct {
	ExecuteFunc func(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error)
}

func (f *FakeExecutionProvider) Execute(ctx context.Context, agentID AgentID, task Task) (ExecutionResult, error) {
	if f.ExecuteFunc == nil {
		return ExecutionResult{TaskID: task.ID, Status: TaskStatusCompleted}, nil
	}
	return f.ExecuteFunc(ctx, agentID, task)
}

var _ ExecutionProvider = (*FakeExecutionProvider)(nil)
