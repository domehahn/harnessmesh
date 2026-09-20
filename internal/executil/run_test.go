package executil

import (
	"context"
	"testing"
)

func TestSandboxPolicyRejectsCommandsAndPaths(t *testing.T) {
	policy := SandboxPolicy{AllowedCommands: []string{"git"}, AllowedPaths: []string{"/tmp/project"}}
	if err := policy.Validate("/tmp/project/subdir", "sh"); err == nil {
		t.Fatal("expected command policy rejection")
	}
	if err := policy.Validate("/tmp/other", "git"); err == nil {
		t.Fatal("expected path policy rejection")
	}
	if _, err := RunWithPolicy(context.Background(), "/tmp/other", nil, "", policy, "git", "status"); err == nil {
		t.Fatal("expected RunWithPolicy rejection")
	}
}
