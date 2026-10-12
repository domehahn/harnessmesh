package performance

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// ObservationKind distinguishes what a PerformanceObservation records.
// Only "execution" exists today - the field exists now so the
// (TaskID, ExecutionID, Kind) idempotency key the request asks for is
// already future-proofed against a second kind later (e.g. a
// standalone "review" observation) without changing ObservationID's
// derivation.
type ObservationKind string

const ObservationKindExecution ObservationKind = "execution"

// ObservationID is a deterministic function of (Tenant, TaskID,
// ExecutionID, Kind) - never a random/generated ID. Two calls with the
// same inputs always produce the same ID, which is exactly the
// idempotency key the request requires: "same TaskID + ExecutionID +
// ObservationKind must have deterministic duplicate handling."
type ObservationID string

// ComputeObservationID derives the deterministic idempotency key.
func ComputeObservationID(tenant string, taskID supervisor.TaskID, executionID string, kind ObservationKind) ObservationID {
	h := sha256.New()
	for _, part := range []string{tenant, string(taskID), executionID, string(kind)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return ObservationID(hex.EncodeToString(h.Sum(nil)))
}

// ObservationStatus distinguishes pending (execution observed, review
// not yet settled) from finalized (an authoritative quality verdict -
// verified or rejected - has been reached). Aggregations must treat
// these separately; an unreviewed/pending observation is never counted
// as a verified success.
type ObservationStatus string

const (
	ObservationPending   ObservationStatus = "pending"
	ObservationFinalized ObservationStatus = "finalized"
)

// ExecutionOutcome is Core's authoritative view of what happened to the
// execution itself - entirely independent of review/quality. Derived
// only from supervisor.ExecutionResult/Scheduler event data, never from
// an agent's own claim.
type ExecutionOutcome string

const (
	ExecutionCompleted             ExecutionOutcome = "completed"
	ExecutionFailed                ExecutionOutcome = "failed"
	ExecutionTimedOut              ExecutionOutcome = "timed_out"
	ExecutionPolicyRejected        ExecutionOutcome = "policy_rejected"
	ExecutionAuthorizationRejected ExecutionOutcome = "authorization_rejected"
	ExecutionBlocked               ExecutionOutcome = "blocked"
	ExecutionCancelled             ExecutionOutcome = "cancelled"
	ExecutionPartiallyCompleted    ExecutionOutcome = "partially_completed"
)

var knownExecutionOutcomes = map[ExecutionOutcome]bool{
	ExecutionCompleted: true, ExecutionFailed: true, ExecutionTimedOut: true,
	ExecutionPolicyRejected: true, ExecutionAuthorizationRejected: true,
	ExecutionBlocked: true, ExecutionCancelled: true, ExecutionPartiallyCompleted: true,
}

// KnownExecutionOutcome reports whether o is a recognized value.
func KnownExecutionOutcome(o ExecutionOutcome) bool { return knownExecutionOutcomes[o] }

// QualityOutcome is the independent-review verdict dimension - entirely
// separate from ExecutionOutcome (an execution can complete and still be
// unreviewed, contested, or rejected). It is a superset of
// protocol.FindingStatus's six Phase-4 verdict values (reused verbatim,
// via QualityFromFindingStatus - no parallel vocabulary) plus one
// sentinel, QualityUnreviewed, for "no review has happened yet" - a
// state protocol.FindingStatus itself has no value for, since
// internal/review only ever creates a ReviewGroup once at least one
// observation exists.
type QualityOutcome string

const (
	QualityUnreviewed    QualityOutcome = "unreviewed"
	QualityUnconfirmed   QualityOutcome = QualityOutcome(protocol.FindingUnconfirmed)
	QualitySupported     QualityOutcome = QualityOutcome(protocol.FindingSupported)
	QualityContested     QualityOutcome = QualityOutcome(protocol.FindingContested)
	QualityNeedsEvidence QualityOutcome = QualityOutcome(protocol.FindingNeedsEvidence)
	QualityVerified      QualityOutcome = QualityOutcome(protocol.FindingVerified)
	QualityRejected      QualityOutcome = QualityOutcome(protocol.FindingRejected)
)

// QualityFromFindingStatus maps an authoritative review.ReviewGroup.Status
// (a protocol.FindingStatus) onto QualityOutcome. The mapping is the
// identity function for all six shared values - documented explicitly,
// per the same "no lossy mapping" discipline ADR 0004 applied to
// Finding-status projection.
func QualityFromFindingStatus(s protocol.FindingStatus) QualityOutcome {
	return QualityOutcome(s)
}

// ModelUsageRef attributes one route/provider/model combination actually
// used during an execution, sourced only from the routing/transport
// context (supervisor.ModelRoutingDecision + the served-model metadata
// an InferenceTransport echoes back) - never from an LLM's own claimed
// identity. Count lets one execution that made several inference calls
// (e.g. a Supervisor planning call plus an agent's own model calls)
// record more than one usage without a false single-model
// simplification.
type ModelUsageRef struct {
	RouteID  string
	Provider string
	Model    string // the SERVED model identity, never LLM-claimed
	Count    int
}

// PerformanceMetrics are the deterministic, bounded measurements this
// phase collects. No fake precision: CostUnits is explicitly a
// deterministic unit count (matching the review package's own
// DefaultActionCost discipline), not a real monetary/token figure
// unless a caller has a real one to supply.
type PerformanceMetrics struct {
	Duration                     time.Duration
	ExecutionAttempts            int
	Retries                      int
	Replans                      int
	ReviewRounds                 int
	ReworkCount                  int
	FindingCount                 int
	VerifiedFindingCount         int
	RejectedFindingCount         int
	IndependentConfirmationCount int
	EvidenceCount                int
	CostUnits                    float64
}

// PerformanceObservation is this package's central record. Every
// authority-bearing field (TaskID, AgentID, ExecutionOutcome,
// ModelUsages, QualityOutcome, ReviewerAgentIDs) is derived from
// existing authoritative state (internal/supervisor's CoreState/
// ExecutionResult, internal/modelrouting's ModelRoutingDecision,
// internal/review's ReviewGroup) - never self-reported by an agent or an
// LLM. See NewObservation/FinalizeWithReview (lifecycle.go) for the only
// two ways one is ever constructed or updated.
type PerformanceObservation struct {
	ID          ObservationID
	Tenant      string
	TaskID      supervisor.TaskID
	ExecutionID string
	TaskClass   TaskClass
	Risk        string // mirrors economy.TaskTier's values, optional

	// AgentID is the agent that EXECUTED the task - implementation
	// attribution. ReviewerAgentIDs are the agents whose observations
	// were counted as independent review of the resulting claim -
	// review attribution. These are deliberately never conflated (see
	// "Agent Attribution" in ADR 0005): a task's implementer and its
	// reviewers may be credited/debited separately.
	AgentID          supervisor.AgentID
	ReviewerAgentIDs []supervisor.AgentID

	// SelectionRecordRef references an existing
	// economy.RoutingDecision/store.RoutingDecisionRecord by its
	// (SessionID, RequestedCapability, Timestamp) - this package never
	// duplicates agent-selection provenance, only points at it.
	SelectionRecordRef string

	ModelUsages []ModelUsageRef

	ExecutionOutcome ExecutionOutcome
	QualityOutcome   QualityOutcome

	// ReviewGroupID references the internal/review.ReviewGroup this
	// observation's quality outcome was derived from, if any.
	ReviewGroupID string

	Metrics PerformanceMetrics
	Status  ObservationStatus

	StartedAt  time.Time
	FinishedAt time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
