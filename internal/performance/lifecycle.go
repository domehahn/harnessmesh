package performance

import (
	"time"

	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// NewObservationInput is everything NewObservation needs - every field
// must already have come from authoritative Core/Scheduler/Executor
// state (see the field-by-field doc comments). This is the only
// constructor; there is no wire decoder for PerformanceObservation
// anywhere in this package, so nothing can build one from untrusted
// Arbiter/Supervisor/agent-text output.
type NewObservationInput struct {
	Tenant      string
	TaskID      supervisor.TaskID
	ExecutionID string
	TaskClass   TaskClass
	Risk        string

	// AgentID must come from supervisor.ExecutionState.Assignments (the
	// agent Core actually dispatched the task to), never from an agent's
	// own self-report.
	AgentID            supervisor.AgentID
	SelectionRecordRef string

	// ExecutionOutcome must come from supervisor.ExecutionResult /
	// Scheduler event classification (classifyExecutionFailure's
	// taxonomy), never from agent-claimed success text.
	ExecutionOutcome ExecutionOutcome
	ModelUsages      []ModelUsageRef

	Metrics PerformanceMetrics

	StartedAt  time.Time
	FinishedAt time.Time
}

// NewObservation validates and constructs a new, Pending
// PerformanceObservation. It never returns a partially-valid record:
// any structural problem (unknown task class, malformed timing/metrics)
// is a typed error and no record is produced.
func NewObservation(in NewObservationInput) (PerformanceObservation, error) {
	if !KnownTaskClass(in.TaskClass) {
		return PerformanceObservation{}, &UnknownTaskClass{TaskClass: in.TaskClass}
	}
	if in.AgentID == "" {
		return PerformanceObservation{}, &AttributionFailure{TaskID: string(in.TaskID), Reason: "no agent attribution supplied"}
	}
	if !KnownExecutionOutcome(in.ExecutionOutcome) {
		return PerformanceObservation{}, &MalformedMetrics{Reason: "unknown execution outcome " + string(in.ExecutionOutcome)}
	}
	if err := validateTiming(in.StartedAt, in.FinishedAt); err != nil {
		return PerformanceObservation{}, err
	}
	if err := validateMetrics(in.Metrics); err != nil {
		return PerformanceObservation{}, err
	}

	now := time.Now().UTC()
	id := ComputeObservationID(in.Tenant, in.TaskID, in.ExecutionID, ObservationKindExecution)
	return PerformanceObservation{
		ID: id, Tenant: in.Tenant, TaskID: in.TaskID, ExecutionID: in.ExecutionID,
		TaskClass: in.TaskClass, Risk: in.Risk, AgentID: in.AgentID,
		SelectionRecordRef: in.SelectionRecordRef, ModelUsages: append([]ModelUsageRef(nil), in.ModelUsages...),
		ExecutionOutcome: in.ExecutionOutcome, QualityOutcome: QualityUnreviewed,
		Metrics: in.Metrics, Status: ObservationPending,
		StartedAt: in.StartedAt, FinishedAt: in.FinishedAt, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func validateTiming(started, finished time.Time) error {
	if started.IsZero() || finished.IsZero() {
		return nil // timing not supplied - not an error, just unmeasured
	}
	if finished.Before(started) {
		return &MalformedMetrics{Reason: "finished_at is before started_at"}
	}
	return nil
}

func validateMetrics(m PerformanceMetrics) error {
	if m.Duration < 0 {
		return &MalformedMetrics{Reason: "negative duration"}
	}
	if m.ExecutionAttempts < 0 || m.Retries < 0 || m.Replans < 0 || m.ReviewRounds < 0 ||
		m.ReworkCount < 0 || m.FindingCount < 0 || m.VerifiedFindingCount < 0 ||
		m.RejectedFindingCount < 0 || m.IndependentConfirmationCount < 0 || m.EvidenceCount < 0 {
		return &MalformedMetrics{Reason: "negative count field"}
	}
	if m.CostUnits < 0 {
		return &MalformedMetrics{Reason: "negative cost"}
	}
	return nil
}

// FinalizeWithReview updates obs's quality dimension from a FRESH
// review.ReviewGroup snapshot - the caller must re-fetch the group from
// review.ReviewState immediately before calling this (never from a
// cached/earlier snapshot), mirroring the TOCTOU discipline ADR 0004
// already established for evidence. Status becomes ObservationFinalized
// only when the group reached a Core-authoritative TERMINAL verdict
// (verified or rejected) - "supported"/"contested"/"needs_evidence"
// keep the observation Pending, since none of those are authority, only
// an intermediate, still-evolving signal (agreement is not authority).
//
// ReviewerAgentIDs is derived as every SourceAgentID in the group other
// than obs.AgentID - the implementer's own observation (if any) is
// excluded from review attribution, and a reviewer that merely consumed
// the implementer's finding (ConsumedFindingIDs) is excluded from the
// INDEPENDENT counts via review.IndependentConfirmingObservations/
// IndependentRefutingObservations, reused directly rather than
// re-implementing independence logic here.
func FinalizeWithReview(obs PerformanceObservation, group review.ReviewGroup) PerformanceObservation {
	out := obs
	out.QualityOutcome = QualityFromFindingStatus(group.Status)
	out.ReviewGroupID = string(group.ID)

	reviewerSet := map[supervisor.AgentID]bool{}
	var reviewers []supervisor.AgentID
	for _, o := range group.Observations {
		if o.SourceAgentID == obs.AgentID {
			continue
		}
		if !reviewerSet[o.SourceAgentID] {
			reviewerSet[o.SourceAgentID] = true
			reviewers = append(reviewers, o.SourceAgentID)
		}
	}
	out.ReviewerAgentIDs = reviewers

	independentConfirms := review.IndependentConfirmingObservations(group)
	independentRefutes := review.IndependentRefutingObservations(group)
	out.Metrics.FindingCount = len(group.Observations)
	out.Metrics.IndependentConfirmationCount = len(independentConfirms)
	out.Metrics.EvidenceCount = 0
	for _, o := range group.Observations {
		out.Metrics.EvidenceCount += len(o.EvidenceRefs)
	}
	out.Metrics.ReviewRounds = group.ArbitrationRounds

	switch group.Status {
	case "verified":
		out.Metrics.VerifiedFindingCount = len(independentConfirms)
		out.Status = ObservationFinalized
	case "rejected":
		out.Metrics.RejectedFindingCount = len(independentRefutes)
		out.Status = ObservationFinalized
	default:
		out.Status = ObservationPending
	}

	out.UpdatedAt = time.Now().UTC()
	return out
}
