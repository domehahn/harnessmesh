package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

type mockPeerCodexHarness struct {
	invocations   int
	lastSessionID string
	responses     []agent.InvokeResult
	prompts       []string
}

func (m *mockPeerCodexHarness) ID() string          { return "openai-reviewer" }
func (m *mockPeerCodexHarness) AdapterType() string { return "codex" }
func (m *mockPeerCodexHarness) Name() string        { return "openai-reviewer" }
func (m *mockPeerCodexHarness) Capabilities() config.AgentCapabilities {
	return config.AgentCapabilities{Review: true, AnswerQuestions: true}
}
func (m *mockPeerCodexHarness) Health(ctx context.Context) error { return nil }
func (m *mockPeerCodexHarness) Start(ctx context.Context, repo string) (string, error) {
	return "", nil
}
func (m *mockPeerCodexHarness) Resume(ctx context.Context, sessionID, repo string) error { return nil }
func (m *mockPeerCodexHarness) StartSession(ctx context.Context, repo string) (string, error) {
	return "codex-thread-persistent-100", nil
}
func (m *mockPeerCodexHarness) ResumeSession(ctx context.Context, sessionID, repo string) error {
	m.lastSessionID = sessionID
	return nil
}
func (m *mockPeerCodexHarness) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}
func (m *mockPeerCodexHarness) Invoke(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
	m.lastSessionID = req.SessionID
	m.prompts = append(m.prompts, req.Prompt)
	idx := m.invocations
	m.invocations++
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return agent.InvokeResult{
		AgentName: "openai-reviewer",
		SessionID: "codex-thread-persistent-100",
		Text:      "LGTM!",
	}, nil
}
func (m *mockPeerCodexHarness) Run(ctx context.Context, req agent.Request) (protocol.AgentResult, error) {
	inv, err := m.Invoke(ctx, agent.InvokeRequest{
		Name:      req.Name,
		Repo:      req.Repo,
		Prompt:    req.Prompt,
		SessionID: req.SessionID,
	})
	return protocol.AgentResult{
		AgentName: inv.AgentName,
		Text:      inv.Text,
	}, err
}

func TestAntigravityCodex_EndToEndConversation(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "conversation_test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		Version: 2,
		Collaboration: config.CollaborationConfig{
			MaxPeerRounds:        5,
			MaxConversationTurns: 4,
			MaxPeerDepth:         2,
			MaxPeerCalls:         10,
		},
		Agents: map[string]config.AgentConfig{
			"antigravity-main": {
				Kind:     "antigravity",
				Role:     "executor",
				Writable: true,
			},
			"openai-reviewer": {
				Kind:     "codex",
				Role:     "reviewer",
				Roles:    []string{"peer", "reviewer", "architecture"},
				Writable: false,
			},
		},
		CapabilityRouting: map[string][]string{
			"architecture": {"openai-reviewer"},
			"review":       {"openai-reviewer"},
		},
	}

	mockCodex := &mockPeerCodexHarness{
		responses: []agent.InvokeResult{
			{
				AgentName: "openai-reviewer",
				SessionID: "codex-thread-persistent-100",
				Text:      "Consider configuring pool min connections to 5 and max idle time to 10m to avoid connection starvation.",
			},
			{
				AgentName: "openai-reviewer",
				SessionID: "codex-thread-persistent-100",
				Text:      "LGTM! The updated connection pool configuration and retry backoff look robust.",
			},
		},
	}

	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Projector: contextpack.New(tmpDir, cfg.Context),
		Harnesses: map[string]agent.Harness{
			"openai-reviewer": mockCodex,
		},
	})

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "", "Antigravity pool implementation")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Turn 1: Antigravity requests architecture check
	req1 := protocol.ConverseRequest{
		Capability:           "architecture",
		Message:              "What is the recommended pool configuration for SQLite in WAL mode under concurrent read/write?",
		ExpectedResponseType: "architecture_check",
	}

	resp1, err := eng.Converse(ctx, sess.ID, "antigravity-main", req1, 1, "")
	if err != nil {
		t.Fatalf("Turn 1 Converse failed: %v", err)
	}

	if resp1.Peer != "openai-reviewer" {
		t.Errorf("expected routed peer openai-reviewer, got %s", resp1.Peer)
	}
	if !strings.Contains(resp1.Response, "pool min connections") {
		t.Errorf("unexpected response text: %s", resp1.Response)
	}
	if resp1.Type != "answer" {
		t.Errorf("expected type answer, got %s", resp1.Type)
	}

	// Verify participant session ID was mapped
	sessAfterTurn1, err := st.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sessAfterTurn1.Participants["openai-reviewer"].HarnessSessionID != "codex-thread-persistent-100" {
		t.Fatalf("expected harness session id codex-thread-persistent-100, got %s",
			sessAfterTurn1.Participants["openai-reviewer"].HarnessSessionID)
	}

	// Turn 2: Antigravity provides follow-up based on peer response
	req2 := protocol.ConverseRequest{
		Peer:                 "openai-reviewer",
		Message:              "I updated the pool config with min 5 connections and 10m idle timeout. Please check the final design.",
		CausationID:          resp1.MessageID,
		ExpectedResponseType: "review",
	}

	resp2, err := eng.Converse(ctx, sess.ID, "antigravity-main", req2, 1, "")
	if err != nil {
		t.Fatalf("Turn 2 Converse failed: %v", err)
	}

	if resp2.Type != "approval" {
		t.Errorf("expected approval response type, got %s", resp2.Type)
	}
	if !strings.Contains(resp2.Response, "LGTM") {
		t.Errorf("unexpected turn 2 response: %s", resp2.Response)
	}

	// Verify the same Codex thread was resumed in Turn 2!
	if mockCodex.lastSessionID != "codex-thread-persistent-100" {
		t.Errorf("expected persistent thread resumption codex-thread-persistent-100, got %s", mockCodex.lastSessionID)
	}

	// Verify message causality chain in SQLite store
	msgs, err := st.GetMessages(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages in conversation transcript, got %d", len(msgs))
	}

	// msg[0]: Antigravity -> Codex (Turn 1 req)
	// msg[1]: Codex -> Antigravity (Turn 1 resp, causation = msg[0].ID)
	// msg[2]: Antigravity -> Codex (Turn 2 req, causation = msg[1].ID)
	// msg[3]: Codex -> Antigravity (Turn 2 resp, causation = msg[2].ID)
	if msgs[1].CausationID != msgs[0].ID {
		t.Errorf("expected msg[1] causation %s, got %s", msgs[0].ID, msgs[1].CausationID)
	}
	if msgs[2].CausationID != msgs[1].ID {
		t.Errorf("expected msg[2] causation %s, got %s", msgs[1].ID, msgs[2].CausationID)
	}
	if msgs[3].CausationID != msgs[2].ID {
		t.Errorf("expected msg[3] causation %s, got %s", msgs[2].ID, msgs[3].CausationID)
	}

	for _, m := range msgs {
		if m.Type != protocol.MsgConverse {
			t.Errorf("expected message type converse, got %s", m.Type)
		}
	}
}

func TestAntigravityCodex_TurnLimitEnforcement(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "turn_limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		Version: 2,
		Collaboration: config.CollaborationConfig{
			MaxConversationTurns: 2,
		},
		Agents: map[string]config.AgentConfig{
			"antigravity": {Kind: "antigravity", Role: "executor", Writable: true},
			"codex":       {Kind: "codex", Role: "reviewer", Writable: false},
		},
	}

	mockCodex := &mockPeerCodexHarness{
		responses: []agent.InvokeResult{
			{AgentName: "codex", Text: "Turn 1 advice"},
			{AgentName: "codex", Text: "Turn 2 advice"},
		},
	}

	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Projector: contextpack.New(tmpDir, cfg.Context),
		Harnesses: map[string]agent.Harness{"codex": mockCodex},
	})

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "", "Turn limit test")
	if err != nil {
		t.Fatal(err)
	}

	// Turn 1
	_, err = eng.Converse(ctx, sess.ID, "antigravity", protocol.ConverseRequest{
		Peer:    "codex",
		Message: "First turn",
	}, 1, "")
	if err != nil {
		t.Fatalf("Turn 1 failed: %v", err)
	}

	// Turn 2
	_, err = eng.Converse(ctx, sess.ID, "antigravity", protocol.ConverseRequest{
		Peer:    "codex",
		Message: "Second turn",
	}, 1, "")
	if err != nil {
		t.Fatalf("Turn 2 failed: %v", err)
	}

	// Turn 3 should be rejected because MaxConversationTurns = 2
	_, err = eng.Converse(ctx, sess.ID, "antigravity", protocol.ConverseRequest{
		Peer:    "codex",
		Message: "Third turn (exceeds limit)",
	}, 1, "")
	if err == nil {
		t.Fatal("expected error on exceeding max conversation turns, got nil")
	}
	var budgetErr *protocol.BudgetExceededError
	if !strings.Contains(err.Error(), "conversation_turns") {
		t.Errorf("expected conversation_turns in error, got: %v", err)
	}
	_ = budgetErr
}

func TestAntigravityCodex_ConfigPreservationOnIntegrate(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, ".agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}

	existingMCP := map[string]any{
		"mcpServers": map[string]any{
			"existing-server": map[string]any{
				"command": "existing-cmd",
				"args":    []string{"run"},
			},
		},
	}
	existingJSON, _ := json.MarshalIndent(existingMCP, "", "  ")
	mcpPath := filepath.Join(agentDir, "mcp_config.json")
	if err := os.WriteFile(mcpPath, existingJSON, 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate mergeMCPConfig
	var root map[string]any
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	servers := root["mcpServers"].(map[string]any)
	servers["harnessmesh"] = map[string]any{
		"command": "harnessmesh",
		"args":    []string{"mcp", "serve"},
	}
	updated, _ := json.MarshalIndent(root, "", "  ")
	_ = os.WriteFile(mcpPath, updated, 0644)

	// Verify both servers exist
	var readBack map[string]any
	readData, _ := os.ReadFile(mcpPath)
	_ = json.Unmarshal(readData, &readBack)
	srvs := readBack["mcpServers"].(map[string]any)
	if srvs["existing-server"] == nil {
		t.Error("existing-server was overwritten or lost!")
	}
	if srvs["harnessmesh"] == nil {
		t.Error("harnessmesh was not added!")
	}
}
