package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/knowledge"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
	"github.com/domehahn/harnessmesh/internal/workspace"
)

func setupMeshCommitEnv(t *testing.T, cfg *config.Config) (*collaboration.Engine, string, store.Store, *config.Config) {
	repoDir := t.TempDir()
	dbDir := t.TempDir()

	srcDir := filepath.Join(repoDir, "src")
	_ = os.MkdirAll(srcDir, 0755)
	_ = os.WriteFile(filepath.Join(srcDir, "auth.go"), []byte("package auth\nfunc Login() bool { return true }\n"), 0644)
	_ = os.WriteFile(filepath.Join(srcDir, "calc.go"), []byte("package calc\nfunc Add(a, b int) int { return a + b }\n"), 0644)

	st, err := store.OpenSQLite(filepath.Join(dbDir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	if cfg == nil {
		cfg = &config.Config{
			ChangeControl: config.ChangeControlConfig{
				Enabled: true,
				Rules: []config.ChangeControlRule{
					{
						Paths:   []string{"src/auth.go"},
						Require: []string{"security_audit", "unit_tests"},
					},
					{
						Paths:   []string{"src/calc.go"},
						Require: []string{"unit_tests"},
					},
				},
			},
		}
	}

	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config: cfg,
		Store:  st,
		Repo:   repoDir,
	})

	return eng, repoDir, st, cfg
}

// Scenario A: Minimal valid change
func TestScenarioA_MinimalValidChange(t *testing.T) {
	ctx := context.Background()
	eng, repoDir, _, _ := setupMeshCommitEnv(t, nil)

	// Modify calc.go
	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\nfunc Add(a, b int) int { return a + b + 0 }\n"), 0644)

	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Update calculator",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatalf("CreateChange failed: %v", err)
	}

	obls, _ := eng.GetProofObligations(ctx, chg.ID)
	if len(obls) == 0 {
		t.Fatalf("expected obligations to be generated")
	}

	// Satisfy obligations
	for _, o := range obls {
		_, err := eng.SubmitChangeEvidence(ctx, &protocol.ChangeEvidence{
			ChangeID:          chg.ID,
			ObligationID:      o.ID,
			TreeHash:          chg.CurrentTreeHash,
			SourceParticipant: "test-runner",
			EvidenceType:      protocol.EvidenceType(o.Type),
			Result:            "passed",
		})
		if err != nil {
			t.Fatalf("SubmitChangeEvidence failed: %v", err)
		}
	}

	// Gate must be committable
	gate, err := eng.EvaluateGate(ctx, chg.ID)
	if err != nil || gate.Status != protocol.GateStatusCommittable {
		t.Fatalf("expected GateStatusCommittable, got %v (%v)", gate, err)
	}

	// Commit succeeds
	committed, err := eng.CommitChange(ctx, chg.ID, "codex", "Merged calculator update")
	if err != nil {
		t.Fatalf("CommitChange failed: %v", err)
	}
	if committed.Status != protocol.ChangeStatusCommitted {
		t.Fatalf("expected committed status, got %s", committed.Status)
	}

	// Verify commit recorded in knowledge archive
	knowledgeRecords, err := eng.KnowledgeSearch(ctx, chg.ID, knowledge.SearchOptions{})
	if err == nil {
		if len(knowledgeRecords) == 0 {
			t.Errorf("expected knowledge record for commit %s", chg.ID)
		}
	}
}

// Scenario B: Gate blocks unverified change
func TestScenarioB_GateBlocksUnverifiedChange(t *testing.T) {
	ctx := context.Background()
	eng, repoDir, _, _ := setupMeshCommitEnv(t, nil)

	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\n// unverified\n"), 0644)

	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Unverified change",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Attempt to commit without satisfying obligations
	_, err = eng.CommitChange(ctx, chg.ID, "codex", "Should fail")
	if err == nil {
		t.Fatalf("expected CommitChange to fail for unverified change")
	}
}

// Scenario C: Working tree mutation invalidates proof
func TestScenarioC_WorktreeMutationInvalidatesProof(t *testing.T) {
	ctx := context.Background()
	eng, repoDir, _, _ := setupMeshCommitEnv(t, nil)

	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\n// v1\n"), 0644)

	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Change v1",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	obls, _ := eng.GetProofObligations(ctx, chg.ID)
	for _, o := range obls {
		_, _ = eng.SubmitChangeEvidence(ctx, &protocol.ChangeEvidence{
			ChangeID:          chg.ID,
			ObligationID:      o.ID,
			TreeHash:          chg.CurrentTreeHash,
			SourceParticipant: "tester",
			EvidenceType:      protocol.EvidenceType(o.Type),
			Result:            "passed",
		})
	}

	gate1, _ := eng.EvaluateGate(ctx, chg.ID)
	if gate1.Status != protocol.GateStatusCommittable {
		t.Fatalf("expected committable gate initially, got %s", gate1.Status)
	}

	// Mutate worktree
	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\n// v2 mutated\n"), 0644)

	// Gate evaluation should now be blocked
	gate2, err := eng.EvaluateGate(ctx, chg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gate2.Status != protocol.GateStatusBlocked {
		t.Fatalf("expected gate blocked after worktree modification, got %s", gate2.Status)
	}
}

// Scenario D: Path-scoped proof freshness preserved
func TestScenarioD_PathScopedProofFreshnessPreserved(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		ChangeControl: config.ChangeControlConfig{
			Enabled:       true,
			DefaultProofs: []string{},
			Rules: []config.ChangeControlRule{
				{
					Paths:   []string{"src/auth.go"},
					Require: []string{"security_audit"},
				},
				{
					Paths:   []string{"src/calc.go"},
					Require: []string{"unit_tests"},
				},
			},
		},
	}
	eng, repoDir, st, _ := setupMeshCommitEnv(t, cfg)

	// Touch both
	_ = os.WriteFile(filepath.Join(repoDir, "src", "auth.go"), []byte("package auth\n// modified auth\n"), 0644)
	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\n// modified calc\n"), 0644)

	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Scoped paths test",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	obls, _ := eng.GetProofObligations(ctx, chg.ID)
	var authOblID, calcOblID string
	for _, o := range obls {
		if o.Type == "security_review" || o.Type == "security_audit" {
			authOblID = o.ID
		}
		if o.Type == "unit_tests" {
			calcOblID = o.ID
		}
	}

	// Submit evidence for auth (scoped to src/auth.go) and calc (scoped to src/calc.go)
	_, _ = eng.SubmitChangeEvidence(ctx, &protocol.ChangeEvidence{
		ChangeID:          chg.ID,
		ObligationID:      authOblID,
		TreeHash:          chg.CurrentTreeHash,
		SourceParticipant: "security-bot",
		EvidenceType:      "security_audit",
		Result:            "passed",
	})
	_, _ = eng.SubmitChangeEvidence(ctx, &protocol.ChangeEvidence{
		ChangeID:          chg.ID,
		ObligationID:      calcOblID,
		TreeHash:          chg.CurrentTreeHash,
		SourceParticipant: "unit-bot",
		EvidenceType:      "unit_tests",
		Result:            "passed",
	})

	// Now modify ONLY src/calc.go
	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\n// calc mutated again\n"), 0644)
	newTree, _ := workspace.ComputeTreeHash(ctx, repoDir)

	// Invalidate stale evidence
	err = eng.MeshCommit().InvalidateStaleEvidence(ctx, chg.ID, newTree, []string{"src/calc.go"})
	if err != nil {
		t.Fatal(err)
	}

	// Verify: calc evidence should be invalidated, but auth evidence should REMAIN VALID!
	evs, _ := st.GetChangeEvidenceForChange(ctx, chg.ID)
	for _, ev := range evs {
		if ev.ObligationID == authOblID && !ev.Valid {
			t.Errorf("expected auth evidence to remain valid since only calc.go changed!")
		}
		if ev.ObligationID == calcOblID && ev.Valid {
			t.Errorf("expected calc evidence to be invalidated since calc.go changed!")
		}
	}
}

// Scenario E: Anti-spoofing rejects self-review
func TestScenarioE_AntiSpoofingRejectsSelfReview(t *testing.T) {
	ctx := context.Background()
	eng, repoDir, _, _ := setupMeshCommitEnv(t, nil)

	_ = os.WriteFile(filepath.Join(repoDir, "src", "auth.go"), []byte("package auth\n// auth change\n"), 0644)

	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Auth patch",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	obls, _ := eng.GetProofObligations(ctx, chg.ID)
	var auditOblID string
	for _, o := range obls {
		if o.Type == "security_review" || o.Type == "security_audit" {
			auditOblID = o.ID
			break
		}
	}

	// Author (codex) attempts to self-certify security audit
	_, err = eng.SubmitChangeEvidence(ctx, &protocol.ChangeEvidence{
		ChangeID:          chg.ID,
		ObligationID:      auditOblID,
		TreeHash:          chg.CurrentTreeHash,
		SourceParticipant: "codex", // Self review!
		EvidenceType:      "security_audit",
		Result:            "passed",
	})
	if err == nil {
		t.Fatalf("expected SelfReviewForbiddenError when author self-reviews")
	}
}

// Scenario F: TOCTOU race rejected at commit
func TestScenarioF_TOCTOURaceRejectedAtCommit(t *testing.T) {
	ctx := context.Background()
	eng, repoDir, _, _ := setupMeshCommitEnv(t, nil)

	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\n// calc v1\n"), 0644)

	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "TOCTOU race test",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	obls, _ := eng.GetProofObligations(ctx, chg.ID)
	for _, o := range obls {
		_, _ = eng.SubmitChangeEvidence(ctx, &protocol.ChangeEvidence{
			ChangeID:          chg.ID,
			ObligationID:      o.ID,
			TreeHash:          chg.CurrentTreeHash,
			SourceParticipant: "bot",
			EvidenceType:      protocol.EvidenceType(o.Type),
			Result:            "passed",
		})
	}

	gate, err := eng.EvaluateGate(ctx, chg.ID)
	if err != nil || gate.Status != protocol.GateStatusCommittable {
		t.Fatalf("expected committable gate before TOCTOU, got %v", gate)
	}

	// Secretly mutate file in repoDir without re-evaluating gate
	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\n// backdoor injected!\n"), 0644)

	// Commit MUST fail because tree hash no longer matches verified tree hash!
	_, err = eng.CommitChange(ctx, chg.ID, "codex", "Should be rejected")
	if err == nil {
		t.Fatalf("expected CommitTreeMismatchError on TOCTOU injection, got nil")
	}
}

// Scenario G: Policy downgrade attempt rejected
func TestScenarioG_PolicyDowngradeAttemptRejected(t *testing.T) {
	ctx := context.Background()
	eng, repoDir, _, activeCfg := setupMeshCommitEnv(t, nil)

	_ = os.WriteFile(filepath.Join(repoDir, "src", "auth.go"), []byte("package auth\n// change\n"), 0644)

	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Locked policy test",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Change has locked policy requiring security_audit and unit_tests
	if chg.ProofPolicyJSON == "" {
		t.Fatalf("expected locked ProofPolicyJSON")
	}

	// Changing config in engine should NOT weaken existing change's locked obligations
	activeCfg.ChangeControl.Rules = nil

	obls, _ := eng.GetProofObligations(ctx, chg.ID)
	if len(obls) < 2 {
		t.Fatalf("expected locked obligations to remain in place despite config change, got %d", len(obls))
	}
}

// Scenario H: Multi-participant change
func TestScenarioH_MultiParticipantChange(t *testing.T) {
	ctx := context.Background()
	eng, repoDir, _, _ := setupMeshCommitEnv(t, nil)

	_ = os.WriteFile(filepath.Join(repoDir, "src", "auth.go"), []byte("package auth\n// multi-agent collaboration\n"), 0644)

	// Participant 1: Codex proposes change
	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Multi-agent auth upgrade",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	obls, _ := eng.GetProofObligations(ctx, chg.ID)
	// Peers review and verify: Claude performs reviews, Antigravity performs test verification
	for _, o := range obls {
		reviewer := "claude"
		if o.Type == "unit_tests" {
			reviewer = "antigravity"
		}
		_, err = eng.SubmitChangeEvidence(ctx, &protocol.ChangeEvidence{
			ChangeID:          chg.ID,
			ObligationID:      o.ID,
			TreeHash:          chg.CurrentTreeHash,
			SourceParticipant: reviewer,
			EvidenceType:      protocol.EvidenceType(o.Type),
			Result:            "passed",
		})
		if err != nil {
			t.Fatalf("Peer %s evidence failed for %s: %v", reviewer, o.ID, err)
		}
	}

	// Gate verified
	gate, err := eng.EvaluateGate(ctx, chg.ID)
	if err != nil || gate.Status != protocol.GateStatusCommittable {
		t.Fatalf("expected committable gate after all peer proofs passed, got %v", gate)
	}

	// Participant 1: Codex commits
	committed, err := eng.CommitChange(ctx, chg.ID, "codex", "Collaborative auth upgrade complete")
	if err != nil {
		t.Fatalf("CommitChange failed: %v", err)
	}
	if committed.Status != protocol.ChangeStatusCommitted {
		t.Fatalf("expected committed, got %s", committed.Status)
	}
}

// Scenario I: Failed proof blocks gate
func TestScenarioI_FailedProofBlocksGate(t *testing.T) {
	ctx := context.Background()
	eng, repoDir, _, _ := setupMeshCommitEnv(t, nil)

	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\n// calc fail\n"), 0644)

	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Failing test",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	obls, _ := eng.GetProofObligations(ctx, chg.ID)
	// Submit failing evidence
	_, err = eng.SubmitChangeEvidence(ctx, &protocol.ChangeEvidence{
		ChangeID:          chg.ID,
		ObligationID:      obls[0].ID,
		TreeHash:          chg.CurrentTreeHash,
		SourceParticipant: "tester",
		EvidenceType:      protocol.EvidenceType(obls[0].Type),
		Result:            "failed",
	})
	if err != nil {
		t.Fatal(err)
	}

	gate, err := eng.EvaluateGate(ctx, chg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gate.Status != protocol.GateStatusBlocked {
		t.Fatalf("expected GateStatusBlocked after failed evidence, got %s", gate.Status)
	}
}

// Scenario J: Idempotent proof execution caching
func TestScenarioJ_IdempotentProofExecutionCaching(t *testing.T) {
	ctx := context.Background()
	eng, repoDir, _, _ := setupMeshCommitEnv(t, nil)

	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\n// idempotent\n"), 0644)

	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Idempotent proof test",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	obls, _ := eng.GetProofObligations(ctx, chg.ID)
	oblID := obls[0].ID

	// First execution
	ev1, err := eng.ExecuteProof(ctx, chg.ID, oblID)
	if err != nil {
		t.Fatalf("first ExecuteProof failed: %v", err)
	}

	// Second execution on same tree hash should return cached result
	ev2, err := eng.ExecuteProof(ctx, chg.ID, oblID)
	if err != nil {
		t.Fatalf("second ExecuteProof failed: %v", err)
	}

	if ev1.ID != ev2.ID {
		t.Errorf("expected cached evidence ID %s, got new ID %s", ev1.ID, ev2.ID)
	}
}

// Scenario K: Abort change transaction
func TestScenarioK_AbortChangeTransaction(t *testing.T) {
	ctx := context.Background()
	eng, repoDir, _, _ := setupMeshCommitEnv(t, nil)

	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\n// abort\n"), 0644)

	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title:             "Abandoned change",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	aborted, err := eng.AbortChange(ctx, chg.ID, "", "requirements changed")
	if err != nil {
		t.Fatalf("AbortChange failed: %v", err)
	}
	if aborted.Status != protocol.ChangeStatusAborted {
		t.Errorf("expected status 'aborted', got %s", aborted.Status)
	}

	// Attempting to submit evidence or commit must fail
	_, err = eng.CommitChange(ctx, chg.ID, "codex", "Should fail")
	if err == nil {
		t.Errorf("expected CommitChange on aborted change to fail")
	}
}

// Scenario L: Cross-space isolation
func TestScenarioL_CrossSpaceIsolation(t *testing.T) {
	ctx := context.Background()
	eng, repoDir, _, _ := setupMeshCommitEnv(t, nil)

	// Create Space A with writer codex
	spaceA, err := eng.SpaceService().CreateSpace(ctx, "space_a", "ws_1", "Space A", "Security", "codex", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Codex creates change in Space A
	_ = os.WriteFile(filepath.Join(repoDir, "src", "calc.go"), []byte("package calc\n// space isolation\n"), 0644)
	chg, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		SpaceID:           spaceA.ID,
		Title:             "Space A change",
		AuthorParticipant: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	if chg.SpaceID != spaceA.ID {
		t.Errorf("expected space %s, got %s", spaceA.ID, chg.SpaceID)
	}

	// Non-writer participant claude cannot create change in Space A
	_, err = eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		SpaceID:           spaceA.ID,
		Title:             "Claude unauthorized change",
		AuthorParticipant: "claude",
	})
	if err == nil {
		t.Errorf("expected unauthorized participant to be rejected in Space A")
	}
}
