package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/mcp"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

// TestRealHarnessSmoke is opt-in and runs only when HARNESSMESH_E2E=1 and claude/codex are available.
func TestRealHarnessSmoke(t *testing.T) {
	if os.Getenv("HARNESSMESH_E2E") != "1" {
		t.Skip("Skipping real harness smoke test; set HARNESSMESH_E2E=1 to run")
	}

	claudePath, err1 := exec.LookPath("claude")
	codexPath, err2 := exec.LookPath("codex")
	if err1 != nil || err2 != nil {
		t.Skipf("Skipping real harness smoke test: claude=%q codex=%q", claudePath, codexPath)
	}

	tmpDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "smoke.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		Version: 2,
		Collaboration: config.CollaborationConfig{
			MaxPeerRounds: 2,
			MaxPeerDepth:  2,
			MaxPeerCalls:  5,
		},
		Agents: map[string]config.AgentConfig{
			"claude": {Kind: "claude", Command: claudePath, Role: "executor", Writable: true},
			"codex":  {Kind: "codex", Command: codexPath, Role: "reviewer", Writable: false},
		},
	}

	claudeHarness, err := agent.NewHarness("claude", cfg.Agents["claude"], config.SwitchyardConfig{})
	if err != nil {
		t.Fatal(err)
	}
	codexHarness, err := agent.NewHarness("codex", cfg.Agents["codex"], config.SwitchyardConfig{})
	if err != nil {
		t.Fatal(err)
	}

	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Projector: contextpack.New(tmpDir, cfg.Context),
		Harnesses: map[string]agent.Harness{
			"claude": claudeHarness,
			"codex":  codexHarness,
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sess, err := eng.CreateSession(ctx, "hm_e2e_smoke", "Verify agent collaboration")
	if err != nil {
		t.Fatal(err)
	}

	// Verify MCP tools discovery
	server := mcp.NewServer(eng, sess.ID, "claude")
	tools := server.ListTools()
	if len(tools) != 8 {
		t.Fatalf("expected 8 tools discovered, got: %d", len(tools))
	}

	// Verify live review request to real Codex harness
	rev, err := eng.RequestReview(ctx, sess.ID, "claude", protocol.ReviewRequestPayload{
		Peer:  "codex",
		Focus: []string{"correctness"},
	}, 1, "")
	if err != nil {
		t.Fatalf("E2E review failed: %v", err)
	}

	if rev.Status == "" {
		t.Fatal("expected non-empty review status from Codex")
	}

	// Verify session survival in store
	persisted, err := st.GetSession(ctx, sess.ID)
	if err != nil || persisted == nil {
		t.Fatalf("session failed to persist: %v", err)
	}
}
