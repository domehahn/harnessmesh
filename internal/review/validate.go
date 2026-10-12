package review

import "fmt"

// ReviewValidator is the structural, fail-closed gate every Arbiter
// action batch must pass before Authorize/apply ever runs - mirroring
// supervisor.Validator's role exactly. It never calls an LLM and never
// mutates anything.
type ReviewValidator struct {
	Limits ReviewLimits
}

// NewReviewValidator constructs a ReviewValidator with explicit limits.
func NewReviewValidator(limits ReviewLimits) *ReviewValidator {
	return &ReviewValidator{Limits: limits}
}

// ValidateActions checks a batch of ArbiterActions against the current
// (fresh) ReviewGroup state. All-or-nothing: the first invalid action
// aborts the whole batch.
func (v *ReviewValidator) ValidateActions(group ReviewGroup, actions []ArbiterAction) error {
	evidenceRequestsThisBatch := 0

	for i, a := range actions {
		if a == nil {
			return &ReviewValidationFailure{ActionIndex: i, Reason: "nil action"}
		}
		if !knownActionTypes[a.ActionType()] {
			return &ReviewValidationFailure{ActionIndex: i, Reason: fmt.Sprintf("unknown action type %q", a.ActionType())}
		}

		switch act := a.(type) {
		case SupportClaimAction:
			if err := v.validateRationale(i, act.RationaleSummary); err != nil {
				return err
			}
			if act.GroupID != group.ID {
				return &ReviewValidationFailure{ActionIndex: i, Reason: "group_id mismatch"}
			}
			if _, ok := group.ObservationByID(act.ObservationID); !ok {
				return &ReviewValidationFailure{ActionIndex: i, Reason: fmt.Sprintf("unknown observation %q", act.ObservationID)}
			}

		case ContestClaimAction:
			if err := v.validateRationale(i, act.RationaleSummary); err != nil {
				return err
			}
			if act.GroupID != group.ID {
				return &ReviewValidationFailure{ActionIndex: i, Reason: "group_id mismatch"}
			}
			if _, ok := group.ObservationByID(act.ObservationID); !ok {
				return &ReviewValidationFailure{ActionIndex: i, Reason: fmt.Sprintf("unknown observation %q", act.ObservationID)}
			}

		case RequestEvidenceAction:
			if err := v.validateRationale(i, act.RationaleSummary); err != nil {
				return err
			}
			if act.GroupID != group.ID {
				return &ReviewValidationFailure{ActionIndex: i, Reason: "group_id mismatch"}
			}
			evidenceRequestsThisBatch++

		case RequestIndependentReviewAction:
			if err := v.validateRationale(i, act.RationaleSummary); err != nil {
				return err
			}
			if act.GroupID != group.ID {
				return &ReviewValidationFailure{ActionIndex: i, Reason: "group_id mismatch"}
			}
			if len(act.RequiredCapabilities) == 0 {
				return &ReviewValidationFailure{ActionIndex: i, Reason: "request_independent_review requires at least one required_capability"}
			}

		case RejectClaimAction:
			if err := v.validateRationale(i, act.RationaleSummary); err != nil {
				return err
			}
			if act.GroupID != group.ID {
				return &ReviewValidationFailure{ActionIndex: i, Reason: "group_id mismatch"}
			}

		case RecommendVerificationAction:
			if err := v.validateRationale(i, act.RationaleSummary); err != nil {
				return err
			}
			if act.GroupID != group.ID {
				return &ReviewValidationFailure{ActionIndex: i, Reason: "group_id mismatch"}
			}

		default:
			return &ReviewValidationFailure{ActionIndex: i, Reason: fmt.Sprintf("unhandled action concrete type %T", act)}
		}
	}

	if v.Limits.MaxEvidenceRequests > 0 {
		total := group.EvidenceRequests + evidenceRequestsThisBatch
		if total > v.Limits.MaxEvidenceRequests {
			return &EvidenceRequestLimitExceeded{Limit: v.Limits.MaxEvidenceRequests, Actual: total}
		}
	}
	if len(actions) > 0 && v.Limits.MaxArbitrationRounds > 0 {
		total := group.ArbitrationRounds + 1
		if total > v.Limits.MaxArbitrationRounds {
			return &ArbitrationRoundLimitExceeded{Limit: v.Limits.MaxArbitrationRounds, Actual: total}
		}
	}
	if v.Limits.MaxReviewCost > 0 {
		batchCost := 0.0
		for _, a := range actions {
			batchCost += DefaultActionCost(a.ActionType())
		}
		total := group.CostSpent + batchCost
		if total > v.Limits.MaxReviewCost {
			return &ReviewCostLimitExceeded{Limit: v.Limits.MaxReviewCost, Actual: total}
		}
	}
	if v.Limits.MaxReviewDepth > 0 && group.Depth > v.Limits.MaxReviewDepth {
		return &ReviewDepthLimitExceeded{Limit: v.Limits.MaxReviewDepth, Actual: group.Depth}
	}

	return nil
}

func (v *ReviewValidator) validateRationale(index int, r RationaleSummary) error {
	if len(r) > MaxRationaleLength {
		return &ReviewValidationFailure{ActionIndex: index, Reason: "rationale_summary too long"}
	}
	return nil
}
