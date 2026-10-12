package review

import (
	"context"
	"errors"
	"testing"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

func TestProjection_OnVerification(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	state.Ingest(claim, "v1", "", newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms))

	s := state.groups[id]
	s.SessionID = "session-1"
	s.FindingID = "finding-1"
	state.groups[id] = s

	projector := &FakeFindingProjector{}
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state).WithProjector(projector)

	if _, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RecommendVerificationAction{GroupID: id, RationaleSummary: "verify"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(projector.Projected) != 1 {
		t.Fatalf("expected exactly 1 projection call, got %d", len(projector.Projected))
	}
	got := projector.Projected[0]
	if got.SessionID != "session-1" || got.FindingID != "finding-1" || got.Status != protocol.FindingVerified {
		t.Fatalf("unexpected projection: %+v", got)
	}
}

func TestProjection_NoChangeNoProjection(t *testing.T) {
	// A RequestEvidenceAction that keeps status at needs_evidence twice
	// in a row should not double-project if status doesn't change
	// between calls... but the first call DOES change status
	// (unconfirmed -> needs_evidence), so only verify the SECOND
	// identical call doesn't re-project.
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	s := state.groups[id]
	s.SessionID = "session-1"
	s.FindingID = "finding-1"
	state.groups[id] = s

	projector := &FakeFindingProjector{}
	limits := DefaultReviewLimits()
	limits.MaxEvidenceRequests = 100
	limits.MaxArbitrationRounds = 100
	applier := NewReviewApplier(NewReviewValidator(limits), NewReviewAuthorizer(), state).WithProjector(projector)

	applier.Apply(context.Background(), id, []ArbiterAction{RequestEvidenceAction{GroupID: id, RationaleSummary: "r"}})
	applier.Apply(context.Background(), id, []ArbiterAction{RequestEvidenceAction{GroupID: id, RationaleSummary: "r"}})

	if len(projector.Projected) != 1 {
		t.Fatalf("expected exactly 1 projection call (status only changed once), got %d", len(projector.Projected))
	}
}

func TestProjection_FailureDoesNotRollBackReviewState(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	state.Ingest(claim, "v1", "", newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms))
	s := state.groups[id]
	s.SessionID = "session-1"
	s.FindingID = "finding-1"
	state.groups[id] = s

	failingProjector := &FakeFindingProjector{ProjectFunc: func(ctx context.Context, sessionID, findingID string, status protocol.FindingStatus) error {
		return errors.New("downstream store unreachable")
	}}
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state).WithProjector(failingProjector)

	_, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RecommendVerificationAction{GroupID: id, RationaleSummary: "verify"},
	})
	var want *FindingProjectionFailure
	if !errors.As(err, &want) {
		t.Fatalf("expected FindingProjectionFailure, got %v (%T)", err, err)
	}

	// The review-state transition itself MUST still have committed -
	// ReviewState remains authoritative even though projection failed;
	// this is the documented eventual-consistency model, not a rollback.
	group, _ := state.Group(id)
	if group.Status != protocol.FindingVerified {
		t.Fatalf("expected the review transition to have committed despite the projection failure, got %q", group.Status)
	}
}

func TestNegative_FindingStatusProjectionFails(t *testing.T) {
	// Same as above, named to match the requested negative-test list
	// explicitly: a failing projection must surface as a typed error
	// without corrupting authoritative review state.
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	state.Ingest(claim, "v1", "", newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms))
	s := state.groups[id]
	s.SessionID = "session-1"
	s.FindingID = "finding-1"
	state.groups[id] = s

	projector := &FakeFindingProjector{ProjectFunc: func(ctx context.Context, sessionID, findingID string, status protocol.FindingStatus) error {
		return errors.New("finding not found downstream")
	}}
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state).WithProjector(projector)
	_, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RecommendVerificationAction{GroupID: id, RationaleSummary: "verify"},
	})
	if err == nil {
		t.Fatalf("expected an error when projection fails")
	}
}
