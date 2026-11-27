package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadFailedGitHubRun(t *testing.T) {
	const sha = "6b8365f8f44ba104c466f2b1b3a123902615f83f"
	runFile := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(runFile, []byte(`{
		"headSha":"`+sha+`","url":"https://github.com/example/project/actions/runs/42",
		"status":"completed","jobs":[{"name":"unit","conclusion":"failure"},
		{"name":"integration","conclusion":"failure"},{"name":"lint","conclusion":"success"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run, err := readFailedGitHubRun(runFile, "unit")
	if err != nil || run.FailedJob != "unit" || run.HeadSHA != sha {
		t.Fatalf("unexpected import: %+v, %v", run, err)
	}
	for _, job := range []string{"", "lint", "unknown"} {
		if _, err := readFailedGitHubRun(runFile, job); err == nil {
			t.Fatalf("expected failure for job %q", job)
		}
	}
}
