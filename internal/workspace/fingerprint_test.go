package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/executil"
)

func TestPathHash(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "hello.txt")
	content := []byte("hello world\n")
	if err := os.WriteFile(filePath, content, 0644); err != nil {
		t.Fatal(err)
	}

	expected := sha256.Sum256(content)
	expectedHex := hex.EncodeToString(expected[:])

	gotHex, err := PathHash(filePath)
	if err != nil {
		t.Fatalf("PathHash failed: %v", err)
	}
	if gotHex != expectedHex {
		t.Errorf("expected %s, got %s", expectedHex, gotHex)
	}
}

func TestComputeTreeHash_DeterministicAndSensitivity(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	f1 := filepath.Join(tmp, "a.txt")
	f2 := filepath.Join(tmp, "b.txt")
	_ = os.WriteFile(f1, []byte("content a"), 0644)
	_ = os.WriteFile(f2, []byte("content b"), 0644)

	h1, err := ComputeTreeHash(ctx, tmp)
	if err != nil {
		t.Fatalf("ComputeTreeHash failed: %v", err)
	}

	h2, err := ComputeTreeHash(ctx, tmp)
	if err != nil {
		t.Fatalf("ComputeTreeHash second call failed: %v", err)
	}

	if h1 != h2 {
		t.Fatalf("expected identical hash for identical directory, got %s and %s", h1, h2)
	}

	// Modify a file
	_ = os.WriteFile(f1, []byte("content a modified"), 0644)
	h3, err := ComputeTreeHash(ctx, tmp)
	if err != nil {
		t.Fatalf("ComputeTreeHash failed after modify: %v", err)
	}
	if h3 == h1 {
		t.Fatalf("expected hash to change after modifying a file")
	}

	// Add a file
	f3 := filepath.Join(tmp, "c.txt")
	_ = os.WriteFile(f3, []byte("content c"), 0644)
	h4, err := ComputeTreeHash(ctx, tmp)
	if err != nil {
		t.Fatalf("ComputeTreeHash failed after add: %v", err)
	}
	if h4 == h3 {
		t.Fatalf("expected hash to change after adding a file")
	}

	// Delete a file
	_ = os.Remove(f2)
	h5, err := ComputeTreeHash(ctx, tmp)
	if err != nil {
		t.Fatalf("ComputeTreeHash failed after delete: %v", err)
	}
	if h5 == h4 {
		t.Fatalf("expected hash to change after deleting a file")
	}
}

func TestComputeTreeHash_GitIsolatedIndex(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	// Init git repo
	_, err := executil.Run(ctx, tmp, nil, "", "git", "init")
	if err != nil {
		t.Skip("git not available in test environment")
	}
	_, _ = executil.Run(ctx, tmp, nil, "", "git", "config", "user.name", "Test")
	_, _ = executil.Run(ctx, tmp, nil, "", "git", "config", "user.email", "test@example.com")

	f1 := filepath.Join(tmp, "file1.txt")
	_ = os.WriteFile(f1, []byte("initial"), 0644)
	_, _ = executil.Run(ctx, tmp, nil, "", "git", "add", "file1.txt")
	_, _ = executil.Run(ctx, tmp, nil, "", "git", "commit", "-m", "initial commit")

	indexPath := filepath.Join(tmp, ".git", "index")
	initialStat, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("failed to stat .git/index: %v", err)
	}

	// Now modify working tree
	_ = os.WriteFile(f1, []byte("modified content"), 0644)
	f2 := filepath.Join(tmp, "untracked.txt")
	_ = os.WriteFile(f2, []byte("untracked content"), 0644)

	treeHash, err := ComputeTreeHash(ctx, tmp)
	if err != nil {
		t.Fatalf("ComputeTreeHash failed: %v", err)
	}
	if treeHash == "" {
		t.Fatalf("expected non-empty treeHash")
	}

	// Verify .git/index was NOT mutated by ComputeTreeHash
	afterStat, err := os.Stat(indexPath)
	if err != nil {
		t.Fatalf("failed to stat .git/index after hashing: %v", err)
	}

	if initialStat.ModTime() != afterStat.ModTime() || initialStat.Size() != afterStat.Size() {
		t.Errorf("ComputeTreeHash mutated .git/index! ModTime before: %v, after: %v", initialStat.ModTime(), afterStat.ModTime())
	}
}

func TestComputeDiff(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	_, err := executil.Run(ctx, tmp, nil, "", "git", "init")
	if err != nil {
		t.Skip("git not available in test environment")
	}
	_, _ = executil.Run(ctx, tmp, nil, "", "git", "config", "user.name", "Test")
	_, _ = executil.Run(ctx, tmp, nil, "", "git", "config", "user.email", "test@example.com")

	f1 := filepath.Join(tmp, "tracked.txt")
	_ = os.WriteFile(f1, []byte("initial"), 0644)
	_, _ = executil.Run(ctx, tmp, nil, "", "git", "add", "tracked.txt")
	_, _ = executil.Run(ctx, tmp, nil, "", "git", "commit", "-m", "commit 1")

	// Modify tracked, add untracked
	_ = os.WriteFile(f1, []byte("modified"), 0644)
	f2 := filepath.Join(tmp, "new.txt")
	_ = os.WriteFile(f2, []byte("new"), 0644)

	paths, err := ComputeDiff(ctx, tmp, "", "")
	if err != nil {
		t.Fatalf("ComputeDiff failed: %v", err)
	}

	foundModified := false
	foundAdded := false
	for _, p := range paths {
		if p.Path == "tracked.txt" && p.ChangeType == "modified" {
			foundModified = true
		}
		if p.Path == "new.txt" && p.ChangeType == "added" {
			foundAdded = true
		}
	}

	if !foundModified {
		t.Errorf("expected tracked.txt to be detected as modified")
	}
	if !foundAdded {
		t.Errorf("expected new.txt to be detected as added")
	}
}
