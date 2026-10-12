package performance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNegative_NoJointRouterExistsAnywhere is a repository-wide
// structural guard: ADR 0007 explicitly forbids a unified
// JointRouter.SelectAgentAndModel(...) authority. This scans every .go
// source file in the repository for either identifier and fails if
// found - a regression here means Phase 8 (or a later change) crossed
// the one authority boundary this phase was required to preserve.
func TestNegative_NoJointRouterExistsAnywhere(t *testing.T) {
	root := repoRoot(t)
	forbidden := []string{"JointRouter", "SelectAgentAndModel"}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if filepath.Base(path) == "joint_router_guard_test.go" {
			return nil // this file names the forbidden identifiers as literal strings to scan for
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil // unreadable file - not this test's concern
		}
		text := string(content)
		for _, forbiddenName := range forbidden {
			if strings.Contains(text, forbiddenName) {
				t.Errorf("%s contains forbidden identifier %q - ADR 0007 prohibits a unified Agent+Model router", path, forbiddenName)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking repository: %v", err)
	}
}

// repoRoot walks up from the current working directory until it finds
// go.mod - this test package lives several directories deep
// (internal/performance), and the repository root is wherever go.mod
// is, regardless of how `go test` was invoked.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod walking up from %s", dir)
		}
		dir = parent
	}
}
