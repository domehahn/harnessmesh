package review

import (
	"context"
)

// ArbiterActionType enumerates the closed set of actions an Arbiter may
// propose. Mirrors supervisor.ProposalType's pattern.
type ArbiterActionType string

const (
	ActionSupportClaim             ArbiterActionType = "support_claim"
	ActionContestClaim             ArbiterActionType = "contest_claim"
	ActionRequestEvidence          ArbiterActionType = "request_evidence"
	ActionRequestIndependentReview ArbiterActionType = "request_independent_review"
	ActionRejectClaim              ArbiterActionType = "reject_claim"
	ActionRecommendVerification    ArbiterActionType = "recommend_verification"
)

var knownActionTypes = map[ArbiterActionType]bool{
	ActionSupportClaim:             true,
	ActionContestClaim:             true,
	ActionRequestEvidence:          true,
	ActionRequestIndependentReview: true,
	ActionRejectClaim:              true,
	ActionRecommendVerification:    true,
}

// RationaleSummary mirrors supervisor.RationaleSummary: a short,
// auditable justification - never raw chain-of-thought. Deliberately its
// own type, not an alias to supervisor.RationaleSummary, keeping
// internal/review's public surface self-contained; MaxRationaleLength
// below matches supervisor.MaxRationaleLength's value.
type RationaleSummary string

// MaxRationaleLength bounds every RationaleSummary this package accepts.
const MaxRationaleLength = 500

// ArbiterAction is the untrusted union of everything an Arbiter may
// propose. None of these carry a model/provider field, a capability
// grant, or a direct status mutation - see ReviewValidator/
// ReviewAuthorizer (validate.go/authorize.go) for what Core does with
// each.
type ArbiterAction interface {
	ActionType() ArbiterActionType
}

type SupportClaimAction struct {
	GroupID          ReviewGroupID
	ObservationID    TaskID
	RationaleSummary RationaleSummary
}

func (SupportClaimAction) ActionType() ArbiterActionType { return ActionSupportClaim }

type ContestClaimAction struct {
	GroupID          ReviewGroupID
	ObservationID    TaskID
	RationaleSummary RationaleSummary
}

func (ContestClaimAction) ActionType() ArbiterActionType { return ActionContestClaim }

type RequestEvidenceAction struct {
	GroupID          ReviewGroupID
	RationaleSummary RationaleSummary
}

func (RequestEvidenceAction) ActionType() ArbiterActionType { return ActionRequestEvidence }

// RequestIndependentReviewAction proposes that Core schedule a new,
// independent review task. RequiredCapabilities is advisory input to
// Scheduler's existing, unmodified capability-based routing (see
// scheduling.go's ToSchedulerProposal) - the Arbiter never names a
// concrete agent, consistent with "Der Arbiter darf keinen Agent hart
// auswählen."
type RequestIndependentReviewAction struct {
	GroupID              ReviewGroupID
	RequiredCapabilities []string
	RationaleSummary     RationaleSummary
}

func (RequestIndependentReviewAction) ActionType() ArbiterActionType {
	return ActionRequestIndependentReview
}

type RejectClaimAction struct {
	GroupID          ReviewGroupID
	RationaleSummary RationaleSummary
}

func (RejectClaimAction) ActionType() ArbiterActionType { return ActionRejectClaim }

// RecommendVerificationAction does NOT mean "Finding = verified". It
// means: the Arbiter recommends Core evaluate verification. Core
// (ReviewAuthorizer.verifyClaim) independently checks evidence,
// artifact-version freshness, and reviewer independence, and may still
// refuse (EvidenceInsufficient/StaleArtifactEvidence) even when this
// action is proposed.
type RecommendVerificationAction struct {
	GroupID          ReviewGroupID
	RationaleSummary RationaleSummary
}

func (RecommendVerificationAction) ActionType() ArbiterActionType {
	return ActionRecommendVerification
}

// ArbitrationRequest is the minimized, policy-safe context an Arbiter
// receives - never full repository content, full transcripts, secrets,
// or unrelated tenant data.
type ArbitrationRequest struct {
	Group        ReviewGroup
	Limits       ReviewLimits
	RecentEvents []string // short, bounded event summaries - never raw transcripts
}

// ArbitrationProposal is the Arbiter's untrusted output: an advisory
// assessment plus a bounded list of actions. Assessment is NEVER applied
// directly - it is informational, exactly like supervisor.Synthesis is
// for workflow completion (Core re-derives the real verdict itself in
// ReviewAuthorizer.verifyClaim).
type ArbitrationProposal struct {
	GroupID          ReviewGroupID
	Assessment       AssessmentHint
	Actions          []ArbiterAction
	RationaleSummary RationaleSummary
}

// AssessmentHint is the Arbiter's advisory read on a claim - structurally
// incapable of being "verified" (that value is deliberately excluded
// from this type's valid set; see wire.go's strict decode).
type AssessmentHint string

const (
	AssessmentUnconfirmed   AssessmentHint = "unconfirmed"
	AssessmentSupported     AssessmentHint = "supported"
	AssessmentContested     AssessmentHint = "contested"
	AssessmentNeedsEvidence AssessmentHint = "needs_evidence"
)

var knownAssessmentHints = map[AssessmentHint]bool{
	AssessmentUnconfirmed:   true,
	AssessmentSupported:     true,
	AssessmentContested:     true,
	AssessmentNeedsEvidence: true,
}

// Arbiter interprets, compares, and proposes - it never authorizes,
// never mutates ReviewState, and never selects a model/provider. See
// ADR 0002 Phase 4.
type Arbiter interface {
	Review(ctx context.Context, req ArbitrationRequest) (ArbitrationProposal, error)
}

// FakeArbiter is a deterministic, LLM-free Arbiter for tests and for
// proving the Core/Arbiter trust boundary independent of any real model -
// the same role FakeSupervisor played for Phases 1-3.
type FakeArbiter struct {
	ReviewFunc func(ctx context.Context, req ArbitrationRequest) (ArbitrationProposal, error)
}

func (f *FakeArbiter) Review(ctx context.Context, req ArbitrationRequest) (ArbitrationProposal, error) {
	if f.ReviewFunc == nil {
		return ArbitrationProposal{}, nil
	}
	return f.ReviewFunc(ctx, req)
}

var _ Arbiter = (*FakeArbiter)(nil)
