// Package review implements ADR 0002 Phase 4: an evidence-driven
// arbitration / independent-review layer on top of the existing
// Supervisor/Core architecture (internal/supervisor) and the existing
// Finding/Evidence vocabulary (internal/protocol).
//
// The central invariant carries over unchanged from Phases 1-3: Arbiter
// output is untrusted data. It is a proposal, never authority. HarnessMesh
// Core (ReviewValidator -> ReviewAuthorizer -> ReviewState's atomic apply)
// remains the only thing that can move a claim to protocol.FindingVerified
// or protocol.FindingRejected, and it does so only after independently
// re-checking evidence, artifact-version freshness, and reviewer
// independence - never because an Arbiter or a majority of agents merely
// said so.
package review

import (
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// ReviewGroupID identifies a correlated set of observations about the
// same claim, against the same artifact version.
type ReviewGroupID string

// ClaimFingerprint is a deterministic correlation key - never free-text
// similarity. See fingerprint.go.
type ClaimFingerprint string

// SubjectRef identifies WHAT a claim is about, independent of how any
// one agent phrased it.
type SubjectRef struct {
	Repository string
	Path       string
}

// ArtifactVersion identifies the exact code/artifact version a claim or
// observation pertains to (e.g. a tree-hash, matching the same concept
// MeshCommit's ChangeEvidence.TreeHash already uses - internal/review
// does not import internal/collaboration/meshcommit to get this value;
// callers supply whatever version identifier their own artifact-binding
// mechanism produces). A ReviewGroup is scoped to exactly one
// ArtifactVersion: an observation against a different version never
// joins an existing group, however similar its ClaimFingerprint - this
// is the mechanism that prevents a stale finding from silently carrying
// a verification result forward onto changed code.
type ArtifactVersion string

// FindingObservation is one agent's report against a ReviewGroup's
// claim. It correlates to an existing protocol.FindingPayload (by ID) -
// internal/review does not duplicate finding content, only the
// provenance fields needed to reason about independence and freshness.
type FindingObservation struct {
	ID TaskID // unique within its ReviewGroup

	// FindingID correlates to protocol.FindingPayload.ID - the existing,
	// unmodified finding record. internal/review never stores finding
	// claim/evidence TEXT itself.
	FindingID string

	// SourceAgentID is the agent that produced this observation. Never
	// trusted from the observation's own claim - a caller constructing a
	// FindingObservation from, say, an MCP peer.submit_finding call must
	// set this from the AUTHENTICATED caller identity, exactly as
	// collaboration's existing peer.challenge handler already guards
	// against challenger-identity spoofing (internal/mcp/server.go).
	SourceAgentID supervisor.AgentID

	// SourceExecutionID correlates this observation to the task
	// execution (internal/supervisor.TaskID) that produced it, if any -
	// enables "did this reviewer run against the primary artifact
	// themselves" provenance checks.
	SourceExecutionID TaskID

	// SourceExecutionRef is the Core-owned, per-attempt execution
	// identity (supervisor.ExecutionID, as a string) this observation's
	// finding actually originated from - distinct from SourceExecutionID
	// (which only identifies the task, not which attempt). This is the
	// field a finalization consumer (internal/performance's
	// ReviewFinalizationHook) uses for EXACT PerformanceObservation
	// correlation, never a "most recent" heuristic over SourceExecutionID
	// alone - ADR 0006 Phase 7C. Must be set only from real Core
	// execution context (e.g. supervisor.ExecutionResult.ExecutionID,
	// propagated through whatever constructs this observation), never
	// trusted from agent/LLM output. May be left empty if this
	// observation cannot be bound to one specific execution attempt (a
	// consumer needing exact attribution then falls back to resolving
	// via EvidenceRefs, and fails closed - no finalization - if that
	// also does not resolve, rather than guessing).
	SourceExecutionRef string

	// ArtifactVersion is the version THIS observation was made against.
	ArtifactVersion ArtifactVersion

	// EvidenceRefs correlates to protocol.FindingPayload.EvidenceRefs /
	// protocol.ChangeEvidence.ID values - again, references only, never
	// evidence content.
	EvidenceRefs []string

	// ConsumedFindingIDs lists any OTHER FindingObservation.FindingID
	// this observation's agent read as input before producing its own
	// report. A non-empty list means the agent did not independently
	// inspect the primary artifact - it is reacting to another agent's
	// conclusion. See independence.go.
	ConsumedFindingIDs []string

	// Stance is this observation's position on the claim.
	Stance ObservationStance

	Timestamp time.Time
}

// TaskID aliases supervisor.TaskID for observation/task correlation
// without forcing every caller to import both packages just to spell the
// same identifier type.
type TaskID = supervisor.TaskID

// ObservationStance is a FindingObservation's position on the group's
// claim - deliberately NOT protocol.FindingStatus (that enum describes
// the claim's overall, Core-derived verdict; Stance is one observer's
// individual report).
type ObservationStance string

const (
	StanceConfirms  ObservationStance = "confirms"
	StanceRefutes   ObservationStance = "refutes"
	StanceUncertain ObservationStance = "uncertain"
)

// ReviewGroup is the correlated set of observations about one claim
// against one artifact version. Status is a protocol.FindingStatus value
// drawn only from the six Phase-4 verdict states
// (unconfirmed/supported/contested/needs_evidence/verified/rejected) -
// reusing the existing enum rather than inventing a parallel one, per
// ADR 0002 Phase 4's explicit instruction. Status is mutated exclusively
// by ReviewState's atomic apply (state.go), never directly by an Arbiter
// and never by a FindingObservation's mere existence.
type ReviewGroup struct {
	ID              ReviewGroupID
	Claim           ClaimFingerprint
	Subject         SubjectRef
	ArtifactVersion ArtifactVersion
	Tenant          string // empty = unscoped, mirrors supervisor.Task.Tenant
	// SessionID correlates this group to the collaboration.Engine
	// session whose Finding records it may eventually project onto (see
	// FindingProjector in projection.go). Empty if no projection is
	// configured for this group.
	SessionID         string
	FindingID         string // the protocol.FindingPayload.ID this group projects onto, if any
	Status            protocol.FindingStatus
	Observations      []FindingObservation
	ArbitrationRounds int
	EvidenceRequests  int
	// CostSpent is a deterministic, bounded cost accumulator (see
	// DefaultActionCost in limits.go) - NOT a real provider-cost figure.
	// Documented explicitly to avoid implying fake monetary precision.
	CostSpent float64
	// PredecessorGroupID/Depth track a remediation chain: when a new
	// group opens because a claim is being re-reviewed against a
	// successor artifact version (see IngestSuccessor), Depth =
	// predecessor.Depth + 1. A fresh, unrelated claim has Depth 0.
	PredecessorGroupID ReviewGroupID
	Depth              int
}

// ByID returns the observation with the given ID, if present.
func (g ReviewGroup) ObservationByID(id TaskID) (FindingObservation, bool) {
	for _, o := range g.Observations {
		if o.ID == id {
			return o, true
		}
	}
	return FindingObservation{}, false
}

// ReviewLimits are Core-configured, deterministic guardrails - never
// read from Arbiter output, mirroring supervisor.SupervisorLimits.
type ReviewLimits struct {
	MaxIndependentReviews int
	MaxArbitrationRounds  int
	MaxEvidenceRequests   int
	MaxReviewDepth        int
	MaxReviewCost         float64
}

// DefaultReviewLimits returns conservative, explicit defaults.
func DefaultReviewLimits() ReviewLimits {
	return ReviewLimits{
		MaxIndependentReviews: 5,
		MaxArbitrationRounds:  5,
		MaxEvidenceRequests:   5,
		MaxReviewDepth:        3,
		MaxReviewCost:         0, // 0 = not enforced unless explicitly set
	}
}
