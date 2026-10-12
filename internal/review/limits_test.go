package review

import (
	"context"
	"errors"
	"testing"
)

func TestNegative_IndependentReviewLimitExceeded(t *testing.T) {
	state := NewReviewState(nil)
	limits := DefaultReviewLimits()
	limits.MaxIndependentReviews = 2
	state.SetLimits(limits)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}

	id, err := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	if err != nil {
		t.Fatalf("ingest 1: %v", err)
	}
	if _, err := state.Ingest(claim, "v1", "", newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms)); err != nil {
		t.Fatalf("ingest 2: %v", err)
	}
	_, err = state.Ingest(claim, "v1", "", newObs(t, "O3", "F3", "agent-c", "v1", StanceConfirms))
	var want *IndependentReviewLimitExceeded
	if !errors.As(err, &want) {
		t.Fatalf("expected IndependentReviewLimitExceeded for a 3rd distinct agent beyond the limit of 2, got %v (%T)", err, err)
	}
	group, _ := state.Group(id)
	if len(group.Observations) != 2 {
		t.Fatalf("expected the rejected 3rd observation to not be appended, got %d observations", len(group.Observations))
	}
}

func TestNegative_ReviewCostLimitExceeded(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	limits := DefaultReviewLimits()
	limits.MaxReviewCost = 2.0 // RequestEvidenceAction costs 1 each (limits.go)
	limits.MaxEvidenceRequests = 1000
	limits.MaxArbitrationRounds = 1000
	applier := NewReviewApplier(NewReviewValidator(limits), NewReviewAuthorizer(), state)

	for i := 0; i < 2; i++ {
		if _, err := applier.Apply(context.Background(), id, []ArbiterAction{
			RequestEvidenceAction{GroupID: id, RationaleSummary: "r"},
		}); err != nil {
			t.Fatalf("request %d should succeed under the cost budget: %v", i, err)
		}
	}
	_, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RequestEvidenceAction{GroupID: id, RationaleSummary: "exceeds budget"},
	})
	var want *ReviewCostLimitExceeded
	if !errors.As(err, &want) {
		t.Fatalf("expected ReviewCostLimitExceeded, got %v (%T)", err, err)
	}
}

func TestNegative_ReviewDepthLimitExceeded(t *testing.T) {
	state := NewReviewState(nil)
	limits := DefaultReviewLimits()
	limits.MaxReviewDepth = 2
	state.SetLimits(limits)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}

	g0, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	g1, err := state.IngestSuccessor(g0, claim, "v2", "", newObs(t, "O2", "F2", "agent-b", "v2", StanceRefutes))
	if err != nil {
		t.Fatalf("successor depth 1: %v", err)
	}
	g2, err := state.IngestSuccessor(g1, claim, "v3", "", newObs(t, "O3", "F3", "agent-c", "v3", StanceRefutes))
	if err != nil {
		t.Fatalf("successor depth 2: %v", err)
	}
	_, err = state.IngestSuccessor(g2, claim, "v4", "", newObs(t, "O4", "F4", "agent-d", "v4", StanceRefutes))
	var want *ReviewDepthLimitExceeded
	if !errors.As(err, &want) {
		t.Fatalf("expected ReviewDepthLimitExceeded at depth 3 beyond limit 2, got %v (%T)", err, err)
	}
}

func TestSnapshotForTenant_OnlyReturnsMatchingTenant(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	state.Ingest(claim, "v1", "tenant-a", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	state.Ingest(ClaimInput{Subject: SubjectRef{Path: "other.go"}, Category: "security", Line: 1}, "v1", "tenant-b", newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms))

	onlyA := state.SnapshotForTenant("tenant-a")
	if len(onlyA) != 1 {
		t.Fatalf("expected exactly 1 group for tenant-a, got %d", len(onlyA))
	}
	if onlyA[0].Tenant != "tenant-a" {
		t.Fatalf("expected tenant-a, got %q", onlyA[0].Tenant)
	}

	all := state.Snapshot()
	if len(all) != 2 {
		t.Fatalf("expected Snapshot() (unscoped) to still return both groups, got %d", len(all))
	}
}

func TestNegative_SnapshotCrossTenantAccessScoped(t *testing.T) {
	// A caller scoped to tenant-a must never see tenant-b's groups via
	// the tenant-scoped primitive, even though they coexist in the same
	// ReviewState.
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	state.Ingest(claim, "v1", "tenant-a", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	state.Ingest(claim, "v1", "tenant-b", newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms))

	for _, g := range state.SnapshotForTenant("tenant-a") {
		if g.Tenant != "tenant-a" {
			t.Fatalf("cross-tenant leak: SnapshotForTenant(tenant-a) returned a group with tenant %q", g.Tenant)
		}
	}
}
