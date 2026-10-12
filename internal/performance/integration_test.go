package performance

import (
	"context"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// TestIntegration_FullDecisionIntelligenceFlow implements the requested
// end-to-end scenario: Scheduler selects Agent A, Switchyard chooses
// Model X (captured, never LLM-claimed), execution succeeds, a Finding
// is produced with Evidence, an independent Agent B reviews and the
// claim is verified, and the resulting PerformanceObservation finalizes
// with exactly the attributes requested. A subsequent query for
// Agent A / security_review returns exactly one finalized record.
func TestIntegration_FullDecisionIntelligenceFlow(t *testing.T) {
	ctx := context.Background()
	tenant := "tenant-a"

	// --- Scheduler selects Agent A (simulated: in a real system this
	// comes from supervisor.Scheduler's capability-based routing +
	// economy.RoutingDecision; here we just record the outcome of that
	// already-tested decision). ---
	taskID := supervisor.TaskID("T1")
	executionID := "T1#1"
	agentA := supervisor.AgentID("security-agent-a")
	selectionRef := "routing-decision-42"

	startedAt := time.Now().Add(-41 * time.Second)
	finishedAt := time.Now()

	// --- Switchyard chooses Model X - captured from real routing/
	// transport context, never from agent text. ---
	modelUsage := []ModelUsageRef{{RouteID: "strong-reasoning", Provider: "switchyard", Model: "model-x", Count: 1}}

	// --- Execution succeeds. ---
	obs, err := NewObservation(NewObservationInput{
		Tenant: tenant, TaskID: taskID, ExecutionID: executionID,
		TaskClass: TaskClassSecurityReview, Risk: "high",
		AgentID: agentA, SelectionRecordRef: selectionRef,
		ExecutionOutcome: ExecutionCompleted, ModelUsages: modelUsage,
		Metrics:   PerformanceMetrics{Duration: 41 * time.Second, ExecutionAttempts: 1},
		StartedAt: startedAt, FinishedAt: finishedAt,
	})
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}

	store := NewMemoryPerformanceStore()
	if err := store.Save(ctx, obs); err != nil {
		t.Fatalf("Save (pending): %v", err)
	}

	// --- Finding F1 + Evidence E1 (review layer, already proven in ADR
	// 0003/0004 - reused here, not re-implemented). ---
	reviewState := review.NewReviewState(nil)
	claim := review.ClaimInput{Subject: review.SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	groupID, err := reviewState.Ingest(claim, "v1", tenant, review.FindingObservation{
		ID: "O1", FindingID: "F1", SourceAgentID: agentA, ArtifactVersion: "v1", Stance: review.StanceConfirms,
	})
	if err != nil {
		t.Fatalf("Ingest F1: %v", err)
	}

	// --- Independent Agent B reviews. ---
	agentB := supervisor.AgentID("claude-agent-b")
	if _, err := reviewState.Ingest(claim, "v1", tenant, review.FindingObservation{
		ID: "O2", FindingID: "F2", SourceAgentID: agentB, ArtifactVersion: "v1", Stance: review.StanceConfirms,
	}); err != nil {
		t.Fatalf("Ingest F2: %v", err)
	}

	applier := review.NewReviewApplier(review.NewReviewValidator(review.DefaultReviewLimits()), review.NewReviewAuthorizer(), reviewState)
	verifiedGroup, err := applier.Apply(ctx, groupID, []review.ArbiterAction{
		review.RecommendVerificationAction{GroupID: groupID, RationaleSummary: "2 independent confirmations"},
	})
	if err != nil {
		t.Fatalf("Apply (verify): %v", err)
	}
	if verifiedGroup.Status != "verified" {
		t.Fatalf("expected verified, got %q", verifiedGroup.Status)
	}

	// --- Performance observation finalized from the FRESH, authoritative
	// review group. ---
	finalized := FinalizeWithReview(obs, verifiedGroup)
	if err := store.Save(ctx, finalized); err != nil {
		t.Fatalf("Save (finalized): %v", err)
	}

	if finalized.Status != ObservationFinalized {
		t.Fatalf("expected finalized, got %q", finalized.Status)
	}
	if finalized.QualityOutcome != QualityVerified {
		t.Fatalf("expected quality verified, got %q", finalized.QualityOutcome)
	}
	if finalized.ExecutionOutcome != ExecutionCompleted {
		t.Fatalf("expected execution completed, got %q", finalized.ExecutionOutcome)
	}
	found := false
	for _, r := range finalized.ReviewerAgentIDs {
		if r == agentB {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected agent B credited as an independent reviewer, got %v", finalized.ReviewerAgentIDs)
	}

	// --- query Agent A / security_review returns exactly one finalized
	// record. ---
	dataset := NewStoreDataset(store)
	results, err := dataset.Observations(ctx, ObservationFilter{Tenant: tenant, AgentID: agentA, TaskClass: TaskClassSecurityReview})
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 observation for agent A / security_review, got %d", len(results))
	}
	if results[0].Status != ObservationFinalized {
		t.Fatalf("expected the returned record to be finalized, got %q", results[0].Status)
	}

	summary, err := Aggregate(ctx, dataset, ObservationFilter{Tenant: tenant}, agentA, TaskClassSecurityReview)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if summary.VerifiedCount != 1 || summary.FinalizedCount != 1 || summary.CompletedCount != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}
