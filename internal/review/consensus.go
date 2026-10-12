package review

import "github.com/domehahn/harnessmesh/internal/protocol"

// DeriveStatus computes the deterministic, non-authoritative baseline
// status for a ReviewGroup from its current observations. This is the
// "agreement / conflict / insufficient evidence" semantics ADR 0002
// Phase 4 explicitly asks for WITHOUT probabilistic consensus or
// majority voting:
//
//   - agreement:  >=2 mutually independent StanceConfirms observations,
//     and zero independent StanceRefutes -> protocol.FindingSupported.
//     "Supported" is NOT "verified" - it is still only a label Core
//     attaches after counting independent corroboration; only
//     ReviewAuthorizer.verifyClaim (authorize.go), with its additional
//     evidence/freshness/policy checks, can move a group to
//     protocol.FindingVerified.
//   - conflict: at least one independent StanceConfirms AND at least one
//     independent StanceRefutes -> protocol.FindingContested.
//   - insufficient evidence (anything else: 0 or 1 independent
//     observations, or only independent refutes with no confirms) ->
//     protocol.FindingUnconfirmed. (NeedsEvidence is set separately, as
//     an explicit side effect of a failed verification attempt - see
//     authorize.go - not derived here, since "insufficient to derive a
//     status automatically" and "insufficient for the specific
//     verification just attempted" are different moments.)
//
// This function never returns protocol.FindingVerified or
// protocol.FindingRejected - those require Core's explicit,
// evidence-checked authorization path and are never auto-derived merely
// from observation counting (agreement is not authority).
func DeriveStatus(group ReviewGroup) protocol.FindingStatus {
	if group.Status == protocol.FindingVerified || group.Status == protocol.FindingRejected {
		// Terminal verdicts are never silently recomputed away by a new
		// observation arriving - only an explicit Core-authorized
		// transition changes a terminal status.
		return group.Status
	}

	confirms := IndependentConfirmingObservations(group)
	refutes := IndependentRefutingObservations(group)

	switch {
	case len(confirms) > 0 && len(refutes) > 0:
		return protocol.FindingContested
	case len(confirms) >= 2 && len(refutes) == 0:
		return protocol.FindingSupported
	default:
		return protocol.FindingUnconfirmed
	}
}
