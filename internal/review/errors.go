package review

import "fmt"

// FailureClass mirrors internal/supervisor's pattern: Core branches on
// failure class, not error message text.
type FailureClass string

const (
	FailureClassCorrelation       FailureClass = "finding_correlation_failure"
	FailureClassReviewValidation  FailureClass = "review_validation_failure"
	FailureClassReviewAuth        FailureClass = "review_authorization_failure"
	FailureClassArbitration       FailureClass = "arbitration_failure"
	FailureClassReviewUnavail     FailureClass = "independent_review_unavailable"
	FailureClassEvidenceInsuff    FailureClass = "evidence_insufficient"
	FailureClassEvidenceConflict  FailureClass = "evidence_conflict"
	FailureClassStaleArtifact     FailureClass = "stale_artifact_evidence"
	FailureClassReviewLimit       FailureClass = "review_limit_exceeded"
	FailureClassMissingProvenance FailureClass = "missing_execution_provenance"
)

// FindingCorrelationFailure is returned when an observation cannot be
// correlated to any ReviewGroup and a new one cannot be created either
// (e.g. malformed subject/fingerprint inputs).
type FindingCorrelationFailure struct {
	FindingID string
	Reason    string
}

func (e *FindingCorrelationFailure) Error() string {
	return fmt.Sprintf("finding correlation failure for %q: %s", e.FindingID, e.Reason)
}
func (e *FindingCorrelationFailure) Class() FailureClass { return FailureClassCorrelation }

// ReviewValidationFailure covers structurally invalid Arbiter actions:
// unknown group/observation references, unknown enum values, malformed
// fields. Always fail-closed.
type ReviewValidationFailure struct {
	ActionIndex int
	Reason      string
}

func (e *ReviewValidationFailure) Error() string {
	return fmt.Sprintf("review validation failure at action %d: %s", e.ActionIndex, e.Reason)
}
func (e *ReviewValidationFailure) Class() FailureClass { return FailureClassReviewValidation }

// ReviewAuthorizationFailure is returned when a structurally valid
// action is refused by Core policy - e.g. an attempt to verify a claim
// without sufficient independent support, or a self-review/self-verify
// attempt.
type ReviewAuthorizationFailure struct {
	GroupID ReviewGroupID
	Reason  string
}

func (e *ReviewAuthorizationFailure) Error() string {
	return fmt.Sprintf("review authorization failure for group %q: %s", e.GroupID, e.Reason)
}
func (e *ReviewAuthorizationFailure) Class() FailureClass { return FailureClassReviewAuth }

// ArbitrationFailure is returned when the Arbiter itself fails (routing,
// inference, or decode failure) - distinct from a rejected proposal.
type ArbitrationFailure struct {
	GroupID ReviewGroupID
	Reason  string
}

func (e *ArbitrationFailure) Error() string {
	return fmt.Sprintf("arbitration failure for group %q: %s", e.GroupID, e.Reason)
}
func (e *ArbitrationFailure) Class() FailureClass { return FailureClassArbitration }

// IndependentReviewUnavailable is returned when no eligible, independent
// reviewer can be found for a RequestIndependentReviewProposal (e.g. the
// only capable agent is the original claimant).
type IndependentReviewUnavailable struct {
	GroupID ReviewGroupID
	Reason  string
}

func (e *IndependentReviewUnavailable) Error() string {
	return fmt.Sprintf("independent review unavailable for group %q: %s", e.GroupID, e.Reason)
}
func (e *IndependentReviewUnavailable) Class() FailureClass { return FailureClassReviewUnavail }

// EvidenceInsufficient is returned when Core cannot reach a determination
// (verified/rejected) with the evidence currently on file - the correct
// outcome is protocol.FindingNeedsEvidence, not a forced verdict.
type EvidenceInsufficient struct {
	GroupID ReviewGroupID
	Reason  string
}

func (e *EvidenceInsufficient) Error() string {
	return fmt.Sprintf("evidence insufficient for group %q: %s", e.GroupID, e.Reason)
}
func (e *EvidenceInsufficient) Class() FailureClass { return FailureClassEvidenceInsuff }

// EvidenceConflict is returned/reported when independent observations
// present incompatible, individually-valid claims - the correct outcome
// is protocol.FindingContested, never "pick the stronger model."
type EvidenceConflict struct {
	GroupID ReviewGroupID
	Reason  string
}

func (e *EvidenceConflict) Error() string {
	return fmt.Sprintf("evidence conflict for group %q: %s", e.GroupID, e.Reason)
}
func (e *EvidenceConflict) Class() FailureClass { return FailureClassEvidenceConflict }

// StaleArtifactEvidence is returned when an observation or a proposed
// verification references an ArtifactVersion that no longer matches the
// ReviewGroup's current ArtifactVersion - preventing a claim against an
// old commit from being verified using a review of a different one.
type StaleArtifactEvidence struct {
	GroupID         ReviewGroupID
	ExpectedVersion ArtifactVersion
	ActualVersion   ArtifactVersion
}

func (e *StaleArtifactEvidence) Error() string {
	return fmt.Sprintf("stale artifact evidence for group %q: expected version %q, got %q", e.GroupID, e.ExpectedVersion, e.ActualVersion)
}
func (e *StaleArtifactEvidence) Class() FailureClass { return FailureClassStaleArtifact }

// ReviewLimitExceeded is returned when a Core-configured ReviewLimits
// guardrail (MaxArbitrationRounds, MaxEvidenceRequests,
// MaxIndependentReviews, MaxReviewDepth, MaxReviewCost) would be
// exceeded. Never read from Arbiter output.
type ReviewLimitExceeded struct {
	Guardrail string
	Limit     int
	Actual    int
}

func (e *ReviewLimitExceeded) Error() string {
	return fmt.Sprintf("review guardrail %q exceeded: limit=%d actual=%d", e.Guardrail, e.Limit, e.Actual)
}
func (e *ReviewLimitExceeded) Class() FailureClass { return FailureClassReviewLimit }

// --- Phase 5: authoritative evidence errors ---

const (
	FailureClassEvidenceNotFound         FailureClass = "evidence_not_found"
	FailureClassEvidenceTenant           FailureClass = "evidence_tenant_mismatch"
	FailureClassEvidenceArtifact         FailureClass = "evidence_artifact_mismatch"
	FailureClassEvidenceVersion          FailureClass = "evidence_version_mismatch"
	FailureClassEvidenceExecution        FailureClass = "evidence_execution_mismatch"
	FailureClassEvidenceIntegrity        FailureClass = "evidence_integrity_failure"
	FailureClassEvidenceRevoked          FailureClass = "evidence_revoked"
	FailureClassEvidenceStale            FailureClass = "evidence_stale"
	FailureClassEvidenceUnauthorized     FailureClass = "evidence_unauthorized"
	FailureClassEvidenceIdentityConflict FailureClass = "evidence_identity_conflict"
	FailureClassProjection               FailureClass = "finding_projection_failure"
)

// EvidenceNotFound is returned when an EvidenceRef does not resolve to
// any record in the authoritative EvidenceStore. A reference by name
// alone is never trusted - this is the fail-closed outcome for an
// unknown, forged, or mistyped ID.
type EvidenceNotFound struct {
	ID     EvidenceID
	Reason string
}

func (e *EvidenceNotFound) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("evidence %q not found: %s", e.ID, e.Reason)
	}
	return fmt.Sprintf("evidence %q not found", e.ID)
}
func (e *EvidenceNotFound) Class() FailureClass { return FailureClassEvidenceNotFound }

// EvidenceTenantMismatch is returned when a resolved EvidenceRecord
// belongs to a different tenant than the review group referencing it -
// evidence is never trusted or correlated across a tenant boundary.
type EvidenceTenantMismatch struct {
	ID       EvidenceID
	Expected string
	Actual   string
}

func (e *EvidenceTenantMismatch) Error() string {
	return fmt.Sprintf("evidence %q tenant mismatch: expected %q, got %q", e.ID, e.Expected, e.Actual)
}
func (e *EvidenceTenantMismatch) Class() FailureClass { return FailureClassEvidenceTenant }

// EvidenceArtifactMismatch is returned when a resolved EvidenceRecord's
// Artifact does not match what the claim is about.
type EvidenceArtifactMismatch struct {
	ID       EvidenceID
	Expected string
	Actual   string
}

func (e *EvidenceArtifactMismatch) Error() string {
	return fmt.Sprintf("evidence %q artifact mismatch: expected %q, got %q", e.ID, e.Expected, e.Actual)
}
func (e *EvidenceArtifactMismatch) Class() FailureClass { return FailureClassEvidenceArtifact }

// EvidenceVersionMismatch is returned when a resolved EvidenceRecord's
// ArtifactVersion does not match the ReviewGroup's ArtifactVersion -
// evidence from v2 can never authorize a decision about a claim scoped
// to v1, or vice versa.
type EvidenceVersionMismatch struct {
	ID       EvidenceID
	Expected ArtifactVersion
	Actual   ArtifactVersion
}

func (e *EvidenceVersionMismatch) Error() string {
	return fmt.Sprintf("evidence %q artifact-version mismatch: expected %q, got %q", e.ID, e.Expected, e.Actual)
}
func (e *EvidenceVersionMismatch) Class() FailureClass { return FailureClassEvidenceVersion }

// EvidenceExecutionMismatch is returned when a resolved EvidenceRecord's
// TaskID/ExecutionID does not match what was expected.
type EvidenceExecutionMismatch struct {
	ID     EvidenceID
	Reason string
}

func (e *EvidenceExecutionMismatch) Error() string {
	return fmt.Sprintf("evidence %q execution mismatch: %s", e.ID, e.Reason)
}
func (e *EvidenceExecutionMismatch) Class() FailureClass { return FailureClassEvidenceExecution }

// EvidenceIntegrityFailure covers a missing/invalid integrity digest, an
// unacceptable evidence kind, or any other structural problem with a
// resolved record that prevents it from being trusted.
type EvidenceIntegrityFailure struct {
	Reason string
}

func (e *EvidenceIntegrityFailure) Error() string {
	return fmt.Sprintf("evidence integrity failure: %s", e.Reason)
}
func (e *EvidenceIntegrityFailure) Class() FailureClass { return FailureClassEvidenceIntegrity }

// EvidenceRevokedError is returned when a resolved EvidenceRecord has
// been explicitly revoked. Named with an "Error" suffix (unlike this
// package's EvidenceRevoked status constant) to avoid colliding with
// the EvidenceRecordStatus value of the same conceptual name.
type EvidenceRevokedError struct {
	ID EvidenceID
}

func (e *EvidenceRevokedError) Error() string {
	return fmt.Sprintf("evidence %q has been revoked", e.ID)
}
func (e *EvidenceRevokedError) Class() FailureClass { return FailureClassEvidenceRevoked }

// EvidenceStaleError is returned when a resolved EvidenceRecord has been
// superseded or has exceeded an expectation's MaxAge.
type EvidenceStaleError struct {
	ID     EvidenceID
	Reason string
}

func (e *EvidenceStaleError) Error() string {
	return fmt.Sprintf("evidence %q is stale: %s", e.ID, e.Reason)
}
func (e *EvidenceStaleError) Class() FailureClass { return FailureClassEvidenceStale }

// EvidenceUnauthorized is returned when a resolved EvidenceRecord exists
// and matches its bindings, but policy still refuses to let it authorize
// the requested transition (e.g. a producer agent not permitted to
// supply this evidence kind - reserved for future policy use; not
// currently triggered by any check in this phase, included so the
// taxonomy matches the request's required error list).
type EvidenceUnauthorized struct {
	ID     EvidenceID
	Reason string
}

func (e *EvidenceUnauthorized) Error() string {
	return fmt.Sprintf("evidence %q unauthorized: %s", e.ID, e.Reason)
}
func (e *EvidenceUnauthorized) Class() FailureClass { return FailureClassEvidenceUnauthorized }

// EvidenceIdentityConflict is returned when Put is called with an
// EvidenceID that already exists but with a different content digest or
// different bindings - a genuine identity conflict, never silently
// overwritten.
type EvidenceIdentityConflict struct {
	ID             EvidenceID
	ExistingDigest string
	NewDigest      string
}

func (e *EvidenceIdentityConflict) Error() string {
	return fmt.Sprintf("evidence identity conflict for %q: existing digest %q, new digest %q", e.ID, e.ExistingDigest, e.NewDigest)
}
func (e *EvidenceIdentityConflict) Class() FailureClass { return FailureClassEvidenceIdentityConflict }

// FindingProjectionFailure is returned when a ReviewGroup's status
// transition committed successfully but projecting it onto the
// corresponding protocol.FindingPayload.Status failed. The review
// transition is NOT rolled back - ReviewState remains authoritative and
// the projection is safely retryable (Project is idempotent), per this
// phase's documented eventual-consistency model across the two stores.
type FindingProjectionFailure struct {
	FindingID string
	Reason    string
}

func (e *FindingProjectionFailure) Error() string {
	return fmt.Sprintf("finding projection failure for %q: %s", e.FindingID, e.Reason)
}
func (e *FindingProjectionFailure) Class() FailureClass { return FailureClassProjection }

// --- Phase 5: review-limit errors (typed per-limit, per the request) ---

type IndependentReviewLimitExceeded struct{ Limit, Actual int }

func (e *IndependentReviewLimitExceeded) Error() string {
	return fmt.Sprintf("independent review limit exceeded: limit=%d actual=%d", e.Limit, e.Actual)
}
func (e *IndependentReviewLimitExceeded) Class() FailureClass { return FailureClassReviewLimit }

type ArbitrationRoundLimitExceeded struct{ Limit, Actual int }

func (e *ArbitrationRoundLimitExceeded) Error() string {
	return fmt.Sprintf("arbitration round limit exceeded: limit=%d actual=%d", e.Limit, e.Actual)
}
func (e *ArbitrationRoundLimitExceeded) Class() FailureClass { return FailureClassReviewLimit }

type EvidenceRequestLimitExceeded struct{ Limit, Actual int }

func (e *EvidenceRequestLimitExceeded) Error() string {
	return fmt.Sprintf("evidence request limit exceeded: limit=%d actual=%d", e.Limit, e.Actual)
}
func (e *EvidenceRequestLimitExceeded) Class() FailureClass { return FailureClassReviewLimit }

type ReviewDepthLimitExceeded struct{ Limit, Actual int }

func (e *ReviewDepthLimitExceeded) Error() string {
	return fmt.Sprintf("review depth limit exceeded: limit=%d actual=%d", e.Limit, e.Actual)
}
func (e *ReviewDepthLimitExceeded) Class() FailureClass { return FailureClassReviewLimit }

type ReviewCostLimitExceeded struct {
	Limit, Actual float64
}

func (e *ReviewCostLimitExceeded) Error() string {
	return fmt.Sprintf("review cost limit exceeded: limit=%.2f actual=%.2f", e.Limit, e.Actual)
}
func (e *ReviewCostLimitExceeded) Class() FailureClass { return FailureClassReviewLimit }

// MissingExecutionProvenance is returned by NewFindingObservationFromExecution
// (finding_provenance.go) when the Core-authoritative execution context it
// was given has no usable ExecutionID. ADR 0007's mandatory invariant: a
// finding without authoritative execution provenance must never
// contribute to joint quality attribution, and must never be given a
// guessed ExecutionID (by TaskID, by "latest," by timestamp, or by
// agent) to paper over the gap - it fails closed instead.
type MissingExecutionProvenance struct {
	FindingID string
	Reason    string
}

func (e *MissingExecutionProvenance) Error() string {
	return fmt.Sprintf("missing execution provenance for finding %q: %s", e.FindingID, e.Reason)
}
func (e *MissingExecutionProvenance) Class() FailureClass { return FailureClassMissingProvenance }
