package performance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// NonCanonicalPolicyIdentity is returned by ValidateExperimentPolicyIdentity
// when a policy descriptor does not use the canonical, deterministically
// computed fingerprint scheme (ADR 0009/0009D) - e.g. a historical
// "manual-v1" descriptor, or one with no fingerprint at all. Historical
// ManualV1 policies remain fully usable for historical
// evaluation/shadow/replay under existing semantics; they are simply
// not ADMITTED for any FUTURE real Challenger execution (Phase 10,
// not built here - this type only provides the admission CHECK).
type NonCanonicalPolicyIdentity struct {
	PolicyID          string
	PolicyVersion     string
	FingerprintScheme string
}

func (e *NonCanonicalPolicyIdentity) Error() string {
	return fmt.Sprintf("policy %s:%s uses fingerprint scheme %q, not %q - not eligible for experiment admission", e.PolicyID, e.PolicyVersion, e.FingerprintScheme, FingerprintSchemeCanonicalV2)
}

// PolicyNotExperimentEligible wraps a more specific cause (e.g.
// NonCanonicalPolicyIdentity) into the general "this policy cannot be
// admitted for future experimentation" verdict ADR 0009D Part 11
// requires as a distinct, nameable typed error.
type PolicyNotExperimentEligible struct {
	PolicyID      string
	PolicyVersion string
	Reason        error
}

func (e *PolicyNotExperimentEligible) Error() string {
	return fmt.Sprintf("policy %s:%s is not experiment-eligible: %v", e.PolicyID, e.PolicyVersion, e.Reason)
}
func (e *PolicyNotExperimentEligible) Unwrap() error { return e.Reason }

// ValidateExperimentPolicyIdentity checks ONLY policy-identity
// admission for future real Challenger execution (Phase 10, NOT
// implemented here) - requires FingerprintScheme == CanonicalV2, i.e.
// the descriptor must have been built via NewRoutingPolicyDescriptor's
// deterministic factory, never a manual/caller-supplied fingerprint.
// This function performs NO execution, NO traffic allocation, and NO
// Challenger selection - it is a pure, read-only admission check.
func ValidateExperimentPolicyIdentity(descriptor RoutingPolicyDescriptor) error {
	if descriptor.FingerprintScheme != FingerprintSchemeCanonicalV2 {
		return &PolicyNotExperimentEligible{
			PolicyID: string(descriptor.ID), PolicyVersion: descriptor.Version,
			Reason: &NonCanonicalPolicyIdentity{PolicyID: string(descriptor.ID), PolicyVersion: descriptor.Version, FingerprintScheme: descriptor.FingerprintScheme},
		}
	}
	return nil
}

// ExperimentContextNotReady is returned by ValidateExperimentContext
// when the decision-scoped evaluation context required for future
// Challenger execution admission is incomplete - never because
// execution itself failed (none is ever attempted here).
type ExperimentContextNotReady struct {
	DecisionID supervisor.DecisionID
	Reason     string
}

func (e *ExperimentContextNotReady) Error() string {
	return fmt.Sprintf("experiment context not ready for decision %q: %s", e.DecisionID, e.Reason)
}

// ValidateExperimentContext is a READ-ONLY readiness check, codifying
// ADR 0009D's required future-Phase-10 invariant: "no future Challenger
// execution may be admitted unless DecisionID exists AND a decision-
// scoped CandidateSnapshot exists AND evaluation context exists AND
// policy identity uses CanonicalV2." It does exactly that check, and
// NOTHING else - no traffic allocation, no Challenger selection, no
// execution, no exploration-budget reservation. The actual execution
// gate is Phase 10's responsibility; this function only proves the
// PRECONDITION is checkable today.
//
// challengerAgentID, if non-empty, is also checked against the
// snapshot's candidate set - ADR 0009D Part 12's "Challenger Agent must
// later be in Snapshot" requirement, re-using CandidateSnapshot.Contains
// rather than duplicating membership logic.
func ValidateExperimentContext(ctx context.Context, snapshotStore CandidateSnapshotStore, evaluationStore EvaluationStore, tenant string, decisionID supervisor.DecisionID, challengerPolicy RoutingPolicyDescriptor, challengerAgentID supervisor.AgentID) error {
	if decisionID == "" {
		return &ExperimentContextNotReady{DecisionID: decisionID, Reason: "no DecisionID supplied"}
	}

	snapshot, found, err := snapshotStore.Get(ctx, tenant, decisionID)
	if err != nil {
		return err
	}
	if !found {
		return &ExperimentContextNotReady{DecisionID: decisionID, Reason: "no decision-scoped CandidateSnapshot exists"}
	}
	if snapshot.DecisionID != decisionID {
		// Defensive - a conforming store can never actually return this,
		// but a future non-conforming implementation must not be trusted
		// silently.
		return &ExperimentContextNotReady{DecisionID: decisionID, Reason: "snapshot does not belong to the requested decision"}
	}

	if err := ValidateExperimentPolicyIdentity(challengerPolicy); err != nil {
		return err
	}

	if challengerAgentID != "" && !snapshot.Contains(challengerAgentID) {
		return &ExperimentContextNotReady{DecisionID: decisionID, Reason: fmt.Sprintf("challenger agent %q is not in this decision's CandidateSnapshot", challengerAgentID)}
	}

	all, err := evaluationStore.Query(ctx, EvaluationFilter{Tenant: tenant})
	if err != nil {
		return err
	}
	found = false
	for _, rec := range all {
		if rec.DecisionID == decisionID {
			found = true
			break
		}
	}
	if !found {
		return &ExperimentContextNotReady{DecisionID: decisionID, Reason: "no evaluation context was captured for this decision"}
	}

	return nil
}

// ClassifyReadinessError maps a ValidateExperimentContext/
// ValidateExperimentPolicyIdentity error onto the same bounded
// ExperimentEligibilityReason enum ExperimentController.Admit uses, so
// callers can audit WHY a decision never reached admission with a typed
// reason rather than a free-form error string.
func ClassifyReadinessError(err error) ExperimentEligibilityReason {
	if err == nil {
		return EligibilityEligible
	}
	var notReady *ExperimentContextNotReady
	if errors.As(err, &notReady) {
		switch {
		case strings.Contains(notReady.Reason, "no DecisionID"):
			return EligibilityMissingDecisionContext
		case strings.Contains(notReady.Reason, "CandidateSnapshot exists") || strings.Contains(notReady.Reason, "snapshot does not belong"):
			return EligibilityMissingDecisionContext
		case strings.Contains(notReady.Reason, "not in this decision's CandidateSnapshot"):
			return EligibilityCandidateNotInSnapshot
		case strings.Contains(notReady.Reason, "evaluation context"):
			return EligibilityMissingEvaluationContext
		}
	}
	var notEligible *PolicyNotExperimentEligible
	if errors.As(err, &notEligible) {
		return EligibilityNonCanonicalPolicy
	}
	return EligibilityControlPlaneUnavailable
}
