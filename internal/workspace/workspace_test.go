package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

func TestCanonicalizeWorkspace(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	ws, err := CanonicalizeWorkspace(ctx, tmp, "executor", []string{"reviewer"}, nil)
	if err != nil {
		t.Fatalf("CanonicalizeWorkspace failed: %v", err)
	}

	if ws.Root == "" {
		t.Errorf("expected root to be set")
	}
	if ws.WriterParticipant != "executor" {
		t.Errorf("expected writer to be executor, got %q", ws.WriterParticipant)
	}
	if len(ws.ReadOnlyParticipants) != 1 || ws.ReadOnlyParticipants[0] != "reviewer" {
		t.Errorf("expected read only participants to be [reviewer]")
	}
}

func TestWorkspaceCheckPath_TraversalAndSymlink(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	sub := filepath.Join(tmp, "src")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(tmp, "secret.key")
	if err := os.WriteFile(secret, []byte("key"), 0600); err != nil {
		t.Fatal(err)
	}

	ws, err := CanonicalizeWorkspace(ctx, sub, "executor", nil, &protocol.PathPolicy{
		DeniedPaths: []string{"*.key", "secrets/*"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Traversing up outside sub
	if err := ws.CheckPath("../secret.key"); err == nil {
		t.Errorf("expected error for path traversal ../secret.key, got nil")
	}

	// Denied path
	fileInSub := filepath.Join(sub, "test.key")
	if err := os.WriteFile(fileInSub, []byte("key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ws.CheckPath("test.key"); err == nil {
		t.Errorf("expected policy denied error for denied path test.key, got nil")
	}
}
