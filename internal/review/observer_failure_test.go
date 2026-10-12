package review

import (
	"context"
	"testing"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

// --- ADR 0006 Phase 7C: Observer Failure Visibility (review side) ---

type panickingReviewObserver struct{}

func (panickingReviewObserver) ObserveReviewTransition(ctx context.Context, group ReviewGroup, previousStatus protocol.FindingStatus) {
	panic("review observer boom")
}

type countingReviewFailureSink struct{ count int }

func (s *countingReviewFailureSink) RecordReviewObserverPanic() { s.count++ }

func TestObserverFailure_ReviewPanicDoesNotAffectReviewState(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 1}
	groupID, err := state.Ingest(claim, "v1", "tenant-a", FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", ArtifactVersion: "v1", Stance: StanceConfirms})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	state.Ingest(claim, "v1", "tenant-a", FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", ArtifactVersion: "v1", Stance: StanceConfirms})

	sink := &countingReviewFailureSink{}
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state)
	applier = applier.WithObserver(panickingReviewObserver{})
	applier.FailureSink = sink

	group, err := applier.Apply(context.Background(), groupID, []ArbiterAction{
		RecommendVerificationAction{GroupID: groupID, RationaleSummary: "2 independent confirms"},
	})
	if err != nil {
		t.Fatalf("Apply must succeed despite an observer panic, got: %v", err)
	}
	if group.Status != protocol.FindingVerified {
		t.Fatalf("expected the review state transition to remain authoritative (verified), got %q", group.Status)
	}
	if sink.count != 1 {
		t.Fatalf("expected the panic to be recorded exactly once, got %d", sink.count)
	}
}

type panickingReviewFailureSink struct{ calls int }

func (s *panickingReviewFailureSink) RecordReviewObserverPanic() {
	s.calls++
	panic("review sink itself panics")
}

func TestObserverFailure_ReviewFailureSinkPanickingDoesNotCrashApply(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 1}
	groupID, err := state.Ingest(claim, "v1", "tenant-a", FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", ArtifactVersion: "v1", Stance: StanceConfirms})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	state.Ingest(claim, "v1", "tenant-a", FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", ArtifactVersion: "v1", Stance: StanceConfirms})

	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state)
	applier = applier.WithObserver(panickingReviewObserver{})
	sink := &panickingReviewFailureSink{}
	applier.FailureSink = sink

	group, err := applier.Apply(context.Background(), groupID, []ArbiterAction{
		RecommendVerificationAction{GroupID: groupID, RationaleSummary: "2 independent confirms"},
	})
	if err != nil {
		t.Fatalf("Apply must still succeed, got: %v", err)
	}
	if group.Status != protocol.FindingVerified {
		t.Fatalf("expected verified, got %q", group.Status)
	}
	if sink.calls != 1 {
		t.Fatalf("expected the sink to have been invoked exactly once, got %d", sink.calls)
	}
}
