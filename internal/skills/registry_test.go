package skills

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
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

func TestSignatureVerificationAndContextSelection(t *testing.T) {
	root := t.TempDir()
	content := []byte("# secure review\n")
	sum := sha256.Sum256(content)
	pub, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := hex.EncodeToString(sum[:])
	manifest := `{"name":"secure-review","version":"1.0.0","triggers":["security review"],"capabilities":["review"],"tools":["peer.request_review"],"sha256":"` + digest + `","public_key":"` + base64.StdEncoding.EncodeToString(pub) + `","signature":"` + base64.StdEncoding.EncodeToString(ed25519.Sign(private, []byte(digest))) + `"}`
	dir := filepath.Join(root, "secure")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skill.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := DiscoverWithOptions(root, true, true, []ed25519.PublicKey{pub})
	if err != nil {
		t.Fatal(err)
	}
	e, err := r.SelectContext("run a security review", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Verified || !e.SignatureVerified {
		t.Fatalf("signature not verified: %#v", e)
	}
	if _, err := DiscoverWithOptions(root, true, true, []ed25519.PublicKey{}); err != nil {
		t.Fatal(err)
	}
}

func TestContextSelectionUsesObservedEvaluation(t *testing.T) {
	root := t.TempDir()
	for name, trigger := range map[string]string{"fast": "run review", "slow": "run review"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := []byte("# " + name + "\n")
		sum := sha256.Sum256(content)
		manifest := `{"name":"` + name + `","version":"1.0.0","triggers":["` + trigger + `"],"sha256":"` + hex.EncodeToString(sum[:]) + `"}`
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "skill.json"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Discover(root, true)
	if err != nil {
		t.Fatal(err)
	}
	// The lexical tie-breaker initially selects fast. Once fast has three
	// failed runs, the evaluator excludes it and routes to the healthy skill.
	selected, err := r.SelectContext("run review", nil)
	if err != nil || selected.Manifest.Name != "fast" {
		t.Fatalf("initial selection = %q, %v", selected.Manifest.Name, err)
	}
	for i := 0; i < 3; i++ {
		r.ObserveOutcome("fast", false)
	}
	selected, err = r.SelectContext("run review", nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Manifest.Name != "slow" {
		t.Fatalf("expected healthy fallback, got %q", selected.Manifest.Name)
	}
	evaluation := r.Evaluation("fast")
	if evaluation.Runs != 3 || evaluation.Successful != 0 || evaluation.SuccessRate != 0 {
		t.Fatalf("unexpected evaluation: %#v", evaluation)
	}
}

func TestEvaluationSurvivesRegistryRestart(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("# review\n")
	sum := sha256.Sum256(content)
	manifest := `{"name":"review","version":"1.0.0","triggers":["review"],"sha256":"` + hex.EncodeToString(sum[:]) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skill.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Discover(root, true)
	if err != nil {
		t.Fatal(err)
	}
	first.ObserveOutcome("review", true)
	first.ObserveOutcome("review", false)

	second, err := Discover(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Evaluation("review"); got.Runs != 2 || got.Successful != 1 || got.SuccessRate != 0.5 {
		t.Fatalf("persisted evaluation = %#v", got)
	}
}
