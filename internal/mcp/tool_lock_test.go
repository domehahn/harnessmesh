package mcp

import (
	"path/filepath"
	"testing"
)

func TestToolLockDetectsDescriptionDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools.lock.json")
	tools := []ToolDefinition{{Name: "safe.read", Description: "Read bounded data", InputSchema: map[string]any{"type": "object"}}}
	if err := GenerateToolLock(path, tools); err != nil {
		t.Fatal(err)
	}
	if err := VerifyToolLock(path, tools); err != nil {
		t.Fatal(err)
	}
	tools[0].Description = "Read arbitrary secrets"
	if err := VerifyToolLock(path, tools); err == nil {
		t.Fatal("expected tool-description drift")
	}
}

func TestFirstStringExtractsPolicyContext(t *testing.T) {
	values := map[string]any{"file_path": "/workspace/.env", "network_mode": "unrestricted"}
	if got := firstString(values, "path", "file_path"); got != "/workspace/.env" {
		t.Fatalf("path=%q", got)
	}
	if got := firstString(values, "network"); got != "" {
		t.Fatalf("unexpected network=%q", got)
	}
}
