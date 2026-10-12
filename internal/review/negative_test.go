package review

import (
	"context"
	"errors"
	"testing"
)

func TestNegative_CopiedFindingNotCountedAsIndependentReview(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))

	// agent-b has a DIFFERENT identity but explicitly consumed agent-a's
	// finding (F1) rather than inspecting the artifact itself.
	obsB := newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms)
	obsB.ConsumedFindingIDs = []string{"F1"}
	id2, err := state.Ingest(claim, "v1", "", obsB)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if id != id2 {
		t.Fatalf("expected same group")
	}
	group, _ := state.Group(id)
	if group.Status != protocol_FindingUnconfirmedLiteral {
		t.Fatalf("a copied finding must not count toward 'supported', expected unconfirmed, got %q", group.Status)
	}
}

func TestNegative_RemediationAgentCannotSoleVerifyItsOwnFix(t *testing.T) {
	// Models the "agent that performed remediation != sole verifier of
	// the remediation" requirement: the coding agent that fixed the code
	// also tries to be the one confirming the fix is good. Its
	// observation is correctly recorded, but - exactly like self-review
	// of the original claim - it is excluded from the independent set,
	// so it alone can never reach verification.
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v2-fixed", "", newObs(t, "O1", "F1", "coding-agent", "v2-fixed", StanceRefutes)) // "fix verified, issue absent" == refutes the original claim

	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state)
	_, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RejectClaimAction{GroupID: id, RationaleSummary: "coding-agent alone says it's fixed"},
	})
	var want *EvidenceInsufficient
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceInsufficient: a single agent (even the remediator) cannot sole-verify its own fix, got %v (%T)", err, err)
	}

	// A second, independent agent corroborates -> now rejection (of the
	// original claim) is authorized.
	state.Ingest(claim, "v2-fixed", "", newObs(t, "O2", "F2", "security-agent-c", "v2-fixed", StanceRefutes))
	group, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RejectClaimAction{GroupID: id, RationaleSummary: "independently corroborated absence of the issue"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if group.Status != protocol_FindingRejectedLiteral {
		t.Fatalf("expected rejected, got %q", group.Status)
	}
}

func TestNegative_CrossTenantFindingCorrelationBlocked(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	idA, _ := state.Ingest(claim, "v1", "tenant-a", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	idB, _ := state.Ingest(claim, "v1", "tenant-b", newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms))
	if idA == idB {
		t.Fatalf("observations from different tenants must never correlate into the same review group")
	}
}

func TestNegative_NonexistentGroupApplyFailsClosed(t *testing.T) {
	state := NewReviewState(nil)
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state)
	_, err := applier.Apply(context.Background(), "RG-999", []ArbiterAction{
		RequestEvidenceAction{GroupID: "RG-999", RationaleSummary: "r"},
	})
	var want *ReviewValidationFailure
	if !errors.As(err, &want) {
		t.Fatalf("expected ReviewValidationFailure for a nonexistent group, got %v (%T)", err, err)
	}
}

func TestNegative_InfiniteEvidenceRequestLoopBounded(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	limits := DefaultReviewLimits()
	limits.MaxEvidenceRequests = 2
	limits.MaxArbitrationRounds = 100 // isolate the evidence-request limit specifically
	applier := NewReviewApplier(NewReviewValidator(limits), NewReviewAuthorizer(), state)

	for i := 0; i < 2; i++ {
		if _, err := applier.Apply(context.Background(), id, []ArbiterAction{
			RequestEvidenceAction{GroupID: id, RationaleSummary: "need more"},
		}); err != nil {
			t.Fatalf("request %d should succeed under the limit: %v", i, err)
		}
	}
	_, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RequestEvidenceAction{GroupID: id, RationaleSummary: "one too many"},
	})
	var want *EvidenceRequestLimitExceeded
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceRequestLimitExceeded, got %v (%T)", err, err)
	}
}

func TestNegative_ArbitrationRoundsExceeded(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	limits := DefaultReviewLimits()
	limits.MaxArbitrationRounds = 2
	limits.MaxEvidenceRequests = 100
	applier := NewReviewApplier(NewReviewValidator(limits), NewReviewAuthorizer(), state)

	for i := 0; i < 2; i++ {
		if _, err := applier.Apply(context.Background(), id, []ArbiterAction{
			RequestEvidenceAction{GroupID: id, RationaleSummary: "round"},
		}); err != nil {
			t.Fatalf("round %d should succeed under the limit: %v", i, err)
		}
	}
	_, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RequestEvidenceAction{GroupID: id, RationaleSummary: "one too many rounds"},
	})
	var want *ArbitrationRoundLimitExceeded
	if !errors.As(err, &want) {
		t.Fatalf("expected ArbitrationRoundLimitExceeded, got %v (%T)", err, err)
	}
}

func TestNegative_DuplicateObservationsDoNotInflateSupportCount(t *testing.T) {
	// Replay/idempotency: delivering the SAME observation twice (e.g. a
	// retried MCP call) must not let it count as two independent
	// confirmations.
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	for i := 0; i < 5; i++ {
		_, _ = state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms)) // same ID, replayed
	}
	group, _ := state.Group(id)
	if len(group.Observations) != 1 {
		t.Fatalf("expected exactly 1 observation after 5 replays of the same ID, got %d", len(group.Observations))
	}
	if group.Status != protocol_FindingUnconfirmedLiteral {
		t.Fatalf("expected unconfirmed (1 agent only, even replayed), got %q", group.Status)
	}
}

func TestNegative_ConflictingNormalizedFingerprintsStayDistinct(t *testing.T) {
	a := ComputeClaimFingerprint(ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42})
	b := ComputeClaimFingerprint(ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "correctness", Line: 42})
	if a == b {
		t.Fatalf("different categories at the same location must not collide into the same fingerprint")
	}
	c := ComputeClaimFingerprint(ClaimInput{Subject: SubjectRef{Path: "other.go"}, Category: "security", Line: 42})
	if a == c {
		t.Fatalf("different files must not collide into the same fingerprint")
	}
}

const (
	protocol_FindingUnconfirmedLiteral = "unconfirmed"
	protocol_FindingRejectedLiteral    = "rejected"
)
