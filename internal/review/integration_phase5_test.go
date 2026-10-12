package review

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// TestIntegration_Phase5_FullEvidenceDrivenFlow implements the Phase 5
// end-to-end scenario: a finding with real, bound evidence, an
// independent review with its own evidence, deterministic "supported"
// correlation, a FakeArbiter recommending verification, Core re-fetching
// and validating both pieces of evidence fresh before verifying,
// remediation to a new artifact version (tracked as a review-depth
// successor), a fresh independent review with NEW evidence on the new
// version, and Core resolving the current (v2) claim while v1's history
// is preserved untouched.
func TestIntegration_Phase5_FullEvidenceDrivenFlow(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryEvidenceStore()
	state := NewReviewState(nil)
	limits := DefaultReviewLimits()
	applier := NewReviewApplier(NewReviewValidator(limits), NewReviewAuthorizerWithEvidence(NewCoreEvidenceValidator(store)), state)

	claim := ClaimInput{Subject: SubjectRef{Repository: "harnessmesh", Path: "auth.go"}, Category: "security", Line: 42}

	// Security Agent, Task T1, Execution X: Finding F1, Evidence E1.
	if err := store.Put(ctx, EvidenceRecord{ID: "E1", Artifact: "auth.go", ArtifactVersion: "v1", TaskID: "T1", ExecutionID: "X", ProducerAgentID: "security-agent", Kind: "static_analysis", Digest: "sha256:e1"}); err != nil {
		t.Fatalf("Put E1: %v", err)
	}
	groupID, err := state.Ingest(claim, "v1", "", FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "security-agent", SourceExecutionID: "T1", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E1"}})
	if err != nil {
		t.Fatalf("ingest F1: %v", err)
	}

	// Independent review T2, Agent B, Evidence E2 (different execution/agent).
	if err := store.Put(ctx, EvidenceRecord{ID: "E2", Artifact: "auth.go", ArtifactVersion: "v1", TaskID: "T2", ExecutionID: "Y", ProducerAgentID: "agent-b", Kind: "manual_review", Digest: "sha256:e2"}); err != nil {
		t.Fatalf("Put E2: %v", err)
	}
	if _, err := state.Ingest(claim, "v1", "", FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", SourceExecutionID: "T2", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E2"}}); err != nil {
		t.Fatalf("ingest F2: %v", err)
	}

	group, _ := state.Group(groupID)
	if group.Status != "supported" {
		t.Fatalf("expected supported, got %q", group.Status)
	}

	arbiter := &FakeArbiter{ReviewFunc: func(ctx context.Context, req ArbitrationRequest) (ArbitrationProposal, error) {
		return ArbitrationProposal{GroupID: req.Group.ID, Assessment: AssessmentSupported, Actions: []ArbiterAction{
			RecommendVerificationAction{GroupID: req.Group.ID, RationaleSummary: "two independent, evidence-backed confirmations"},
		}}, nil
	}}
	proposal, err := arbiter.Review(ctx, ArbitrationRequest{Group: group, Limits: limits})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	verified, err := applier.Apply(ctx, groupID, proposal.Actions)
	if err != nil {
		t.Fatalf("Apply (verify): %v", err)
	}
	if verified.Status != "verified" {
		t.Fatalf("expected verified, got %q", verified.Status)
	}

	// Remediation: new artifact version v2, new evidence E3/E4, tracked
	// as a review-depth successor of the v1 group.
	if err := store.Put(ctx, EvidenceRecord{ID: "E3", Artifact: "auth.go", ArtifactVersion: "v2", TaskID: "T4", ExecutionID: "Z", ProducerAgentID: "agent-c", Kind: "dynamic_analysis", Digest: "sha256:e3"}); err != nil {
		t.Fatalf("Put E3: %v", err)
	}
	successorID, err := state.IngestSuccessor(groupID, claim, "v2", "", FindingObservation{ID: "O3", FindingID: "F3", SourceAgentID: "agent-c", SourceExecutionID: "T4", ArtifactVersion: "v2", Stance: StanceRefutes, EvidenceRefs: []string{"E3"}})
	if err != nil {
		t.Fatalf("IngestSuccessor: %v", err)
	}
	if successorID == groupID {
		t.Fatalf("remediation review must open a new group, not reuse v1's verified group")
	}

	if err := store.Put(ctx, EvidenceRecord{ID: "E4", Artifact: "auth.go", ArtifactVersion: "v2", TaskID: "T4b", ExecutionID: "ZZ", ProducerAgentID: "agent-d", Kind: "dynamic_analysis", Digest: "sha256:e4"}); err != nil {
		t.Fatalf("Put E4: %v", err)
	}
	if _, err := state.Ingest(claim, "v2", "", FindingObservation{ID: "O4", FindingID: "F4", SourceAgentID: "agent-d", SourceExecutionID: "T4b", ArtifactVersion: "v2", Stance: StanceRefutes, EvidenceRefs: []string{"E4"}}); err != nil {
		t.Fatalf("ingest F4: %v", err)
	}

	successorGroup, _ := state.Group(successorID)
	if successorGroup.Depth != 1 {
		t.Fatalf("expected depth 1 (successor of a depth-0 group), got %d", successorGroup.Depth)
	}

	arbiter2 := &FakeArbiter{ReviewFunc: func(ctx context.Context, req ArbitrationRequest) (ArbitrationProposal, error) {
		return ArbitrationProposal{GroupID: req.Group.ID, Assessment: AssessmentUnconfirmed, Actions: []ArbiterAction{
			RejectClaimAction{GroupID: req.Group.ID, RationaleSummary: "v2 evidence independently confirms the issue is absent"},
		}}, nil
	}}
	proposal2, _ := arbiter2.Review(ctx, ArbitrationRequest{Group: successorGroup, Limits: limits})
	resolved, err := applier.Apply(ctx, successorID, proposal2.Actions)
	if err != nil {
		t.Fatalf("Apply (reject v2 claim): %v", err)
	}
	if resolved.Status != "rejected" {
		t.Fatalf("expected the v2 claim rejected, got %q", resolved.Status)
	}

	// v1's history (verified) must remain untouched.
	v1Group, _ := state.Group(groupID)
	if v1Group.Status != "verified" {
		t.Fatalf("v1 group status must remain verified, got %q", v1Group.Status)
	}
}

// TestSecurity_StaleEvidenceAfterRemediationRejected proves v1 evidence
// can never authorize a verdict on the v2 (successor) group, even though
// the successor group was explicitly created as a remediation of v1 -
// "v2 remediates v1" is a sequencing relationship, never an evidence
// equivalence.
func TestSecurity_StaleEvidenceAfterRemediationRejected(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryEvidenceStore()
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}

	store.Put(ctx, EvidenceRecord{ID: "E1", ArtifactVersion: "v1", Digest: "sha256:e1", Kind: "static_analysis"})
	groupID, _ := state.Ingest(claim, "v1", "", FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E1"}})

	store.Put(ctx, EvidenceRecord{ID: "E2", ArtifactVersion: "v2", Digest: "sha256:e2", Kind: "static_analysis"})
	successorID, _ := state.IngestSuccessor(groupID, claim, "v2", "", FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", ArtifactVersion: "v2", Stance: StanceRefutes, EvidenceRefs: []string{"E1"}}) // reuses v1's evidence on purpose
	// A second observation using v1's evidence again (still stale for v2).
	state.Ingest(claim, "v2", "", FindingObservation{ID: "O3", FindingID: "F3", SourceAgentID: "agent-c", ArtifactVersion: "v2", Stance: StanceRefutes, EvidenceRefs: []string{"E1"}})

	authz := NewReviewAuthorizerWithEvidence(NewCoreEvidenceValidator(store))
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), authz, state)
	_, err := applier.Apply(ctx, successorID, []ArbiterAction{
		RejectClaimAction{GroupID: successorID, RationaleSummary: "reject v2 claim using v1 evidence"},
	})
	var want *EvidenceVersionMismatch
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceVersionMismatch: v1 evidence must never authorize a v2 verdict, got %v (%T)", err, err)
	}
}

// TestSecurity_CopiedEvidenceNotCountedAsIndependent combines
// ConsumedFindingIDs (Phase 4) with EvidenceRefs (Phase 5): an agent that
// merely reused the SAME evidence reference another observation already
// used, while also explicitly marking it as consumed from that
// observation, must not count as an independent corroboration - this is
// already covered structurally by Phase 4's IsIndependentObservation
// (ConsumedFindingIDs check), re-asserted here with real EvidenceRefs
// present to prove the two mechanisms compose correctly rather than one
// accidentally overriding the other.
func TestSecurity_CopiedEvidenceNotCountedAsIndependent(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryEvidenceStore()
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	store.Put(ctx, EvidenceRecord{ID: "E1", ArtifactVersion: "v1", Digest: "sha256:e1", Kind: "static_analysis"})

	groupID, _ := state.Ingest(claim, "v1", "", FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E1"}})
	copier := FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E1"}, ConsumedFindingIDs: []string{"F1"}}
	state.Ingest(claim, "v1", "", copier)

	group, _ := state.Group(groupID)
	if group.Status != "unconfirmed" {
		t.Fatalf("expected unconfirmed: agent-b merely reused agent-a's evidence and explicitly consumed its finding, got %q", group.Status)
	}
}

// TestConcurrency_VerifyRejectRaceOnSameClaim proves two goroutines
// racing to verify vs. reject the SAME claim never both succeed - the
// atomic apply under one lock means whichever runs first commits a
// terminal status, and the second sees group.Status already terminal
// (verifyClaim/rejectClaim's early-return) and gets back that SAME
// status as a no-op, never a conflicting one.
func TestConcurrency_VerifyRejectRaceOnSameClaim(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	state.Ingest(claim, "v1", "", newObs(t, "O2", "F2", "agent-b", "v1", StanceConfirms))
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), NewReviewAuthorizer(), state)

	var wg sync.WaitGroup
	results := make([]string, 20)
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			var g ReviewGroup
			var err error
			if i%2 == 0 {
				g, err = applier.Apply(context.Background(), id, []ArbiterAction{RecommendVerificationAction{GroupID: id, RationaleSummary: "r"}})
			} else {
				g, err = applier.Apply(context.Background(), id, []ArbiterAction{RejectClaimAction{GroupID: id, RationaleSummary: "r"}})
			}
			if err == nil {
				results[i] = string(g.Status)
			} else {
				results[i] = "error:" + err.Error()
			}
		}()
	}
	wg.Wait()

	final, _ := state.Group(id)
	if final.Status != "verified" && final.Status != "rejected" {
		t.Fatalf("expected a single terminal status to win, got %q", final.Status)
	}
	// Every successful result must match the final, single terminal
	// status - no goroutine observed a conflicting committed status.
	for _, r := range results {
		if r == "verified" || r == "rejected" {
			if r != string(final.Status) {
				t.Fatalf("observed conflicting committed statuses: %q vs final %q", r, final.Status)
			}
		}
	}
}
