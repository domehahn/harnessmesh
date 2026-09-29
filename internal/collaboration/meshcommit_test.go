package collaboration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

func setupTestMeshCommit(t *testing.T) (*MeshCommitCoordinator, *Engine, store.Store, string) {
	repoDir := t.TempDir()
	dbDir := t.TempDir()

	// Create sample repo structure
	srcDir := filepath.Join(repoDir, "src")
	_ = os.MkdirAll(srcDir, 0755)
	f1 := filepath.Join(srcDir, "main.go")
	_ = os.WriteFile(f1, []byte("package main\n\nfunc main() {}\n"), 0644)

	dbPath := filepath.Join(dbDir, "test.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	cfg := &config.Config{
		ChangeControl: config.ChangeControlConfig{
			Enabled: true,
			Rules: []config.ChangeControlRule{
				{
					Paths:   []string{"src/**"},
					Require: []string{"unit_tests", "independent_review"},
				},
			},
		},
	}

	eng := NewEngine(EngineConfig{
		Config: cfg,
		Store:  st,
		Repo:   repoDir,
	})

	return eng.MeshCommit(), eng, st, repoDir
}

func TestMeshCommit_CreateChange_And_PolicyResolution(t *testing.T) {
	ctx := context.Background()
	mc, _, _, _ := setupTestMeshCommit(t)

	req := &protocol.CreateChangeRequest{
		Title:             "Add new feature",
		AuthorParticipant: "codex",
		Intent:            "Implementation of feature X",
	}

	chg, err := mc.CreateChange(ctx, req)
	if err != nil {
		t.Fatalf("CreateChange failed: %v", err)
	}

	if chg.ID == "" {
		t.Errorf("expected generated change ID")
	}
	if chg.Status != protocol.ChangeStatusDraft {
		t.Errorf("expected status 'draft', got %q", chg.Status)
	}
	if chg.CurrentTreeHash == "" {
		t.Errorf("expected non-empty CurrentTreeHash")
	}

	// Verify obligations were resolved & locked
	obls, err := mc.store.GetProofObligations(ctx, chg.ID)
	if err != nil {
		t.Fatalf("GetProofObligations failed: %v", err)
	}

	if len(obls) < 2 {
		t.Fatalf("expected at least 2 obligations for src/**, got %d", len(obls))
	}

	var foundTest, foundReview bool
	for _, obl := range obls {
		if obl.Type == "unit_tests" {
			foundTest = true
		}
		if obl.Type == "independent_review" {
			foundReview = true
		}
	}

	if !foundTest || !foundReview {
		t.Errorf("expected unit_tests and independent_review obligations, got: %+v", obls)
	}
}

func TestMeshCommit_SingleWriterEnforcement(t *testing.T) {
	ctx := context.Background()
	mc, eng, _, _ := setupTestMeshCommit(t)

	// Create space with designated writer
	space, err := eng.SpaceService().CreateSpace(ctx, "space_arch", "ws_1", "Architecture Space", "Design", "codex", nil)
	if err != nil {
		t.Fatalf("CreateSpace failed: %v", err)
	}

	// Another participant attempts to create a change
	reqUnauthorized := &protocol.CreateChangeRequest{
		SpaceID:           space.ID,
		Title:             "Unauthorized change",
		AuthorParticipant: "claude",
	}

	_, err = mc.CreateChange(ctx, reqUnauthorized)
	if err == nil {
		t.Fatalf("expected PolicyDeniedError when non-writer creates change, got nil")
	}

	// Designated writer creates change successfully
	reqAuthorized := &protocol.CreateChangeRequest{
		SpaceID:           space.ID,
		Title:             "Authorized change",
		AuthorParticipant: "codex",
	}

	chg, err := mc.CreateChange(ctx, reqAuthorized)
	if err != nil {
		t.Fatalf("expected authorized writer to succeed, got %v", err)
	}
	if chg.AuthorParticipant != "codex" {
		t.Errorf("expected author codex, got %s", chg.AuthorParticipant)
	}
}

func TestMeshCommit_ExternalParticipantCannotCreateOrCommitChange(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	dbDir := t.TempDir()

	srcDir := filepath.Join(repoDir, "src")
	_ = os.MkdirAll(srcDir, 0755)
	_ = os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	dbPath := filepath.Join(dbDir, "test.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"chatgpt-browser": {
				ExecutionMode: "external",
				Writable:      false,
			},
			"reviewer-only": {
				ExecutionMode: "managed",
				Writable:      false,
			},
			"claude-executor": {
				ExecutionMode: "managed",
				Writable:      true,
			},
		},
	}

	eng := NewEngine(EngineConfig{Config: cfg, Store: st, Repo: repoDir})
	mc := eng.MeshCommit()

	// External participant must never be able to open a change transaction.
	_, err = mc.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "ChatGPT tries to change repo",
		AuthorParticipant: "chatgpt-browser",
	})
	if err == nil {
		t.Fatalf("expected external participant to be denied CreateChange, got nil error")
	}
	var pd *protocol.PolicyDeniedError
	if !asPolicyDenied(err, &pd) {
		t.Fatalf("expected PolicyDeniedError, got %T: %v", err, err)
	}

	// A managed-but-non-writable participant must also be denied.
	_, err = mc.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Reviewer tries to change repo",
		AuthorParticipant: "reviewer-only",
	})
	if err == nil {
		t.Fatalf("expected non-writable participant to be denied CreateChange, got nil error")
	}

	// The writable/managed executor succeeds.
	chg, err := mc.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Executor changes repo",
		AuthorParticipant: "claude-executor",
	})
	if err != nil {
		t.Fatalf("expected writable executor to succeed, got %v", err)
	}

	// Even if somehow committable, CommitChange must reject a caller
	// impersonating a different author, and must reject a non-writable author.
	_, err = mc.CommitChange(ctx, chg.ID, "chatgpt-browser", "msg")
	if err == nil {
		t.Fatalf("expected CommitChange to reject mismatched/non-writer author, got nil")
	}
}

func asPolicyDenied(err error, target **protocol.PolicyDeniedError) bool {
	if pd, ok := err.(*protocol.PolicyDeniedError); ok {
		*target = pd
		return true
	}
	return false
}

func TestMeshCommit_IndependentReviewAntiSpoofing(t *testing.T) {
	ctx := context.Background()
	mc, _, _, _ := setupTestMeshCommit(t)

	chg, err := mc.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Feature Y",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatalf("CreateChange failed: %v", err)
	}

	obls, _ := mc.store.GetProofObligations(ctx, chg.ID)
	var reviewOblID string
	for _, o := range obls {
		if o.Type == "independent_review" {
			reviewOblID = o.ID
			break
		}
	}

	// Author (codex) attempts to submit own review evidence
	authorSelfReview := &protocol.ChangeEvidence{
		ChangeID:          chg.ID,
		ObligationID:      reviewOblID,
		TreeHash:          chg.CurrentTreeHash,
		SourceParticipant: "codex", // Same as author
		EvidenceType:      "independent_review",
		Result:            "passed",
	}

	_, err = mc.SubmitEvidence(ctx, authorSelfReview)
	if err == nil {
		t.Fatalf("expected SelfReviewForbiddenError when author self-reviews, got nil")
	}

	// Independent peer (antigravity) submits review evidence
	peerReview := &protocol.ChangeEvidence{
		ChangeID:          chg.ID,
		ObligationID:      reviewOblID,
		TreeHash:          chg.CurrentTreeHash,
		SourceParticipant: "antigravity", // Independent reviewer
		EvidenceType:      "independent_review",
		Result:            "passed",
	}

	_, err = mc.SubmitEvidence(ctx, peerReview)
	if err != nil {
		t.Fatalf("expected peer review to succeed, got %v", err)
	}
}

func TestMeshCommit_GateEvaluation_And_Commit(t *testing.T) {
	ctx := context.Background()
	mc, _, _, _ := setupTestMeshCommit(t)

	chg, err := mc.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Complete flow",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = mc.PrepareChange(ctx, chg.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Initially gate should be blocked because obligations are pending
	gateRes, err := mc.EvaluateGate(ctx, chg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gateRes.Status != protocol.GateStatusBlocked {
		t.Fatalf("expected GateStatusBlocked, got %s", gateRes.Status)
	}

	// Submit passing evidence for all obligations
	obls, _ := mc.store.GetProofObligations(ctx, chg.ID)
	for _, o := range obls {
		reviewer := "reviewer-agent"
		if o.Type != "independent_review" {
			reviewer = "test-runner"
		}
		_, err := mc.SubmitEvidence(ctx, &protocol.ChangeEvidence{
			ChangeID:          chg.ID,
			ObligationID:      o.ID,
			TreeHash:          chg.CurrentTreeHash,
			SourceParticipant: reviewer,
			EvidenceType:      protocol.EvidenceType(o.Type),
			Result:            "passed",
		})
		if err != nil {
			t.Fatalf("failed to submit evidence for %s: %v", o.ID, err)
		}
	}

	// Re-evaluate gate -> should now be committable
	gateRes, err = mc.EvaluateGate(ctx, chg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gateRes.Status != protocol.GateStatusCommittable {
		t.Fatalf("expected GateStatusCommittable after all proofs passed, got %s", gateRes.Status)
	}

	// Commit change
	committed, err := mc.CommitChange(ctx, chg.ID, "codex", "Merged feature")
	if err != nil {
		t.Fatalf("CommitChange failed: %v", err)
	}

	if committed.Status != protocol.ChangeStatusCommitted {
		t.Errorf("expected status 'committed', got %s", committed.Status)
	}
	if committed.CommitSHA == "" {
		t.Errorf("expected CommitSHA to be set")
	}
	if committed.CommittedAt == nil {
		t.Errorf("expected CommittedAt to be set")
	}
}

func TestMeshCommit_TOCTOU_Protection(t *testing.T) {
	ctx := context.Background()
	mc, _, _, tmpDir := setupTestMeshCommit(t)

	chg, err := mc.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "TOCTOU test",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Satisfy all obligations
	obls, _ := mc.store.GetProofObligations(ctx, chg.ID)
	for _, o := range obls {
		_, _ = mc.SubmitEvidence(ctx, &protocol.ChangeEvidence{
			ChangeID:          chg.ID,
			ObligationID:      o.ID,
			TreeHash:          chg.CurrentTreeHash,
			SourceParticipant: "reviewer",
			EvidenceType:      protocol.EvidenceType(o.Type),
			Result:            "passed",
		})
	}

	gateRes, err := mc.EvaluateGate(ctx, chg.ID)
	if err != nil || gateRes.Status != protocol.GateStatusCommittable {
		t.Fatalf("expected committable gate result, got %v (%v)", gateRes, err)
	}

	// Now secretly mutate the repository files behind HarnessMesh's back before commit!
	dirtyFile := filepath.Join(tmpDir, "src", "sneaky.go")
	_ = os.WriteFile(dirtyFile, []byte("package main\n// sneak"), 0644)

	// Attempt commit -> MUST FAIL with CommitTreeMismatchError
	_, err = mc.CommitChange(ctx, chg.ID, "codex", "Sneaky commit")
	if err == nil {
		t.Fatalf("expected CommitTreeMismatchError on TOCTOU worktree change, got nil")
	}
}

func TestMeshCommit_ConservativeInvalidation(t *testing.T) {
	ctx := context.Background()
	mc, _, _, tmpDir := setupTestMeshCommit(t)

	chg, err := mc.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Invalidation test",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	obls, _ := mc.store.GetProofObligations(ctx, chg.ID)
	for _, o := range obls {
		_, _ = mc.SubmitEvidence(ctx, &protocol.ChangeEvidence{
			ChangeID:          chg.ID,
			ObligationID:      o.ID,
			TreeHash:          chg.CurrentTreeHash,
			SourceParticipant: "reviewer",
			EvidenceType:      protocol.EvidenceType(o.Type),
			Result:            "passed",
		})
	}

	gateRes, err := mc.EvaluateGate(ctx, chg.ID)
	if err != nil || gateRes.Status != protocol.GateStatusCommittable {
		t.Fatalf("expected committable gate result, got %v", gateRes)
	}

	// Mutate worktree
	modFile := filepath.Join(tmpDir, "src", "main.go")
	_ = os.WriteFile(modFile, []byte("package main\n\nfunc main() { println(1) }\n"), 0644)

	// Evaluating gate should detect change, invalidate stale evidence, and block the gate!
	gateResAfter, err := mc.EvaluateGate(ctx, chg.ID)
	if err != nil {
		t.Fatal(err)
	}

	if gateResAfter.Status != protocol.GateStatusBlocked {
		t.Fatalf("expected gate to become blocked after worktree modification, got %s", gateResAfter.Status)
	}

	// Verify change status is no longer committable
	reloaded, _ := mc.store.GetMeshChange(ctx, chg.ID)
	if reloaded.Status == protocol.ChangeStatusCommittable {
		t.Errorf("expected change status to not be committable after invalidation, got %s", reloaded.Status)
	}
}
