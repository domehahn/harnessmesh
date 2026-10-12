package review

import (
	"context"
	"fmt"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

// MinIndependentConfirmationsForVerification is the Core-owned policy
// threshold below which a claim can never be verified, regardless of
// what an Arbiter recommends. It intentionally matches
// DeriveStatus's "Supported" threshold (>=2) - verification requires at
// least as much independent corroboration as "Supported" already does,
// plus the additional checks verifyClaim performs (zero independent
// refutes, artifact-version freshness). This is Core configuration, not
// something an Arbiter or Supervisor proposal can lower.
const MinIndependentConfirmationsForVerification = 2

// MinIndependentRefutationsForRejection is the symmetric threshold for
// rejecting a claim - deliberately equal to
// MinIndependentConfirmationsForVerification. A single agent, even the
// one that performed a remediation, can never be the sole authority that
// verifies its own fix: rejecting the original claim requires the same
// bar as verifying it would have.
const MinIndependentRefutationsForRejection = 2

// ReviewAuthorizer is Core's policy boundary for Arbiter actions,
// mirroring supervisor.Authorizer's role: a structurally valid action
// (passed ReviewValidator) is NOT yet authorized. Only
// ReviewAuthorizer/verifyClaim decide whether a claim actually becomes
// protocol.FindingVerified or protocol.FindingRejected.
type ReviewAuthorizer struct {
	// Evidence is consulted, if non-nil, to validate every EvidenceRef
	// carried by an observation being counted toward a verification or
	// rejection decision - re-fetched fresh from the authoritative store
	// every time (never a cached/snapshotted record), closing the TOCTOU
	// window between when an Arbiter saw a reference and when this
	// authorization actually runs. A nil Evidence preserves Phase 4's
	// original behavior exactly (no evidence-reference checking) - set
	// for callers/tests that predate Phase 5 and never populate
	// EvidenceRefs at all.
	Evidence EvidenceValidator
}

// NewReviewAuthorizer constructs a ReviewAuthorizer with no evidence
// validation configured (Phase 4 behavior). Use
// NewReviewAuthorizerWithEvidence for Phase 5's evidence-checked path.
func NewReviewAuthorizer() *ReviewAuthorizer { return &ReviewAuthorizer{} }

// NewReviewAuthorizerWithEvidence constructs a ReviewAuthorizer that
// validates every EvidenceRef an observation carries against the
// authoritative EvidenceStore (via validator) before letting that
// observation count toward a verification or rejection decision.
func NewReviewAuthorizerWithEvidence(validator EvidenceValidator) *ReviewAuthorizer {
	return &ReviewAuthorizer{Evidence: validator}
}

// Authorize checks one action against the fresh ReviewGroup state. It is
// called from inside ReviewState's atomic apply (state.go), under the
// same lock that will perform the mutation, closing the same
// validate/authorize-vs-apply TOCTOU window the Phase 2A/2B closure
// review found and fixed in internal/supervisor.
func (a *ReviewAuthorizer) Authorize(ctx context.Context, group ReviewGroup, action ArbiterAction) (protocol.FindingStatus, error) {
	switch action.(type) {
	case RecommendVerificationAction:
		status, err := a.verifyClaim(ctx, group)
		if err != nil {
			return "", err
		}
		if status != protocol.FindingVerified {
			return "", &EvidenceInsufficient{GroupID: group.ID, Reason: fmt.Sprintf("verification not authorized: resulting status would be %q, not verified", status)}
		}
		return status, nil

	case RejectClaimAction:
		status, err := a.rejectClaim(ctx, group)
		if err != nil {
			return "", err
		}
		return status, nil

	case RequestEvidenceAction:
		return protocol.FindingNeedsEvidence, nil

	default:
		// SupportClaimAction/ContestClaimAction/RequestIndependentReviewAction
		// are audit-only or scheduling-only (see scheduling.go) - they
		// never themselves change group.Status. Returning the group's
		// current status is a no-op signal to the caller.
		return group.Status, nil
	}
}

// verifyClaim is the evidence-driven check that decides whether a claim
// may actually be marked protocol.FindingVerified. It is deliberately
// independent of whatever an Arbiter recommended - a
// RecommendVerificationAction is only ever a trigger to RUN this check,
// never a shortcut past it.
func (a *ReviewAuthorizer) verifyClaim(ctx context.Context, group ReviewGroup) (protocol.FindingStatus, error) {
	if group.Status == protocol.FindingVerified || group.Status == protocol.FindingRejected {
		// Already terminal; re-verification is a no-op, not an error -
		// Core does not re-litigate a settled claim just because a new
		// RecommendVerificationAction arrived.
		return group.Status, nil
	}

	confirms := IndependentConfirmingObservations(group)
	refutes := IndependentRefutingObservations(group)

	if err := a.checkArtifactFreshness(group, confirms); err != nil {
		return "", err
	}
	if err := a.checkEvidence(ctx, group, confirms); err != nil {
		return "", err
	}

	if len(refutes) > 0 {
		return "", &EvidenceConflict{GroupID: group.ID, Reason: fmt.Sprintf("%d independent refuting observation(s) outstanding - cannot verify a contested claim", len(refutes))}
	}
	if len(confirms) < MinIndependentConfirmationsForVerification {
		return "", &EvidenceInsufficient{GroupID: group.ID, Reason: fmt.Sprintf("only %d independent confirming observation(s), need %d", len(confirms), MinIndependentConfirmationsForVerification)}
	}
	return protocol.FindingVerified, nil
}

// rejectClaim is the symmetric check for moving a claim to
// protocol.FindingRejected.
func (a *ReviewAuthorizer) rejectClaim(ctx context.Context, group ReviewGroup) (protocol.FindingStatus, error) {
	if group.Status == protocol.FindingVerified || group.Status == protocol.FindingRejected {
		return group.Status, nil
	}

	confirms := IndependentConfirmingObservations(group)
	refutes := IndependentRefutingObservations(group)

	if err := a.checkArtifactFreshness(group, refutes); err != nil {
		return "", err
	}
	if err := a.checkEvidence(ctx, group, refutes); err != nil {
		return "", err
	}

	if len(confirms) > 0 {
		return "", &EvidenceConflict{GroupID: group.ID, Reason: fmt.Sprintf("%d independent confirming observation(s) outstanding - cannot reject a contested claim", len(confirms))}
	}
	if len(refutes) < MinIndependentRefutationsForRejection {
		return "", &EvidenceInsufficient{GroupID: group.ID, Reason: fmt.Sprintf("only %d independent refuting observation(s), need %d", len(refutes), MinIndependentRefutationsForRejection)}
	}
	return protocol.FindingRejected, nil
}

// checkEvidence validates every EvidenceRef carried by the given
// observations against the authoritative EvidenceStore (via a.Evidence),
// re-fetching fresh every call - never trusting a snapshot. If
// a.Evidence is nil, this is a no-op (Phase 4 compatibility: callers
// that never configured evidence validation, and observations that
// carry no EvidenceRefs at all, are unaffected). An unknown, mismatched,
// revoked, or stale EvidenceRef fails the WHOLE verification/rejection
// attempt closed - evidence is never silently excluded and the decision
// quietly reached anyway on fewer observations than were actually used.
func (a *ReviewAuthorizer) checkEvidence(ctx context.Context, group ReviewGroup, observations []FindingObservation) error {
	if a.Evidence == nil {
		return nil
	}
	expectation := EvidenceExpectation{Tenant: group.Tenant, ArtifactVersion: group.ArtifactVersion}
	for _, obs := range observations {
		for _, ref := range obs.EvidenceRefs {
			if _, err := a.Evidence.Validate(ctx, EvidenceID(ref), expectation); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkArtifactFreshness re-verifies, independently of FindGroup's
// join-time check, that every observation about to be used for a
// verification/rejection decision still matches the group's current
// ArtifactVersion. A claim against artifact version A must never be
// verified using a review performed against version B, even if that
// review somehow ended up correlated into the same group (e.g. via a
// future code path that does not go through FindGroup).
func (a *ReviewAuthorizer) checkArtifactFreshness(group ReviewGroup, observations []FindingObservation) error {
	for _, obs := range observations {
		if obs.ArtifactVersion != group.ArtifactVersion {
			return &StaleArtifactEvidence{GroupID: group.ID, ExpectedVersion: group.ArtifactVersion, ActualVersion: obs.ArtifactVersion}
		}
	}
	return nil
}
