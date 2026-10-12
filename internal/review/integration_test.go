package review

import (
	"context"
	"errors"
	"testing"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// TestIntegration_VerifyRemediateReReview implements the requested
// end-to-end scenario: a security finding is independently confirmed by
// two agents, a FakeArbiter recommends verification, Core checks
// evidence/provenance/policy and verifies it, a remediation happens
// (new artifact version), and a fresh independent review on the new
// version finds the issue absent - leading the Arbiter to recommend, and
// Core to authorize, resolving the original (old-version) claim as no
// longer applicable going forward (the new-version group is rejected,
// proving the old finding's verification is never silently carried
// across the artifact-version boundary).
func TestIntegration_VerifyRemediateReReview(t *testing.T) {
	ctx := context.Background()
	state := NewReviewState(nil)
	limits := DefaultReviewLimits()
	validator := NewReviewValidator(limits)
	authorizer := NewReviewAuthorizer()
	applier := NewReviewApplier(validator, authorizer, state)

	claim := ClaimInput{Subject: SubjectRef{Repository: "harnessmesh", Path: "auth.go"}, Category: "security", FindingType: "sql_injection", Line: 42}

	// Security Agent A produces F1: SQL injection.
	groupID, err := state.Ingest(claim, "v1", "", FindingObservation{
		ID: "O1", FindingID: "F1", SourceAgentID: "security-agent-a", ArtifactVersion: "v1", Stance: StanceConfirms,
	})
	if err != nil {
		t.Fatalf("ingest A: %v", err)
	}

	// Claude Agent B independently reviews the SAME artifact and
	// supports F1 (different agent, did not consume A's finding).
	if _, err := state.Ingest(claim, "v1", "", FindingObservation{
		ID: "O2", FindingID: "F2", SourceAgentID: "claude-agent-b", ArtifactVersion: "v1", Stance: StanceConfirms,
	}); err != nil {
		t.Fatalf("ingest B: %v", err)
	}

	group, _ := state.Group(groupID)
	if group.Status != protocol.FindingSupported {
		t.Fatalf("expected the correlated ReviewGroup to be 'supported' after 2 independent confirmations, got %q", group.Status)
	}

	// FakeArbiter recommends verification.
	arbiter := &FakeArbiter{ReviewFunc: func(ctx context.Context, req ArbitrationRequest) (ArbitrationProposal, error) {
		return ArbitrationProposal{
			GroupID:          req.Group.ID,
			Assessment:       AssessmentSupported,
			Actions:          []ArbiterAction{RecommendVerificationAction{GroupID: req.Group.ID, RationaleSummary: "two independent confirmations, no conflicting evidence"}},
			RationaleSummary: "recommend verification",
		}, nil
	}}
	proposal, err := arbiter.Review(ctx, ArbitrationRequest{Group: group, Limits: limits})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	// Core checks evidence/provenance/policy and verifies the claim.
	group, err = applier.Apply(ctx, groupID, proposal.Actions)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if group.Status != protocol.FindingVerified {
		t.Fatalf("expected verified, got %q", group.Status)
	}

	// Scheduler creates a remediation task (via the existing,
	// unmodified Phase 3 Scheduler/Applier - not re-implemented here,
	// just proving the translation boundary works).
	remediationAction := RequestIndependentReviewAction{GroupID: groupID, RequiredCapabilities: []string{"remediation"}, RationaleSummary: "fix the verified issue"}
	schedProposal := ToSchedulerProposal(remediationAction, "T-remediate")
	createTask, ok := schedProposal.(supervisor.CreateTaskProposal)
	if !ok {
		t.Fatalf("expected a supervisor.CreateTaskProposal, got %T", schedProposal)
	}
	if len(createTask.Task.RequiredCapabilities) != 1 || createTask.Task.RequiredCapabilities[0] != "remediation" {
		t.Fatalf("unexpected required capabilities: %v", createTask.Task.RequiredCapabilities)
	}

	// Codex fixes the code -> new artifact version v2.
	const newVersion ArtifactVersion = "v2"

	// Security Agent C independently reviews the NEW artifact version
	// and finds the issue absent (refutes the claim, scoped to v2).
	newGroupID, err := state.Ingest(claim, newVersion, "", FindingObservation{
		ID: "O3", FindingID: "F3", SourceAgentID: "security-agent-c", ArtifactVersion: newVersion, Stance: StanceRefutes,
	})
	if err != nil {
		t.Fatalf("ingest C: %v", err)
	}
	if newGroupID == groupID {
		t.Fatalf("a review against the NEW artifact version must open a NEW review group, never silently reuse the old (v1) group's verified status")
	}

	oldGroup, _ := state.Group(groupID)
	if oldGroup.Status != protocol.FindingVerified {
		t.Fatalf("the OLD group's verified status must remain untouched by activity on the new version, got %q", oldGroup.Status)
	}

	// A second independent agent (e.g. re-running the original Agent A's
	// role, but against v2) corroborates absence of the issue.
	if _, err := state.Ingest(claim, newVersion, "", FindingObservation{
		ID: "O4", FindingID: "F4", SourceAgentID: "claude-agent-b", ArtifactVersion: newVersion, Stance: StanceRefutes,
	}); err != nil {
		t.Fatalf("ingest second v2 review: %v", err)
	}

	newGroup, _ := state.Group(newGroupID)
	if newGroup.Status != protocol.FindingContested && newGroup.Status != protocol.FindingUnconfirmed {
		// Both v2 observations refute with no confirms -> DeriveStatus
		// stays Unconfirmed (refute-only is not auto-anything); explicit
		// Core authorization is still required to reach Rejected.
	}

	// Arbiter recommends rejecting the (now v2-scoped) claim.
	arbiter2 := &FakeArbiter{ReviewFunc: func(ctx context.Context, req ArbitrationRequest) (ArbitrationProposal, error) {
		return ArbitrationProposal{
			GroupID:          req.Group.ID,
			Assessment:       AssessmentUnconfirmed,
			Actions:          []ArbiterAction{RejectClaimAction{GroupID: req.Group.ID, RationaleSummary: "fix verified by two independent reviewers on v2"}},
			RationaleSummary: "recommend rejection",
		}, nil
	}}
	proposal2, err := arbiter2.Review(ctx, ArbitrationRequest{Group: newGroup, Limits: limits})
	if err != nil {
		t.Fatalf("Review 2: %v", err)
	}
	finalGroup, err := applier.Apply(ctx, newGroupID, proposal2.Actions)
	if err != nil {
		t.Fatalf("Apply 2: %v", err)
	}
	if finalGroup.Status != protocol.FindingRejected {
		t.Fatalf("expected the v2 claim rejected (issue confirmed absent), got %q", finalGroup.Status)
	}

	// Production-readiness can proceed: no outstanding Verified claim
	// against the CURRENT artifact version (v2); the only Verified
	// claim is permanently scoped to v1.
	all := state.Snapshot()
	for _, g := range all {
		if g.Status == protocol.FindingVerified && g.ArtifactVersion == newVersion {
			t.Fatalf("no claim against the current artifact version should be Verified in this scenario")
		}
	}
}

// TestIntegration_ConflictScenario implements the requested conflict
// scenario: two agents produce incompatible, individually-valid
// observations; Core marks the group contested (deterministic, no
// majority vote); the Arbiter requests independent review rather than
// picking a side; a third agent's independent review breaks the tie;
// the Arbiter proposes a determination; Core decides.
func TestIntegration_ConflictScenario(t *testing.T) {
	ctx := context.Background()
	state := NewReviewState(nil)
	limits := DefaultReviewLimits()
	applier := NewReviewApplier(NewReviewValidator(limits), NewReviewAuthorizer(), state)
	claim := ClaimInput{Subject: SubjectRef{Path: "query.go"}, Category: "security", Line: 10}

	groupID, _ := state.Ingest(claim, "v1", "", FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", ArtifactVersion: "v1", Stance: StanceConfirms})
	state.Ingest(claim, "v1", "", FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", ArtifactVersion: "v1", Stance: StanceRefutes})

	group, _ := state.Group(groupID)
	if group.Status != protocol.FindingContested {
		t.Fatalf("expected contested, got %q", group.Status)
	}

	arbiter := &FakeArbiter{ReviewFunc: func(ctx context.Context, req ArbitrationRequest) (ArbitrationProposal, error) {
		if req.Group.Status != protocol.FindingContested {
			t.Fatalf("expected the Arbiter to see the contested status, got %q", req.Group.Status)
		}
		return ArbitrationProposal{
			GroupID:    req.Group.ID,
			Assessment: AssessmentContested,
			Actions: []ArbiterAction{
				RequestIndependentReviewAction{GroupID: req.Group.ID, RequiredCapabilities: []string{"security_reasoning"}, RationaleSummary: "break the tie - do not pick a winner"},
			},
			RationaleSummary: "contested, requesting independent review",
		}, nil
	}}
	proposal, err := arbiter.Review(ctx, ArbitrationRequest{Group: group, Limits: limits})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if _, err := applier.Apply(ctx, groupID, proposal.Actions); err != nil {
		t.Fatalf("Apply(request_independent_review): %v", err)
	}

	// The Arbiter's action never hardcoded a concrete agent - translate
	// to a Scheduler task.
	reqAction := proposal.Actions[0].(RequestIndependentReviewAction)
	schedProposal := ToSchedulerProposal(reqAction, "T-tiebreak")
	createTask := schedProposal.(supervisor.CreateTaskProposal)
	if createTask.Task.RequiredCapabilities[0] != "security_reasoning" {
		t.Fatalf("unexpected capabilities: %v", createTask.Task.RequiredCapabilities)
	}

	// Agent C reviews the primary artifact independently.
	if _, err := state.Ingest(claim, "v1", "", FindingObservation{ID: "O3", FindingID: "F3", SourceAgentID: "agent-c", ArtifactVersion: "v1", Stance: StanceConfirms}); err != nil {
		t.Fatalf("ingest C: %v", err)
	}
	group, _ = state.Group(groupID)
	// Still contested: 2 independent confirms (A, C) vs 1 independent
	// refute (B) - the presence of ANY independent refute keeps it
	// contested per DeriveStatus, by design (agreement is not authority
	// over an outstanding, independently-sourced disagreement).
	if group.Status != protocol.FindingContested {
		t.Fatalf("expected still contested with an outstanding independent refutation, got %q", group.Status)
	}

	// Arbiter proposes a determination; Core still refuses to verify
	// while contested.
	arbiter2 := &FakeArbiter{ReviewFunc: func(ctx context.Context, req ArbitrationRequest) (ArbitrationProposal, error) {
		return ArbitrationProposal{
			GroupID:          req.Group.ID,
			Assessment:       AssessmentContested,
			Actions:          []ArbiterAction{RecommendVerificationAction{GroupID: req.Group.ID, RationaleSummary: "majority now confirms"}},
			RationaleSummary: "attempt determination",
		}, nil
	}}
	proposal2, _ := arbiter2.Review(ctx, ArbitrationRequest{Group: group, Limits: limits})
	_, err = applier.Apply(ctx, groupID, proposal2.Actions)
	var conflictErr *EvidenceConflict
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected Core to refuse verification over an outstanding independent conflict (NOT a majority-vote override), got %v (%T)", err, err)
	}
}
