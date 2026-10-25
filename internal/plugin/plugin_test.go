package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/agent"
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
