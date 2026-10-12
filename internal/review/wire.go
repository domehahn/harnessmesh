package review

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// This file is the strict decode/validation boundary between untrusted
// Arbiter LLM text output and this package's trusted domain types
// (ArbiterAction, ArbitrationProposal) - the exact same role
// internal/supervisor/wire.go plays for Supervisor output, reusing its
// already-fuzzed DetectDuplicateKeys and ValidID helpers rather than
// duplicating them.

// wireLimits bounds every array/string an Arbiter's wire response may
// contain. Core-owned, never Arbiter-owned.
type wireLimits struct {
	MaxResponseBytes   int
	MaxActionsPerBatch int
	MaxCapabilities    int
	MaxRationaleLength int
	MaxJSONDepth       int
}

func defaultWireLimits() wireLimits {
	return wireLimits{
		MaxResponseBytes:   256 * 1024,
		MaxActionsPerBatch: 16,
		MaxCapabilities:    16,
		MaxRationaleLength: MaxRationaleLength,
		MaxJSONDepth:       32,
	}
}

// WireAction is the untrusted wire representation of exactly one
// ArbiterAction.
type WireAction struct {
	Type                 string   `json:"type"`
	GroupID              string   `json:"group_id"`
	ObservationID        string   `json:"observation_id,omitempty"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	RationaleSummary     string   `json:"rationale_summary"`
}

// WireArbitrationResponse is the top-level schema for LLMArbiter output.
type WireArbitrationResponse struct {
	ClaimID          string       `json:"claim_id"`
	Assessment       string       `json:"assessment"`
	Proposals        []WireAction `json:"proposals"`
	RationaleSummary string       `json:"rationale_summary"`
}

func decodeStrict(raw []byte, limits wireLimits, out any) error {
	if len(raw) > limits.MaxResponseBytes {
		return &ArbitrationFailure{Reason: fmt.Sprintf("response exceeds %d bytes", limits.MaxResponseBytes)}
	}
	if err := supervisor.DetectDuplicateKeys(raw, limits.MaxJSONDepth); err != nil {
		return &ArbitrationFailure{Reason: fmt.Sprintf("duplicate keys or invalid nesting: %v", err)}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return &ArbitrationFailure{Reason: fmt.Sprintf("json decode: %v", err)}
	}
	if dec.More() {
		return &ArbitrationFailure{Reason: "trailing data after JSON value"}
	}
	return nil
}

// DecodeArbitrationResponse strictly decodes an Arbiter's raw model
// response and converts it into a trusted ArbitrationProposal. Any
// syntactic or schema violation fails closed - decode never "repairs" a
// malformed response into a usable action, and never returns a partial
// proposal alongside an error.
func DecodeArbitrationResponse(raw []byte, groupID ReviewGroupID) (ArbitrationProposal, error) {
	limits := defaultWireLimits()
	var wire WireArbitrationResponse
	if err := decodeStrict(raw, limits, &wire); err != nil {
		return ArbitrationProposal{}, err
	}

	if !supervisor.ValidID(wire.ClaimID) {
		return ArbitrationProposal{}, &ArbitrationFailure{GroupID: groupID, Reason: fmt.Sprintf("malformed claim_id %q", wire.ClaimID)}
	}
	if ReviewGroupID(wire.ClaimID) != groupID {
		return ArbitrationProposal{}, &ArbitrationFailure{GroupID: groupID, Reason: fmt.Sprintf("claim_id %q does not match the requested group %q", wire.ClaimID, groupID)}
	}
	if !knownAssessmentHints[AssessmentHint(wire.Assessment)] {
		return ArbitrationProposal{}, &ArbitrationFailure{GroupID: groupID, Reason: fmt.Sprintf("unknown assessment %q", wire.Assessment)}
	}
	if len(wire.RationaleSummary) > limits.MaxRationaleLength {
		return ArbitrationProposal{}, &ArbitrationFailure{GroupID: groupID, Reason: "rationale_summary too long"}
	}
	if len(wire.Proposals) > limits.MaxActionsPerBatch {
		return ArbitrationProposal{}, &ArbitrationFailure{GroupID: groupID, Reason: fmt.Sprintf("proposal count %d exceeds limit %d", len(wire.Proposals), limits.MaxActionsPerBatch)}
	}

	actions := make([]ArbiterAction, 0, len(wire.Proposals))
	for i, w := range wire.Proposals {
		action, err := toDomainAction(groupID, i, w, limits)
		if err != nil {
			return ArbitrationProposal{}, err
		}
		actions = append(actions, action)
	}

	return ArbitrationProposal{
		GroupID:          groupID,
		Assessment:       AssessmentHint(wire.Assessment),
		Actions:          actions,
		RationaleSummary: RationaleSummary(wire.RationaleSummary),
	}, nil
}

// toDomainAction is the explicit, testable wire-to-domain conversion
// boundary - untrusted WireAction JSON in, trusted ArbiterAction out, or
// a typed failure. Never a best-guess value.
func toDomainAction(groupID ReviewGroupID, index int, w WireAction, limits wireLimits) (ArbiterAction, error) {
	if len(w.RationaleSummary) > limits.MaxRationaleLength {
		return nil, &ArbitrationFailure{GroupID: groupID, Reason: fmt.Sprintf("proposal %d: rationale_summary too long", index)}
	}
	if w.GroupID != "" && w.GroupID != string(groupID) {
		return nil, &ArbitrationFailure{GroupID: groupID, Reason: fmt.Sprintf("proposal %d: group_id %q does not match the requested group", index, w.GroupID)}
	}
	rationale := RationaleSummary(w.RationaleSummary)

	switch ArbiterActionType(w.Type) {
	case ActionSupportClaim:
		if !supervisor.ValidID(w.ObservationID) {
			return nil, &ArbitrationFailure{GroupID: groupID, Reason: fmt.Sprintf("proposal %d: support_claim requires a valid observation_id", index)}
		}
		return SupportClaimAction{GroupID: groupID, ObservationID: TaskID(w.ObservationID), RationaleSummary: rationale}, nil

	case ActionContestClaim:
		if !supervisor.ValidID(w.ObservationID) {
			return nil, &ArbitrationFailure{GroupID: groupID, Reason: fmt.Sprintf("proposal %d: contest_claim requires a valid observation_id", index)}
		}
		return ContestClaimAction{GroupID: groupID, ObservationID: TaskID(w.ObservationID), RationaleSummary: rationale}, nil

	case ActionRequestEvidence:
		return RequestEvidenceAction{GroupID: groupID, RationaleSummary: rationale}, nil

	case ActionRequestIndependentReview:
		if len(w.RequiredCapabilities) == 0 {
			return nil, &ArbitrationFailure{GroupID: groupID, Reason: fmt.Sprintf("proposal %d: request_independent_review requires required_capabilities", index)}
		}
		if len(w.RequiredCapabilities) > limits.MaxCapabilities {
			return nil, &ArbitrationFailure{GroupID: groupID, Reason: fmt.Sprintf("proposal %d: too many required_capabilities", index)}
		}
		return RequestIndependentReviewAction{
			GroupID:              groupID,
			RequiredCapabilities: append([]string(nil), w.RequiredCapabilities...),
			RationaleSummary:     rationale,
		}, nil

	case ActionRejectClaim:
		return RejectClaimAction{GroupID: groupID, RationaleSummary: rationale}, nil

	case ActionRecommendVerification:
		return RecommendVerificationAction{GroupID: groupID, RationaleSummary: rationale}, nil

	default:
		return nil, &ArbitrationFailure{GroupID: groupID, Reason: fmt.Sprintf("proposal %d: unknown proposal type %q", index, w.Type)}
	}
}
