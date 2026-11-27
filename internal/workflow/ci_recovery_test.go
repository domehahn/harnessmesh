package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

func TestCIRecoveryReference(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	worktree := filepath.Join(root, "repair")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write(repo, "go.mod", "module example.com/ci-recovery\n\ngo 1.22\n")
	write(repo, "calc.go", "package calc\nfunc add(a, b int) int { return a - b }\n")
	write(repo, "calc_test.go", `package calc
import "testing"
func TestAdd(t *testing.T) {
	if got := add(2, 3); got != 5 {
		t.Errorf("add(2, 3) = %d, want 5", got)
	}
}
`)
	git(repo, "init", "-q")
	git(repo, "add", ".")
	git(repo, "-c", "user.name=HarnessMesh Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "Introduce failing CI fixture")
	git(repo, "worktree", "add", "--detach", worktree)

	db := filepath.Join(root, "state.db")
	open := func() (store.Store, *collaboration.Engine) {
		t.Helper()
		st, err := store.OpenSQLite(db)
		if err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{ChangeControl: config.ChangeControlConfig{Enabled: true}}
		return st, collaboration.NewEngine(collaboration.EngineConfig{Config: cfg, Store: st, Repo: worktree})
	}
	st, eng := open()
	t.Cleanup(func() { _ = st.Close() })
	const check = "go test ./..."
	const author = "executor"
	const reviewer = "independent-operator"
	change, err := eng.CreateChange(ctx, &protocol.CreateChangeRequest{
		Title: "Repair failing CI check", Intent: check, AuthorParticipant: author,
	})
	if err != nil {
		t.Fatal(err)
	}
	obligations, err := eng.GetProofObligations(ctx, change.ID)
	if err != nil {
		t.Fatal(err)
	}
	var testID, reviewID string
	for _, obligation := range obligations {
		switch obligation.Type {
		case "unit_tests":
			testID = obligation.ID
			if obligation.Command != check {
				t.Fatalf("unexpected check: %q", obligation.Command)
			}
		case "independent_review":
			reviewID = obligation.ID
		}
	}
	if testID == "" || reviewID == "" {
		t.Fatalf("expected unit test and review obligations: %+v", obligations)
	}
	failed, err := eng.ExecuteProof(ctx, change.ID, testID)
	if err != nil || failed.Result != "failed" || failed.ExitCode == nil || *failed.ExitCode != 1 {
		t.Fatalf("expected failing Go check, got %+v, %v", failed, err)
	}
	t.Logf("Check failed | %s | tree %s | evidence %s", failed.Command, failed.TreeHash, failed.ID)
	assertCIGate(t, eng, change.ID, protocol.GateStatusBlocked)

	write(worktree, "calc.go", "package calc\nfunc add(a, b int) int { return a + b }\n")
	prepared, err := eng.PrepareChange(ctx, change.ID, author)
	if err != nil {
		t.Fatal(err)
	}
	passed, err := eng.ExecuteProof(ctx, change.ID, testID)
	if err != nil || passed.Result != "passed" || passed.TreeHash != prepared.CurrentTreeHash || passed.TreeHash == failed.TreeHash {
		t.Fatalf("expected passing check on repaired tree, got %+v, %v", passed, err)
	}
	t.Logf("Patch verified | %s | tree %s | evidence %s", passed.Command, passed.TreeHash, passed.ID)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, eng = open()
	evidence, err := eng.GetChangeEvidence(ctx, change.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted := make(map[string]*protocol.ChangeEvidence, len(evidence))
	for _, item := range evidence {
		persisted[item.ID] = item
	}
	original, rerun := persisted[failed.ID], persisted[passed.ID]
	if original == nil || original.Valid || rerun == nil || !rerun.Valid || rerun.TreeHash != prepared.CurrentTreeHash {
		t.Fatalf("expected stale original evidence and valid repaired evidence: %+v", evidence)
	}
	assertCIGate(t, eng, change.ID, protocol.GateStatusBlocked)
	t.Logf("Review pending after restart | tree %s | obligation %s", rerun.TreeHash, reviewID)
	if _, err := eng.ExecuteProof(ctx, change.ID, reviewID); err == nil {
		t.Fatal("manual review obligation accepted automated verification")
	}
	submit := func(actor string, evidenceType protocol.EvidenceType) error {
		_, err := eng.SubmitChangeEvidence(ctx, &protocol.ChangeEvidence{
			ChangeID: change.ID, ObligationID: reviewID, TreeHash: rerun.TreeHash,
			SourceParticipant: actor, EvidenceType: evidenceType, Result: "passed",
		})
		return err
	}
	if err := submit(author, "unit_tests"); err == nil {
		t.Fatal("review obligation accepted a different evidence type")
	}
	if err := submit(author, "peer_review"); err == nil {
		t.Fatal("author approved their own change")
	}
	if err := submit(reviewer, "independent_review"); err != nil {
		t.Fatal(err)
	}
	assertCIGate(t, eng, change.ID, protocol.GateStatusCommittable)
	t.Logf("Review approved | tree %s | participant %s", rerun.TreeHash, reviewer)
}

func assertCIGate(t *testing.T, eng *collaboration.Engine, changeID string, want protocol.GateStatus) {
	t.Helper()
	gate, err := eng.EvaluateGate(t.Context(), changeID)
	if err != nil || gate.Status != want {
		t.Fatalf("expected gate %s, got %+v, %v", want, gate, err)
	}
}
