package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestLazySelectAndHash(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("# review\nUse peer.review.\n")
	sum := sha256.Sum256(content)
	manifest := `{"name":"repository-review","version":"1.0.0","triggers":["review repository"],"tools":["peer.request_review"],"sha256":"` + hex.EncodeToString(sum[:]) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skill.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Discover(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if r.List()[0].Loaded {
		t.Fatal("skill was eagerly loaded")
	}
	e, err := r.Select("please review repository")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Loaded || !e.Verified || !e.AllowsTool("peer.request_review") {
		t.Fatalf("unexpected entry: %#v", e)
	}
}
