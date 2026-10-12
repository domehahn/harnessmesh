package performance

import "fmt"

// FailureClass mirrors the pattern established in
// internal/supervisor/internal/review: Core branches on failure class,
// not error message text.
type FailureClass string

const (
	FailureClassAttribution      FailureClass = "attribution_failure"
	FailureClassDuplicate        FailureClass = "duplicate_observation"
	FailureClassUnknownTaskClass FailureClass = "unknown_task_class"
	FailureClassUnknownAgent     FailureClass = "unknown_agent"
	FailureClassStaleOutcome     FailureClass = "stale_review_outcome"
	FailureClassCrossTenant      FailureClass = "cross_tenant_query_denied"
	FailureClassMalformedMetrics FailureClass = "malformed_metrics"
	FailureClassNotFound         FailureClass = "observation_not_found"
)

// AttributionFailure is returned when an outcome cannot be attributed to
// a specific agent/model from authoritative state (e.g. no assignment
// record exists for the task) - never silently attributed to "whichever
// agent's output we happened to be looking at."
type AttributionFailure struct {
	TaskID string
	Reason string
}

func (e *AttributionFailure) Error() string {
	return fmt.Sprintf("attribution failure for task %q: %s", e.TaskID, e.Reason)
}
func (e *AttributionFailure) Class() FailureClass { return FailureClassAttribution }

// DuplicateObservation is returned when Save is called with an
// ObservationID that already exists and the new record is not an exact
// idempotent replay - see MemoryPerformanceStore.Save. A duplicate event
// must never inflate metrics by being counted twice.
type DuplicateObservation struct {
	ID ObservationID
}

func (e *DuplicateObservation) Error() string {
	return fmt.Sprintf("duplicate observation %q", e.ID)
}
func (e *DuplicateObservation) Class() FailureClass { return FailureClassDuplicate }

// UnknownTaskClass is returned when an observation references a
// TaskClass outside the registered taxonomy - never an unbounded
// free-text value.
type UnknownTaskClass struct {
	TaskClass TaskClass
}

func (e *UnknownTaskClass) Error() string {
	return fmt.Sprintf("unknown task class %q", e.TaskClass)
}
func (e *UnknownTaskClass) Class() FailureClass { return FailureClassUnknownTaskClass }

// UnknownAgent is returned when an observation references an AgentID
// with no corresponding authoritative record - this package never
// invents agent identities.
type UnknownAgent struct {
	AgentID string
}

func (e *UnknownAgent) Error() string       { return fmt.Sprintf("unknown agent %q", e.AgentID) }
func (e *UnknownAgent) Class() FailureClass { return FailureClassUnknownAgent }

// StaleReviewOutcome is returned when an attempt to finalize an
// observation uses a review.ReviewGroup snapshot that is no longer the
// group's current state (e.g. it was superseded/updated after the
// snapshot was taken) - finalization must act on fresh state, exactly
// like ADR 0002/0004's TOCTOU discipline.
type StaleReviewOutcome struct {
	ReviewGroupID string
}

func (e *StaleReviewOutcome) Error() string {
	return fmt.Sprintf("stale review outcome for group %q", e.ReviewGroupID)
}
func (e *StaleReviewOutcome) Class() FailureClass { return FailureClassStaleOutcome }

// CrossTenantQueryDenied is returned when a Query is attempted without
// an explicit tenant scope and without explicitly opting into an
// all-tenant query - fail closed, mirroring ADR 0002 Phase 2B's tenant
// discipline.
type CrossTenantQueryDenied struct {
	Reason string
}

func (e *CrossTenantQueryDenied) Error() string {
	return fmt.Sprintf("cross-tenant query denied: %s", e.Reason)
}
func (e *CrossTenantQueryDenied) Class() FailureClass { return FailureClassCrossTenant }

// MalformedMetrics is returned when PerformanceMetrics/timing fields are
// structurally invalid (e.g. negative duration, FinishedAt before
// StartedAt, negative counts) - never silently clamped or ignored.
type MalformedMetrics struct {
	Reason string
}

func (e *MalformedMetrics) Error() string       { return fmt.Sprintf("malformed metrics: %s", e.Reason) }
func (e *MalformedMetrics) Class() FailureClass { return FailureClassMalformedMetrics }

// ObservationNotFound is returned when Get/Finalize references an
// ObservationID that does not exist.
type ObservationNotFound struct {
	ID ObservationID
}

func (e *ObservationNotFound) Error() string       { return fmt.Sprintf("observation %q not found", e.ID) }
func (e *ObservationNotFound) Class() FailureClass { return FailureClassNotFound }
