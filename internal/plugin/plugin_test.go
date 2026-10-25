package plugin

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/store"
)

func TestDiscoverVerifiesAndRegistersExecutablePlugin(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo-agent")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "agent")
	contents := []byte(`#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  case "$line" in
    *invoke*) echo "{\"id\":\"$id\",\"ok\":true,\"payload\":{\"text\":\"plugin response\"}}" ;;
    *) echo "{\"id\":\"$id\",\"ok\":true,\"payload\":{}}" ;;
  esac
done
`)
	if err := os.WriteFile(binary, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	manifest := []byte(`{"name":"demo-agent","version":"1.2.3","protocol":"harnessmesh-agent-v1","binary":"agent","enabled":true,"sha256":"` + hex.EncodeToString(digest[:]) + `","capabilities":["review"],"risk":"medium","sandbox":{"network":"host","timeout_seconds":2}}`)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := RegisterDirectory(root, Options{RequireHash: true, HarnessMeshVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != "ready" || !items[0].Verified {
		t.Fatalf("unexpected catalog: %+v", items)
	}
	h, err := NewHarness("demo", items[0].Manifest, dir, items[0].Binary)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := h.Health(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := h.Invoke(ctx, agent.InvokeRequest{Prompt: "hello"})
	if err != nil || result.Text != "plugin response" {
		t.Fatalf("invoke result=%+v err=%v", result, err)
	}
	if closer, ok := h.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func TestSupplyChainDependencySignatureAndCodexImport(t *testing.T) {
	entries := []RegistryEntry{{Name: "base", Version: "1.2.0"}, {Name: "review", Version: "1.0.0", Dependencies: []Dependency{{Name: "base", Constraint: ">=1.0.0"}}}}
	resolved, err := ResolveDependencies(entries, []string{"review"})
	if err != nil || len(resolved) != 2 {
		t.Fatalf("dependency resolution: %#v %v", resolved, err)
	}
	if _, err := ResolveDependencies([]RegistryEntry{{Name: "a", Version: "1.0.0", Conflicts: []Conflict{{Name: "b"}}}, {Name: "b", Version: "1.0.0"}}, []string{"b", "a"}); err == nil {
		t.Fatal("expected conflict")
	}
	pub, private, _ := ed25519.GenerateKey(nil)
	index, err := SignIndex(RegistryIndex{Version: 1, Entries: entries}, "key-1", private)
	if err != nil {
		t.Fatal(err)
	}
	ring := &Keyring{}
	if err := ring.Rotate("key-1", pub); err != nil {
		t.Fatal(err)
	}
	if err := ring.Verify(index); err != nil {
		t.Fatal(err)
	}
	if err := ring.Revoke("key-1", time.Now().UTC()); err == nil {
		t.Fatal("active key revocation must require rotation")
	}
	file := filepath.Join(t.TempDir(), "marketplace.json")
	if err := os.WriteFile(file, []byte(`{"name":"catalog","plugins":[{"name":"one","source":{"source":"local","path":"./one"}},{"name":"one","source":{"source":"local"}}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	items, err := ImportCodexMarketplace(file)
	if err != nil || len(items) != 1 {
		t.Fatalf("codex import: %#v %v", items, err)
	}
}

func TestMarketplacePersistsLifecycleInStore(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "source", "sample-agent")
	t.Setenv("HARNESSMESH_MARKETPLACE_SOURCE_ROOT", filepath.Dir(filepath.Dir(source)))
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"name":"sample-agent","version":"1.0.0","description":"sample","enabled":false}`)
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenSQLite(filepath.Join(t.TempDir(), "marketplace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := NewMarketplace(root, db)
	if _, err := m.Mutate(context.Background(), "tenant-a", "owner", "source_register", map[string]any{"id": "source-a", "path": filepath.Dir(source)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Mutate(context.Background(), "tenant-a", "owner", "install", map[string]any{"plugin": "sample-agent", "source_id": "source-a"}); err != nil {
		t.Fatal(err)
	}
	registry, err := m.Registry(context.Background())
	if err != nil || len(registry.Entries) != 1 || registry.Entries[0].Name != "sample-agent" {
		t.Fatalf("registry publication failed: %+v err=%v", registry, err)
	}
	artifact, err := m.GetArtifact(context.Background(), registry.Entries[0].ArtifactKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = artifact.Close()
	if _, err := m.Mutate(context.Background(), "tenant-a", "reviewer", "approve", map[string]any{"plugin": "sample-agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Mutate(context.Background(), "tenant-a", "owner", "activate", map[string]any{"plugin": "sample-agent"}); err != nil {
		t.Fatal(err)
	}
	second := NewMarketplace(root, db)
	snapshot, err := second.Snapshot(context.Background(), "tenant-a", "sample")
	if err != nil {
		t.Fatal(err)
	}
	plugins := snapshot["plugins"].([]map[string]any)
	if len(plugins) != 1 || plugins[0]["active"] != true {
		t.Fatalf("unexpected marketplace snapshot: %#v", snapshot)
	}
}
