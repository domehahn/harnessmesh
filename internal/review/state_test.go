package review

import (
	"context"
	"errors"
	"testing"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

func newObs(t *testing.T, id, findingID string, agent supervisor.AgentID, version ArtifactVersion, stance ObservationStance) FindingObservation {
	t.Helper()
	return FindingObservation{
		ID:              TaskID(id),
		FindingID:       findingID,
		SourceAgentID:   agent,
		ArtifactVersion: version,
		Stance:          stance,
	}
}

func TestIngest_NewGroupCreatedAndUnconfirmed(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, err := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "security-agent", "v1", StanceConfirms))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	group, ok := state.Group(id)
	if !ok {
		t.Fatalf("expected group to exist")
	}
	if group.Status != protocol.FindingUnconfirmed {
		t.Fatalf("expected unconfirmed with only 1 observation, got %q", group.Status)
	}
}

func TestIngest_SimilarWordingSameLocationCorrelates(t *testing.T) {
	state := NewReviewState(nil)
	claimA := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	claimB := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 44} // within the same location bucket
	idA, err := state.Ingest(claimA, "v1", "", newObs(t, "O1", "F1", "security-agent", "v1", StanceConfirms))
	if err != nil {
		t.Fatalf("ingest A: %v", err)
	}
	idB, err := state.Ingest(claimB, "v1", "", newObs(t, "O2", "F2", "claude-agent", "v1", StanceConfirms))
	if err != nil {
		t.Fatalf("ingest B: %v", err)
	}
	if idA != idB {
		t.Fatalf("expected 'SQL injection' and 'unsanitized query' style reports at the same location to correlate into one group, got %q vs %q", idA, idB)
	}
	group, _ := state.Group(idA)
	if group.Status != protocol.FindingSupported {
		t.Fatalf("expected supported with 2 independent confirms, got %q", group.Status)
	}
}

func TestIngest_DifferentLocationDoesNotCorrelate(t *testing.T) {
	state := NewReviewState(nil)
	claimA := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	claimB := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 87}
	idA, _ := state.Ingest(claimA, "v1", "", newObs(t, "O1", "F1", "security-agent", "v1", StanceConfirms))
	idB, _ := state.Ingest(claimB, "v1", "", newObs(t, "O2", "F2", "claude-agent", "v1", StanceConfirms))
	if idA == idB {
		t.Fatalf("auth.go:42 and auth.go:87 must not be blindly merged into one group")
	}
}

func TestIngest_DifferentArtifactVersionDoesNotCorrelate(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	idA, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "security-agent", "v1", StanceConfirms))
	idB, _ := state.Ingest(claim, "v2", "", newObs(t, "O2", "F2", "claude-agent", "v2", StanceConfirms))
	if idA == idB {
		t.Fatalf("observations against different artifact versions must not join the same group")
	}
}

func TestIngest_SelfConfirmationDoesNotCountTowardSupported(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "security-agent", "v1", StanceConfirms))
	id2, err := state.Ingest(claim, "v1", "", newObs(t, "O2", "F2", "security-agent", "v1", StanceConfirms))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if id != id2 {
		t.Fatalf("expected same group")
	}
	group, _ := state.Group(id)
	if group.Status != protocol.FindingUnconfirmed {
		t.Fatalf("expected unconfirmed: two observations from the SAME agent must not count as independent corroboration, got %q", group.Status)
	}
}

func TestIngest_ConflictingIndependentObservationsContested(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	id2, _ := state.Ingest(claim, "v1", "", newObs(t, "O2", "F2", "agent-b", "v1", StanceRefutes))
	if id != id2 {
		t.Fatalf("expected same group")
	}
	group, _ := state.Group(id)
	if group.Status != protocol.FindingContested {
		t.Fatalf("expected contested, got %q", group.Status)
	}
}

func TestIngest_DuplicateObservationIDRejected(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	_, err := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1-replay", "agent-a", "v1", StanceConfirms))
	var want *FindingCorrelationFailure
	if !errors.As(err, &want) {
		t.Fatalf("expected FindingCorrelationFailure for a replayed observation ID, got %v (%T)", err, err)
	}
	group, _ := state.Group(id)
	if len(group.Observations) != 1 {
		t.Fatalf("replayed observation must not be appended twice, got %d observations", len(group.Observations))
	}
}

func TestApply_RecommendVerification_InsufficientEvidence(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state)

	_, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RecommendVerificationAction{GroupID: id, RationaleSummary: "looks solid"},
	})
	var want *EvidenceInsufficient
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceInsufficient with only 1 observation, got %v (%T)", err, err)
	}
	group, _ := state.Group(id)
	if group.Status == protocol.FindingVerified {
		t.Fatalf("group must not be verified")
	}
}

func TestApply_RecommendVerification_SucceedsWithTwoIndependentConfirms(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	state.Ingest(claim, "v1", "", newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms))
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state)

	group, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RecommendVerificationAction{GroupID: id, RationaleSummary: "two independent confirmations"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if group.Status != protocol.FindingVerified {
		t.Fatalf("expected verified, got %q", group.Status)
	}
}

func TestApply_RecommendVerification_RejectedWhenContested(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	state.Ingest(claim, "v1", "", newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms))
	state.Ingest(claim, "v1", "", newObs(t, "O3", "F3", "agent-c", "v1", StanceRefutes))
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state)

	_, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RecommendVerificationAction{GroupID: id, RationaleSummary: "try anyway"},
	})
	var want *EvidenceConflict
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceConflict, got %v (%T)", err, err)
	}
}

func TestApply_StaleArtifactEvidenceBlocksVerification(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	state.Ingest(claim, "v1", "", newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms))

	// Simulate an observation whose artifact version silently diverged
	// from the group's (should not normally happen via Ingest/FindGroup,
	// but verifyClaim defends against it anyway - see checkArtifactFreshness).
	state.mu.Lock()
	g := state.groups[id]
	g.Observations[1].ArtifactVersion = "v2-stale"
	state.groups[id] = g
	state.mu.Unlock()

	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state)
	_, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RecommendVerificationAction{GroupID: id, RationaleSummary: "verify"},
	})
	var want *StaleArtifactEvidence
	if !errors.As(err, &want) {
		t.Fatalf("expected StaleArtifactEvidence, got %v (%T)", err, err)
	}
}

func TestApply_NoPartialApplicationOnRejectedBatch(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state)

	_, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RequestEvidenceAction{GroupID: id, RationaleSummary: "need more"},
		SupportClaimAction{GroupID: id, ObservationID: "nonexistent", RationaleSummary: "bad ref"},
	})
	if err == nil {
		t.Fatalf("expected the batch to fail")
	}
	group, _ := state.Group(id)
	if group.EvidenceRequests != 0 {
		t.Fatalf("expected no partial application: EvidenceRequests should still be 0, got %d", group.EvidenceRequests)
	}
}
