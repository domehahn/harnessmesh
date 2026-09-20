package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

func TestMCPServerHandshakeAndTools(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		Version: 2,
		Agents: map[string]config.AgentConfig{
			"claude": {Kind: "claude", Role: "executor", Writable: true},
			"codex":  {Kind: "codex", Role: "reviewer", Writable: false},
		},
	}
	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Projector: contextpack.New(tmpDir, cfg.Context),
		Harnesses: map[string]agent.Harness{},
	})

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_mcp", "MCP task")
	if err != nil {
		t.Fatal(err)
	}

	server := NewServer(eng, sess.ID, "claude")

	// 1. initialize
	initReq := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	resp, err := server.HandleMessage(ctx, initReq)
	if err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected initialize error: %+v", resp.Error)
	}

	// 2. tools/list
	listReq := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	resp, err = server.HandleMessage(ctx, listReq)
	if err != nil {
		t.Fatalf("tools/list failed: %v", err)
	}
	resMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected tools/list result type: %T", resp.Result)
	}
	tools, ok := resMap["tools"].([]ToolDefinition)
	if !ok || len(tools) != 34 {
		t.Fatalf("expected 34 tools, got: %d", len(tools))
	}

	// 3. tools/call peer.list
	listCall := []byte(`{
		"jsonrpc": "2.0",
		"id": 3,
		"method": "tools/call",
		"params": {
			"name": "peer.list",
			"arguments": {}
		}
	}`)
	resp, err = server.HandleMessage(ctx, listCall)
	if err != nil {
		t.Fatalf("tools/call peer.list failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("tool call error: %+v", resp.Error)
	}

	// 4. tools/call peer.submit_finding
	callReq := []byte(`{
		"jsonrpc": "2.0",
		"id": 4,
		"method": "tools/call",
		"params": {
			"name": "peer.submit_finding",
			"arguments": {
				"id": "HM-TEST-001",
				"severity": "medium",
				"category": "correctness",
				"claim": "Missing error check",
				"evidence": "main.go:42",
				"recommendation": "Check error"
			}
		}
	}`)
	resp, err = server.HandleMessage(ctx, callReq)
	if err != nil {
		t.Fatalf("tools/call submit_finding failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("tool call error: %+v", resp.Error)
	}

	// 5. tools/call peer.status
	statusReq := []byte(`{
		"jsonrpc": "2.0",
		"id": 5,
		"method": "tools/call",
		"params": {
			"name": "peer.status",
			"arguments": {}
		}
	}`)
	resp, err = server.HandleMessage(ctx, statusReq)
	if err != nil {
		t.Fatalf("tools/call peer.status failed: %v", err)
	}
	toolRes, ok := resp.Result.(ToolCallResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool call result: %+v", resp.Result)
	}
	var status protocol.StatusPayload
	if err := json.Unmarshal([]byte(toolRes.Content[0].Text), &status); err != nil {
		t.Fatalf("failed to unmarshal status payload: %v", err)
	}
	if status.OpenFindings != 1 {
		t.Fatalf("expected 1 open finding, got %d", status.OpenFindings)
	}
}

func TestMCPServer_StdioContract(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "mcp_stdio.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		Version: 2,
		Agents: map[string]config.AgentConfig{
			"claude": {Kind: "claude", Role: "executor", Writable: true},
			"codex":  {Kind: "codex", Role: "reviewer", Writable: false},
		},
	}
	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Projector: contextpack.New(tmpDir, cfg.Context),
		Harnesses: map[string]agent.Harness{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sess, err := eng.CreateSession(ctx, "hm_mcp_stdio", "MCP stdio task")
	if err != nil {
		t.Fatal(err)
	}

	server := NewServer(eng, sess.ID, "claude")

	clientInReader, clientInWriter := io.Pipe()
	serverOutReader, serverOutWriter := io.Pipe()

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ServeStdio(ctx, clientInReader, serverOutWriter)
	}()

	scanner := bufio.NewScanner(serverOutReader)

	// 1. Send initialize over pipe
	_, _ = clientInWriter.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{}}\n"))
	if !scanner.Scan() {
		t.Fatalf("expected response for initialize: %v", scanner.Err())
	}
	var initResp JSONRPCResponse
	if err := json.Unmarshal(scanner.Bytes(), &initResp); err != nil {
		t.Fatalf("failed to parse init response: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("initialize returned error: %+v", initResp.Error)
	}

	// 2. Send tools/list over pipe
	_, _ = clientInWriter.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n"))
	if !scanner.Scan() {
		t.Fatalf("expected response for tools/list: %v", scanner.Err())
	}
	var listResp JSONRPCResponse
	if err := json.Unmarshal(scanner.Bytes(), &listResp); err != nil {
		t.Fatalf("failed to parse tools/list response: %v", err)
	}
	if listResp.Error != nil {
		t.Fatalf("tools/list returned error: %+v", listResp.Error)
	}
	resMap := listResp.Result.(map[string]any)
	toolsSlice := resMap["tools"].([]any)
	if len(toolsSlice) != 34 {
		t.Fatalf("expected 34 tools, got %d", len(toolsSlice))
	}

	// 3. Send peer.list over pipe
	_, _ = clientInWriter.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"peer.list\",\"arguments\":{}}}\n"))
	if !scanner.Scan() {
		t.Fatalf("expected response for peer.list: %v", scanner.Err())
	}
	var callResp JSONRPCResponse
	if err := json.Unmarshal(scanner.Bytes(), &callResp); err != nil {
		t.Fatalf("failed to parse peer.list response: %v", err)
	}
	if callResp.Error != nil {
		t.Fatalf("peer.list returned error: %+v", callResp.Error)
	}

	// Cleanly close writer and cancel context
	_ = clientInWriter.Close()
	cancel()
	_ = serverOutReader.Close()
}

type fakePeerHarness struct {
	response string
}

func (f *fakePeerHarness) ID() string          { return "codex" }
func (f *fakePeerHarness) AdapterType() string { return "codex" }
func (f *fakePeerHarness) Name() string        { return "codex" }
func (f *fakePeerHarness) Capabilities() config.AgentCapabilities {
	return config.AgentCapabilities{Review: true, AnswerQuestions: true}
}
func (f *fakePeerHarness) Health(ctx context.Context) error                         { return nil }
func (f *fakePeerHarness) Start(ctx context.Context, repo string) (string, error)   { return "", nil }
func (f *fakePeerHarness) Resume(ctx context.Context, sessionID, repo string) error { return nil }
func (f *fakePeerHarness) StartSession(ctx context.Context, repo string) (string, error) {
	return "codex-thread-mcp-123", nil
}
func (f *fakePeerHarness) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return nil
}
func (f *fakePeerHarness) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}
func (f *fakePeerHarness) Invoke(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
	return agent.InvokeResult{
		AgentName: "codex",
		Text:      f.response,
		SessionID: "codex-thread-mcp-123",
	}, nil
}
func (f *fakePeerHarness) Run(ctx context.Context, req agent.Request) (protocol.AgentResult, error) {
	return protocol.AgentResult{
		AgentName: "codex",
		Text:      f.response,
	}, nil
}

func TestMCPServer_Converse(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "mcp_converse.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		Version: 2,
		Agents: map[string]config.AgentConfig{
			"antigravity": {Kind: "antigravity", Role: "executor", Writable: true},
			"codex":       {Kind: "codex", Role: "reviewer", Writable: false},
		},
	}
	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Projector: contextpack.New(tmpDir, cfg.Context),
		Harnesses: map[string]agent.Harness{
			"codex": &fakePeerHarness{response: "LGTM! The architecture looks solid."},
		},
	})

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_mcp_conv", "Converse task")
	if err != nil {
		t.Fatal(err)
	}

	server := NewServer(eng, sess.ID, "antigravity")

	callReq := []byte(`{
		"jsonrpc": "2.0",
		"id": 10,
		"method": "tools/call",
		"params": {
			"name": "peer.converse",
			"arguments": {
				"peer": "codex",
				"message": "Can you review this new module architecture?",
				"expected_outcome": "architecture_check"
			}
		}
	}`)
	resp, err := server.HandleMessage(ctx, callReq)
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	toolRes, ok := resp.Result.(ToolCallResult)
	if !ok || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected result: %+v", resp.Result)
	}

	var convResp protocol.ConverseResponse
	if err := json.Unmarshal([]byte(toolRes.Content[0].Text), &convResp); err != nil {
		t.Fatalf("failed to unmarshal ConverseResponse: %v", err)
	}
	if convResp.Peer != "codex" {
		t.Errorf("expected peer codex, got %s", convResp.Peer)
	}
	if convResp.Type != "approval" {
		t.Errorf("expected type approval, got %s", convResp.Type)
	}
	if convResp.Response != "LGTM! The architecture looks solid." {
		t.Errorf("unexpected response text: %s", convResp.Response)
	}
}

func TestMCPServer_CollaborationTools(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "test_mcp_collab.db"))
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_mcp_test"

	participants := map[string]protocol.SpaceParticipant{
		"antigravity": {
			ID:       "antigravity",
			Adapter:  "antigravity",
			Mode:     protocol.ParticipantModeActive,
			Writable: true,
		},
		"codex": {
			ID:       "codex",
			Adapter:  "codex",
			Mode:     protocol.ParticipantModeOnDemand,
			Writable: false,
		},
	}

	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"antigravity": {Command: "echo", Writable: true},
			"codex":       {Command: "echo", Writable: false},
		},
	}

	harnesses := map[string]agent.Harness{
		"codex": &fakePeerHarness{response: "MCP analysis from codex."},
	}

	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Harnesses: harnesses,
	})

	_, err = eng.SpaceService().CreateSpace(ctx, spaceID, tmpDir, "MCP Space", "MCP test", "antigravity", participants)
	if err != nil {
		t.Fatalf("CreateSpace failed: %v", err)
	}

	server := NewServer(eng, spaceID, "antigravity")

	// 1. collaboration.channels
	chCall := []byte(`{
		"jsonrpc": "2.0",
		"id": 10,
		"method": "tools/call",
		"params": {
			"name": "collaboration.channels",
			"arguments": {
				"space_id": "space_mcp_test"
			}
		}
	}`)
	resp, err := server.HandleMessage(ctx, chCall)
	if err != nil || resp.Error != nil {
		t.Fatalf("collaboration.channels failed: err=%v, respErr=%+v", err, resp.Error)
	}

	// 2. collaboration.publish
	pubCall := []byte(`{
		"jsonrpc": "2.0",
		"id": 11,
		"method": "tools/call",
		"params": {
			"name": "collaboration.publish",
			"arguments": {
				"space_id": "space_mcp_test",
				"channel": "architecture",
				"message": "Let's review the MCP server tools",
				"mentions": ["codex"]
			}
		}
	}`)
	resp, err = server.HandleMessage(ctx, pubCall)
	if err != nil || resp.Error != nil {
		t.Fatalf("collaboration.publish failed: err=%v, respErr=%+v", err, resp.Error)
	}

	// 3. collaboration.inbox
	inboxCall := []byte(`{
		"jsonrpc": "2.0",
		"id": 12,
		"method": "tools/call",
		"params": {
			"name": "collaboration.inbox",
			"arguments": {
				"space_id": "space_mcp_test"
			}
		}
	}`)
	resp, err = server.HandleMessage(ctx, inboxCall)
	if err != nil || resp.Error != nil {
		t.Fatalf("collaboration.inbox failed: err=%v, respErr=%+v", err, resp.Error)
	}

	// 4. collaboration.decide (propose)
	decCall := []byte(`{
		"jsonrpc": "2.0",
		"id": 13,
		"method": "tools/call",
		"params": {
			"name": "collaboration.decide",
			"arguments": {
				"space_id": "space_mcp_test",
				"action": "propose",
				"title": "Use MCP collaboration tools",
				"statement": "Antigravity communicates with peers via collaboration.* MCP tools",
				"rationale": "Enables rich collaboration space coordination without human copying"
			}
		}
	}`)
	resp, err = server.HandleMessage(ctx, decCall)
	if err != nil || resp.Error != nil {
		t.Fatalf("collaboration.decide failed: err=%v, respErr=%+v", err, resp.Error)
	}

	// 5. collaboration.status
	statusCall := []byte(`{
		"jsonrpc": "2.0",
		"id": 14,
		"method": "tools/call",
		"params": {
			"name": "collaboration.status",
			"arguments": {
				"space_id": "space_mcp_test"
			}
		}
	}`)
	resp, err = server.HandleMessage(ctx, statusCall)
	if err != nil || resp.Error != nil {
		t.Fatalf("collaboration.status failed: err=%v, respErr=%+v", err, resp.Error)
	}
}
