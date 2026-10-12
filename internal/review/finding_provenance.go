package review

import (
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// ExecutionProvenance is the Core-authoritative execution context a
// FindingObservation is constructed from. Every field here must be
// supplied from real Core/Executor state - this type exists specifically
// so a caller cannot accidentally (or deliberately) pass agent/provider-
// supplied data into the fields that become SourceExecutionRef/
// SourceExecutionID/SourceAgentID. ADR 0006 Phase 7C introduced
// SourceExecutionRef as a Core-owned field; ADR 0007 closes the
// remaining gap ("the productive finding path does not yet reliably set
// it") by making this the ONE constructor that ever sets it, mirroring
// how supervisor.Executor.Execute is the one place ExecutionID is ever
// generated and how ExecutionResult.ExecutionID is set unconditionally,
// overwriting anything a provider supplied.
type ExecutionProvenance struct {
	Tenant      string
	TaskID      TaskID
	ExecutionID string             // supervisor.ExecutionID as a string - Core-owned
	AgentID     supervisor.AgentID // the agent Core actually dispatched
}

// FindingClaim is the agent/claim-level data a FindingObservation also
// needs - everything here is untrusted-origin (an agent's own report)
// and carries no authority over provenance. It can never override any
// ExecutionProvenance field: there is no field here named
// SourceExecutionRef/SourceExecutionID/SourceAgentID, so a caller
// physically cannot pass a forged value into those positions through
// this struct.
type FindingClaim struct {
	ID                 TaskID // unique within the ReviewGroup
	FindingID          string
	ArtifactVersion    ArtifactVersion
	EvidenceRefs       []string
	ConsumedFindingIDs []string
	Stance             ObservationStance
	Timestamp          time.Time
}

// NewFindingObservationFromExecution is the ONLY constructor that
// produces a FindingObservation with its provenance fields populated.
// SourceExecutionRef/SourceExecutionID/SourceAgentID are ALWAYS derived
// from `provenance`, never from `claim` - even if a future caller tried
// to smuggle a fake execution reference into claim-shaped data, there is
// no field in FindingClaim that could carry it, so it is structurally
// impossible for agent-supplied data to reach these three fields through
// this path.
//
// Fails closed (*MissingExecutionProvenance) if provenance.ExecutionID
// is empty - never falls back to guessing by TaskID, by "latest
// execution," by timestamp, or by agent, which is exactly the class of
// heuristic ADR 0006 Phase 7C already eliminated from the finalization
// side; this constructor ensures the SAME discipline on the
// finding-creation side.
func NewFindingObservationFromExecution(provenance ExecutionProvenance, claim FindingClaim) (FindingObservation, error) {
	if provenance.ExecutionID == "" {
		return FindingObservation{}, &MissingExecutionProvenance{
			FindingID: claim.FindingID,
			Reason:    "no Core-owned ExecutionID in execution context - refusing to guess",
		}
	}
	if provenance.TaskID == "" {
		return FindingObservation{}, &MissingExecutionProvenance{
			FindingID: claim.FindingID,
			Reason:    "no TaskID in execution context",
		}
	}
	if provenance.AgentID == "" {
		return FindingObservation{}, &MissingExecutionProvenance{
			FindingID: claim.FindingID,
			Reason:    "no Core-dispatched AgentID in execution context",
		}
	}

	return FindingObservation{
		ID:                 claim.ID,
		FindingID:          claim.FindingID,
		SourceAgentID:      provenance.AgentID,
		SourceExecutionID:  provenance.TaskID,
		SourceExecutionRef: provenance.ExecutionID,
		ArtifactVersion:    claim.ArtifactVersion,
		EvidenceRefs:       claim.EvidenceRefs,
		ConsumedFindingIDs: claim.ConsumedFindingIDs,
		Stance:             claim.Stance,
		Timestamp:          claim.Timestamp,
	}, nil
}

// HasExecutionProvenance reports whether obs carries a Core-owned
// execution reference sufficient for joint quality attribution -
// exactly the SourceExecutionRef field ReviewFinalizationHook already
// requires (directly, or via EvidenceRefs resolution) to finalize a
// PerformanceObservation. A legacy/external FindingObservation lacking
// this may still be valid for display/review under existing semantics,
// but must never be treated as attributable here.
func (o FindingObservation) HasExecutionProvenance() bool {
	return o.SourceExecutionRef != "" || len(o.EvidenceRefs) > 0
}
