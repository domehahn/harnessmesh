package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

func TestChangePlanApprovalAndHandoff(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("HARNESSMESH_DB_PATH", filepath.Join(root, "change.db"))
	git := func(args ...string) {
		t.Helper()
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	write := func(file string, data []byte) {
		t.Helper()
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("ADR.md", []byte("Preserve additive behavior.\n"))
	git("add", "ADR.md")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "Add decision")

	eng, st, err := getChangeEngine("")
	if err != nil {
		t.Fatal(err)
	}
	change, err := eng.CreateChange(t.Context(), &protocol.CreateChangeRequest{Title: "Repair sample", AuthorParticipant: "writer"})
	_ = st.Close()
	if err != nil {
		t.Fatal(err)
	}
	spec := collaboration.SpecContract{
		Title: "Repair sample", Intent: "Restore the expected calculation behavior",
		NonGoals: []string{"API changes"}, Scope: []string{"calc.go"},
		MaxChangedFiles: 1, MaxChangedLines: 6, NeverCut: []string{"ADR.md"},
		VerificationCommands: []string{"go test ./..."}, AcceptanceCriteria: []string{"The calculation passes."},
		DecisionReferences: []collaboration.DecisionReference{{Path: "ADR.md", Revision: "HEAD"}},
	}
	specFile := filepath.Join(root, "spec.json")
	saveSpec := func() {
		t.Helper()
		data, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		write(specFile, data)
	}
	saveSpec()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := loadPlan(t.Context(), cwd, specFile, change)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Spec.DecisionReferences[0].Revision == "HEAD" {
		t.Fatal("expected resolved ADR commit")
	}
	common := []string{"--spec", specFile, "--change", change.ID}
	receiptFile := filepath.Join(root, "executor-input.json")
	run := func() error {
		return changePlan(append(append([]string{"run"}, common...), "--", "cp", "/dev/stdin", receiptFile))
	}
	approve := func(actor string) error {
		return changePlan(append(append([]string{"approve"}, common...), "--reviewer", actor, "--digest", plan.Digest))
	}
	if err := run(); err == nil {
		t.Fatal("unapproved executor started")
	}
	if err := approve("writer"); err == nil {
		t.Fatal("change author approved own plan")
	}
	if err := approve("reviewer"); err != nil {
		t.Fatal(err)
	}
	spec.AcceptanceCriteria[0] = "The revised calculation passes."
	saveSpec()
	if err := run(); err == nil {
		t.Fatal("modified plan started without fresh approval")
	}
	spec.AcceptanceCriteria[0] = "The calculation passes."
	saveSpec()
	if err := run(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(receiptFile)
	var receipt planDecision
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.Digest != plan.Digest {
		t.Fatalf("executor did not receive approved plan: %s %v", raw, err)
	}
	write("calc.go", []byte("package calc\n"))
	if err := run(); err == nil {
		t.Fatal("executor started against changed worktree")
	}
	if err := changeHandoff([]string{"--change", change.ID, "--plan-digest", plan.Digest}); err != nil {
		t.Fatal(err)
	}
}
