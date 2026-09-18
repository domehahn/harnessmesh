package contextpack

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
)

func TestMiddleOut(t *testing.T) {
	input := strings.Repeat("a", 1000)
	got := MiddleOut(input, 100)
	if len(got) > 100 {
		t.Fatalf("len=%d", len(got))
	}
	if !strings.Contains(got, "HarnessMesh middle truncation") {
		t.Fatalf("missing marker")
	}
}

func TestIsPathAllowed_Secrets(t *testing.T) {
	deniedSecrets := []string{
		".env",
		".env.production",
		"config/.env.local",
		"certs/server.key",
		"certs/server.pem",
		"id_rsa",
		"id_ed25519",
		"secrets/database.yml",
		"credentials/token.json",
		"terraform.tfstate",
		"infra/terraform.tfstate.backup",
		".git/config",
		".harnessmesh/harnessmesh.db",
	}

	for _, secret := range deniedSecrets {
		allowed, reason := IsPathAllowed(secret, nil, nil)
		if allowed {
			t.Errorf("expected secret path %q to be denied, but was allowed", secret)
		}
		if reason == "" {
			t.Errorf("expected reason for denying %q", secret)
		}
	}

	allowedSafe := []string{
		"main.go",
		"internal/auth/token.go",
		"pkg/util/helper.go",
		"README.md",
	}

	for _, safe := range allowedSafe {
		allowed, reason := IsPathAllowed(safe, nil, nil)
		if !allowed {
			t.Errorf("expected safe path %q to be allowed, but denied: %s", safe, reason)
		}
	}
}

func TestIsPathAllowed_PathTraversal(t *testing.T) {
	traversals := []string{
		"../secret.txt",
		"../../etc/passwd",
		"internal/../../secrets.json",
		"/etc/shadow",
	}

	for _, trav := range traversals {
		allowed, _ := IsPathAllowed(trav, nil, nil)
		if allowed {
			t.Errorf("expected traversal path %q to be rejected", trav)
		}
	}
}

func TestProjector_SymlinkEscape(t *testing.T) {
	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	outsideDir := filepath.Join(tmpDir, "outside")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}

	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("super_secret_token"), 0644); err != nil {
		t.Fatal(err)
	}

	symlinkFile := filepath.Join(repoDir, "leak_symlink.txt")
	if err := os.Symlink(outsideFile, symlinkFile); err != nil {
		t.Fatal(err)
	}

	err := validatePathInsideRepo(repoDir, symlinkFile)
	if err == nil {
		t.Fatal("expected symlink escape to be detected and rejected")
	}
	if !strings.Contains(err.Error(), "symlink target escapes repository root") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestProjector_DiffAndStatusFiltering(t *testing.T) {
	rawStatus := ` M main.go
 M .env
?? secrets/config.json
 M pkg/token.go`

	filteredStatus, changed := filterGitStatus(rawStatus, nil, nil)
	if strings.Contains(filteredStatus, ".env") || strings.Contains(filteredStatus, "secrets") {
		t.Fatalf("status contains secret files: %s", filteredStatus)
	}
	if !strings.Contains(filteredStatus, "main.go") || !strings.Contains(filteredStatus, "pkg/token.go") {
		t.Fatalf("status missing safe files: %s", filteredStatus)
	}
	if len(changed) != 2 {
		t.Fatalf("expected 2 changed safe files, got %d", len(changed))
	}

	rawDiff := `diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -1 +1 @@
-old
+new
diff --git a/.env b/.env
index 333..444 100644
--- a/.env
+++ b/.env
@@ -1 +1 @@
-API_KEY=old
+API_KEY=new
diff --git a/pkg/token.go b/pkg/token.go
index 555..666 100644
--- a/pkg/token.go
+++ b/pkg/token.go
@@ -1 +1 @@
-old
+new
`

	filteredDiff := filterDiffContent(rawDiff, nil, nil)
	if strings.Contains(filteredDiff, ".env") || strings.Contains(filteredDiff, "API_KEY") {
		t.Fatalf("filtered diff leaked secret: %s", filteredDiff)
	}
	if !strings.Contains(filteredDiff, "main.go") || !strings.Contains(filteredDiff, "pkg/token.go") {
		t.Fatalf("filtered diff missing safe files: %s", filteredDiff)
	}
}

func TestProjector_TruncationMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	p := New(tmpDir, config.ContextConfig{
		MaxDiffChars: 50,
	})

	proj, err := p.Project(context.Background(), ProjectRequest{
		Task: "Implement auth",
	})
	if err != nil {
		t.Fatal(err)
	}

	if proj.Truncated {
		t.Fatal("expected not truncated for small context")
	}
	if proj.IncludedChars == 0 {
		t.Fatal("expected included chars > 0")
	}
}
