package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/economy"
	"github.com/domehahn/harnessmesh/internal/mcp"
	"github.com/domehahn/harnessmesh/internal/modelrouting"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

type testHarness struct {
	name        string
	caps        config.AgentCapabilities
	invokeFunc  func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error)
	invocations int
}

func (h *testHarness) ID() string                                               { return h.name }
func (h *testHarness) AdapterType() string                                      { return "test" }
func (h *testHarness) Name() string                                             { return h.name }
func (h *testHarness) Capabilities() config.AgentCapabilities                   { return h.caps }
func (h *testHarness) Health(ctx context.Context) error                         { return nil }
func (h *testHarness) Start(ctx context.Context, repo string) (string, error)   { return "", nil }
func (h *testHarness) Resume(ctx context.Context, sessionID, repo string) error { return nil }
func (h *testHarness) StartSession(ctx context.Context, repo string) (string, error) {
	return "test-sess", nil
}
func (h *testHarness) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return nil
}
func (h *testHarness) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}
func (h *testHarness) Invoke(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
	h.invocations++
	if h.invokeFunc != nil {
		return h.invokeFunc(ctx, req)
	}
	return agent.InvokeResult{AgentName: h.name, Text: "default test response"}, nil
}
func (h *testHarness) Run(ctx context.Context, req agent.Request) (protocol.AgentResult, error) {
	inv, err := h.Invoke(ctx, agent.InvokeRequest{
		Name:         req.Name,
		Repo:         req.Repo,
		Prompt:       req.Prompt,
		SessionID:    req.SessionID,
		ReviewMode:   req.ReviewMode,
		ReviewSchema: req.ReviewSchema,
	})
	return protocol.AgentResult{
		AgentName: inv.AgentName,
		SessionID: inv.SessionID,
		Text:      inv.Text,
	}, err
}

func setupIntegrationEnv(t *testing.T, harnesses map[string]agent.Harness, cfgModifier func(*config.Config)) (*collaboration.Engine, store.Store, *config.Config, string) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "integration.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}

	cfg := &config.Config{
		Version: 2,
		Collaboration: config.CollaborationConfig{
			MaxPeerRounds: 3,
			MaxPeerDepth:  2,
			MaxPeerCalls:  10,
		},
		Workflow: config.WorkflowConfig{
			Executor:     "claude",
			Reviewer:     "codex",
			MaxRounds:    3,
			StopOnRepeat: true,
		},
		Context: config.ContextConfig{
			MaxDiffChars: 50000,
		},
		Agents: map[string]config.AgentConfig{
			"claude": {
				Kind:     "claude",
				Role:     "executor",
				Writable: true,
				Capabilities: config.AgentCapabilities{
					ReadRepository:  true,
					WriteRepository: true,
					RunCommands:     true,
					Review:          true,
					AnswerQuestions: true,
					SubmitEvidence:  true,
				},
			},
			"codex": {
				Kind:     "codex",
				Role:     "reviewer",
				Writable: false,
				Capabilities: config.AgentCapabilities{
					ReadRepository:  true,
					WriteRepository: false,
					RunCommands:     true,
					Review:          true,
					AnswerQuestions: true,
					SubmitEvidence:  true,
				},
			},
		},
	}

	if cfgModifier != nil {
		cfgModifier(cfg)
	}

	proj := contextpack.New(tmpDir, cfg.Context)
	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Projector: proj,
		Harnesses: harnesses,
	})

	return eng, st, cfg, tmpDir
}

// Scenario 1: Claude -> peer.ask(Codex) -> Codex answer -> Claude continues
func TestScenario1_ClaudeAskCodex(t *testing.T) {
	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{AnswerQuestions: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			if !strings.Contains(req.Prompt, "refresh-token") {
				t.Errorf("prompt missing question: %s", req.Prompt)
			}
			return agent.InvokeResult{
				AgentName: "codex",
				Text:      "The token refresh logic is safe if callers use AcquireLock().",
			}, nil
		},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{"codex": codex}, nil)
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_scen_1", "Implement auth refresh")
	if err != nil {
		t.Fatal(err)
	}

	ans, err := eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{
		Peer:     "codex",
		Question: "Check refresh-token concurrency handling",
	}, 1, "idem_1")
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}

	if !strings.Contains(ans.Answer, "AcquireLock()") {
		t.Fatalf("unexpected answer: %s", ans.Answer)
	}

	// Verify interaction persisted in state
	messages, err := st.GetMessages(ctx, sess.ID)
	if err != nil || len(messages) != 1 {
		t.Fatalf("expected 1 persisted message, got: %d", len(messages))
	}
}

// Scenario 2: Claude -> request_review -> Codex changes_required -> findings returned -> Claude fixes -> Codex approve
func TestScenario2_ReviewCycleApprove(t *testing.T) {
	rounds := 0
	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			rounds++
			if rounds == 1 {
				rev := protocol.ReviewResult{
					Verdict: protocol.VerdictChangesRequired,
					Summary: "Found concurrency flaw",
					Findings: []protocol.Finding{
						{
							ID:             "HM-RACE-001",
							Severity:       "high",
							Claim:          "token map not locked",
							Evidence:       "auth.go:42",
							Recommendation: "add sync.Mutex",
						},
					},
				}
				raw, _ := json.Marshal(rev)
				return agent.InvokeResult{AgentName: "codex", Text: string(raw)}, nil
			}

			// Round 2 approve
			rev := protocol.ReviewResult{
				Verdict: protocol.VerdictApprove,
				Summary: "All issues fixed",
			}
			raw, _ := json.Marshal(rev)
			return agent.InvokeResult{AgentName: "codex", Text: string(raw)}, nil
		},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{"codex": codex}, nil)
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_scen_2", "Fix auth concurrency")
	if err != nil {
		t.Fatal(err)
	}

	// Round 1
	rev1, err := eng.RequestReview(ctx, sess.ID, "claude", protocol.ReviewRequestPayload{
		Peer:  "codex",
		Focus: []string{"concurrency"},
	}, 1, "")
	if err != nil {
		t.Fatalf("round 1 review failed: %v", err)
	}
	if rev1.Status != "changes_required" || len(rev1.Findings) != 1 {
		t.Fatalf("expected changes_required with 1 finding, got: %+v", rev1)
	}

	// Round 2
	rev2, err := eng.RequestReview(ctx, sess.ID, "claude", protocol.ReviewRequestPayload{
		Peer: "codex",
	}, 1, "")
	if err != nil {
		t.Fatalf("round 2 review failed: %v", err)
	}
	if rev2.Status != "approve" || len(rev2.Findings) != 0 {
		t.Fatalf("expected approve with 0 findings, got: %+v", rev2)
	}
}

// Scenario 3: Codex challenges Claude finding -> evidence submitted -> finding resolved
func TestScenario3_ChallengeAndEvidenceResolution(t *testing.T) {
	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{}, nil)
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_scen_3", "Evidence resolution")
	if err != nil {
		t.Fatal(err)
	}

	// Finding published
	finding := protocol.FindingPayload{
		ID:             "HM-001",
		SourceAgent:    "claude",
		Severity:       "high",
		Claim:          "potential dead-lock in sync worker",
		Evidence:       "worker.go:50",
		Recommendation: "reorder mutex locks",
		Status:         protocol.FindingOpen,
	}
	_, err = eng.SubmitFinding(ctx, sess.ID, "claude", finding)
	if err != nil {
		t.Fatal(err)
	}

	// Codex challenges
	_, err = eng.Challenge(ctx, sess.ID, "codex", protocol.ChallengePayload{
		FindingID:  finding.ID,
		Challenger: "codex",
		Claim:      "Locks are acquired in uniform order across all callers",
		Evidence:   "caller.go:12 and worker.go:50",
	})
	if err != nil {
		t.Fatal(err)
	}

	fAfterChallenge, _ := st.GetFinding(ctx, sess.ID, finding.ID)
	if fAfterChallenge.Status != protocol.FindingDisputed {
		t.Fatalf("expected finding disputed, got: %s", fAfterChallenge.Status)
	}

	// Submit evidence: test run passed with race detector
	exitCode := 0
	_, err = eng.SubmitEvidence(ctx, sess.ID, "codex", protocol.EvidencePayload{
		ID:          "ev_race_pass",
		FindingID:   finding.ID,
		Type:        protocol.EvidenceTestResult,
		Command:     "go test -race ./...",
		Result:      "PASS",
		ExitCode:    &exitCode,
		SourceAgent: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Resolve finding as rejected based on evidence
	_, err = eng.Resolve(ctx, sess.ID, "claude", protocol.ResolutionPayload{
		ID:                 "res_001",
		FindingID:          finding.ID,
		Status:             protocol.ResolutionRejected,
		Rationale:          "Tests pass cleanly under race detector and uniform lock ordering verified",
		EvidenceReferences: []string{"ev_race_pass"},
		ResolvingAgent:     "claude",
	})
	if err != nil {
		t.Fatal(err)
	}

	fResolved, _ := st.GetFinding(ctx, sess.ID, finding.ID)
	if fResolved.Status != protocol.FindingDismissed {
		t.Fatalf("expected finding dismissed, got: %s", fResolved.Status)
	}
}

// Scenario 4: Recursive peer call exceeds max_peer_depth
func TestScenario4_PeerDepthExceeded(t *testing.T) {
	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{
		"codex": &testHarness{name: "codex", caps: config.AgentCapabilities{AnswerQuestions: true}},
	}, func(c *config.Config) {
		c.Collaboration.MaxPeerDepth = 2
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_scen_4", "Depth check")
	if err != nil {
		t.Fatal(err)
	}

	// Depth 2 call must be rejected when MaxPeerDepth = 2
	_, err = eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{Peer: "codex", Question: "Nested call"}, 2, "")
	if err == nil {
		t.Fatal("expected depth exceeded error, got nil")
	}
	var depthErr *protocol.PeerDepthExceededError
	if !errors.As(err, &depthErr) {
		t.Fatalf("expected PeerDepthExceededError, got: %T: %v", err, err)
	}
}

// Scenario 5: Peer timeout
func TestScenario5_PeerTimeout(t *testing.T) {
	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{AnswerQuestions: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{}, &protocol.PeerTimeoutError{
				Peer:    "codex",
				Timeout: 100 * time.Millisecond,
			}
		},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{"codex": codex}, nil)
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_scen_5", "Timeout check")
	if err != nil {
		t.Fatal(err)
	}

	_, err = eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{Peer: "codex", Question: "Sleep"}, 1, "")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	var timeoutErr *protocol.PeerTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("expected PeerTimeoutError, got %T: %v", err, err)
	}
	if !protocol.IsTransient(err) {
		t.Fatal("expected timeout to be classified as transient")
	}
}

// Scenario 6: Peer unavailable
func TestScenario6_PeerUnavailable(t *testing.T) {
	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{}, nil)
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_scen_6", "Unavailable peer")
	if err != nil {
		t.Fatal(err)
	}

	_, err = eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{Peer: "nonexistent", Question: "Hi"}, 1, "")
	if err == nil {
		t.Fatal("expected peer unavailable error, got nil")
	}
	var unavailErr *protocol.PeerUnavailableError
	if !errors.As(err, &unavailErr) {
		t.Fatalf("expected PeerUnavailableError, got %T: %v", err, err)
	}
}

// Scenario 7: Budget exhausted
func TestScenario7_BudgetExhausted(t *testing.T) {
	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{AnswerQuestions: true},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{"codex": codex}, func(c *config.Config) {
		c.Collaboration.MaxPeerCalls = 2
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_scen_7", "Budget check")
	if err != nil {
		t.Fatal(err)
	}

	// Call 1: OK
	_, err = eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{Peer: "codex", Question: "Q1"}, 1, "id1")
	if err != nil {
		t.Fatal(err)
	}
	// Call 2: OK
	_, err = eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{Peer: "codex", Question: "Q2"}, 1, "id2")
	if err != nil {
		t.Fatal(err)
	}
	// Call 3: Exceeds max_peer_calls = 2
	_, err = eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{Peer: "codex", Question: "Q3"}, 1, "id3")
	if err == nil {
		t.Fatal("expected budget exceeded error, got nil")
	}
	var budgetErr *protocol.BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected BudgetExceededError, got %T: %v", err, err)
	}
}

// Scenario 8: Duplicate/idempotent request
func TestScenario8_Idempotency(t *testing.T) {
	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{AnswerQuestions: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{
				AgentName: "codex",
				Text:      "Idempotent answer",
			}, nil
		},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{"codex": codex}, nil)
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_scen_8", "Idempotency check")
	if err != nil {
		t.Fatal(err)
	}

	ans1, err := eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{Peer: "codex", Question: "Q"}, 1, "idem_stable_1")
	if err != nil {
		t.Fatal(err)
	}
	ans2, err := eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{Peer: "codex", Question: "Q"}, 1, "idem_stable_1")
	if err != nil {
		t.Fatal(err)
	}

	if ans1.Answer != ans2.Answer {
		t.Fatalf("answers do not match: %s != %s", ans1.Answer, ans2.Answer)
	}
	if codex.invocations != 1 {
		t.Fatalf("expected 1 invocation, got %d", codex.invocations)
	}
}

// Scenario 9: Secret path excluded from context
func TestScenario9_SecretPathExcluded(t *testing.T) {
	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{
		"codex": &testHarness{name: "codex", caps: config.AgentCapabilities{AnswerQuestions: true}},
	}, nil)
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_scen_9", "Secret path test")
	if err != nil {
		t.Fatal(err)
	}

	// Attempting to scope a secret file like .env must return ContextRejectedError
	_, err = eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{
		Peer:     "codex",
		Question: "Check .env file",
		Scope:    []string{".env"},
	}, 1, "")
	if err == nil {
		t.Fatal("expected context rejection for .env, got nil")
	}
	var rejErr *protocol.ContextRejectedError
	if !errors.As(err, &rejErr) {
		t.Fatalf("expected ContextRejectedError, got %T: %v", err, err)
	}
}

// Scenario 10: Same findings repeatedly -> stalled
func TestScenario10_StalledLoop(t *testing.T) {
	reviewJSON, _ := json.Marshal(protocol.ReviewResult{
		Verdict: protocol.VerdictChangesRequired,
		Summary: "Repeated finding",
		Findings: []protocol.Finding{
			{
				ID:             "HM-STALL-001",
				Severity:       "high",
				Claim:          "unresolved race",
				Evidence:       "main.go:1",
				Recommendation: "fix it",
			},
		},
	})

	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{
				AgentName: "codex",
				Text:      string(reviewJSON),
			}, nil
		},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{"codex": codex}, nil)
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_scen_10", "Stall test")
	if err != nil {
		t.Fatal(err)
	}

	req := protocol.ReviewRequestPayload{Peer: "codex"}
	// Round 1
	_, err = eng.RequestReview(ctx, sess.ID, "claude", req, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	// Round 2 (same findings repeated)
	_, err = eng.RequestReview(ctx, sess.ID, "claude", req, 1, "")
	if err == nil {
		t.Fatal("expected stall error, got nil")
	}
	var stallErr *protocol.StalledError
	if !errors.As(err, &stallErr) {
		t.Fatalf("expected StalledError, got %T: %v", err, err)
	}
}

// Scenario 11: v0.1 collaborate workflow remains functional
func TestScenario11_V01CollaborateRemainsFunctional(t *testing.T) {
	executor := &fakeAgent{name: "claude", results: []protocol.AgentResult{
		{AgentName: "claude", Text: "implemented feature"},
		{AgentName: "claude", Text: "addressed review"},
	}}
	reviewer := &fakeAgent{name: "codex", results: []protocol.AgentResult{
		{
			AgentName: "codex",
			Text: reviewJSON(protocol.VerdictChangesRequired, []protocol.Finding{{
				ID:             "HM-LEGACY-001",
				Severity:       "medium",
				Claim:          "missing docs",
				Evidence:       "pkg.go:1",
				Recommendation: "add godoc",
			}}),
		},
		{
			AgentName: "codex",
			Text:      reviewJSON(protocol.VerdictApprove, nil),
		},
	}}

	tmpDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "v01.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		Version: 1,
		Workflow: config.WorkflowConfig{
			Executor:     "claude",
			Reviewer:     "codex",
			MaxRounds:    3,
			StopOnRepeat: true,
		},
		Agents: map[string]config.AgentConfig{
			"claude": {Kind: "claude", Role: "executor", Writable: true},
			"codex":  {Kind: "codex", Role: "reviewer", Writable: false},
		},
	}

	runner := Runner{
		Config:    cfg,
		Repo:      tmpDir,
		Executor:  executor,
		Reviewer:  reviewer,
		Projector: fakeProjector{},
		Store:     st,
	}

	result, err := runner.Run(context.Background(), "v0.1 legacy task")
	if err != nil {
		t.Fatalf("v0.1 workflow failed: %v", err)
	}
	if result.Status != "approved" {
		t.Fatalf("expected approved, got: %s", result.Status)
	}
	if len(result.Rounds) != 2 {
		t.Fatalf("expected 2 rounds, got: %d", len(result.Rounds))
	}

	// Verify it was stored in SQLite
	persistedSess, err := st.GetSession(context.Background(), result.RunID)
	if err != nil || persistedSess.Status != "approved" {
		t.Fatalf("expected persisted approved session, err=%v, sess=%+v", err, persistedSess)
	}
}

// Scenario 12: MCP tools can be enumerated and invoked by an MCP client
func TestScenario12_MCPEnumerationAndCall(t *testing.T) {
	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{AnswerQuestions: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{
				AgentName: "codex",
				Text:      "MCP tool call success: verified concurrency.",
			}, nil
		},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{"codex": codex}, nil)
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_scen_12", "MCP test task")
	if err != nil {
		t.Fatal(err)
	}

	server := mcp.NewServer(eng, sess.ID, "claude")

	// 1. Enumerate tools via tools/list
	listReq := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	listResp, err := server.HandleMessage(ctx, listReq)
	if err != nil || listResp.Error != nil {
		t.Fatalf("tools/list failed: err=%v, resp=%+v", err, listResp)
	}
	resMap := listResp.Result.(map[string]any)
	tools := resMap["tools"].([]mcp.ToolDefinition)
	if len(tools) < 11 {
		t.Fatalf("expected at least 11 tools, got: %d", len(tools))
	}

	// 2. Invoke peer.ask tool
	callReq := []byte(`{
		"jsonrpc": "2.0",
		"id": 2,
		"method": "tools/call",
		"params": {
			"name": "peer.ask",
			"arguments": {
				"peer": "codex",
				"question": "Inspect auth locking",
				"context": {"include_diff": false}
			}
		}
	}`)
	callResp, err := server.HandleMessage(ctx, callReq)
	if err != nil || callResp.Error != nil {
		t.Fatalf("tools/call failed: err=%v, resp=%+v", err, callResp)
	}

	toolRes := callResp.Result.(mcp.ToolCallResult)
	if toolRes.IsError || len(toolRes.Content) == 0 {
		t.Fatalf("unexpected tool call result: %+v", toolRes)
	}
	if !strings.Contains(toolRes.Content[0].Text, "verified concurrency") {
		t.Fatalf("unexpected tool result text: %s", toolRes.Content[0].Text)
	}
}

// Scenario 13: Capability-based routing & peer discovery
func TestScenario13_CapabilityRoutingAndPeerDiscovery(t *testing.T) {
	antigravity := &testHarness{
		name: "antigravity",
		caps: config.AgentCapabilities{Review: true, AnswerQuestions: true},
	}
	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{Review: true, AnswerQuestions: true},
	}

	eng, st, cfg, _ := setupIntegrationEnv(t, map[string]agent.Harness{
		"antigravity": antigravity,
		"codex":       codex,
	}, func(c *config.Config) {
		c.Agents["antigravity"] = config.AgentConfig{
			Kind:  "antigravity",
			Role:  "reviewer",
			Roles: []string{"security_review", "reviewer"},
		}
		c.Agents["codex"] = config.AgentConfig{
			Kind:  "codex",
			Role:  "reviewer",
			Roles: []string{"performance_review", "reviewer"},
		}
		c.CapabilityRouting = map[string][]string{
			"security_review": {"antigravity"},
		}
	})
	defer st.Close()
	_ = cfg

	ctx := context.Background()

	// Peer list
	list, err := eng.ListParticipants(ctx, "")
	if err != nil {
		t.Fatalf("ListParticipants failed: %v", err)
	}
	if len(list.Participants) < 2 {
		t.Fatalf("expected at least 2 participants, got %d", len(list.Participants))
	}

	// Discover capabilities
	disc, err := eng.DiscoverCapabilities(ctx, protocol.PeerCapabilitiesRequest{Capability: "security_review"})
	if err != nil {
		t.Fatalf("DiscoverCapabilities failed: %v", err)
	}
	if len(disc.Matches) != 1 || disc.Matches[0].Participant != "antigravity" {
		t.Fatalf("expected antigravity match for security_review, got %+v", disc)
	}

	// Route participant
	resolved, err := eng.ResolveParticipant("security_review", "")
	if err != nil {
		t.Fatalf("ResolveParticipant failed: %v", err)
	}
	if resolved != "antigravity" {
		t.Fatalf("expected antigravity, got %s", resolved)
	}
}

// Scenario 14: Parallel multi-review with bounded concurrency and conservative deduplication
func TestScenario14_ParallelMultiReviewAndDeduplication(t *testing.T) {
	reviewJSON1, _ := json.Marshal(protocol.ReviewResult{
		Verdict: protocol.VerdictChangesRequired,
		Summary: "Security issue found",
		Findings: []protocol.Finding{
			{
				ID:             "SEC-001",
				Severity:       "high",
				Claim:          "SQL injection in query",
				File:           "store/db.go",
				Line:           10,
				Evidence:       "db.go:10",
				Recommendation: "use prepared statements",
			},
		},
	})
	reviewJSON2, _ := json.Marshal(protocol.ReviewResult{
		Verdict: protocol.VerdictChangesRequired,
		Summary: "Performance & security check",
		Findings: []protocol.Finding{
			{
				ID:             "CODEX-001",
				Severity:       "high",
				Claim:          "SQL injection in query", // Duplicate
				File:           "store/db.go",
				Line:           10,
				Evidence:       "db.go:10",
				Recommendation: "parameterize query",
			},
			{
				ID:             "PERF-001",
				Severity:       "medium",
				Claim:          "Unbuffered channel causes latency",
				File:           "worker/queue.go",
				Line:           50,
				Evidence:       "queue.go:50",
				Recommendation: "buffer channel",
			},
		},
	})

	h1 := &testHarness{
		name: "antigravity",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{AgentName: "antigravity", Text: string(reviewJSON1)}, nil
		},
	}
	h2 := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{AgentName: "codex", Text: string(reviewJSON2)}, nil
		},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{
		"antigravity": h1,
		"codex":       h2,
	}, func(c *config.Config) {
		c.Collaboration.MaxParallelPeers = 2
		c.Agents["antigravity"] = config.AgentConfig{Kind: "antigravity", Role: "reviewer", Writable: false}
		c.Agents["codex"] = config.AgentConfig{Kind: "codex", Role: "reviewer", Writable: false}
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "multi-rev-test", "Multi-review verification")
	if err != nil {
		t.Fatal(err)
	}

	rev, err := eng.RequestReview(ctx, sess.ID, "claude", protocol.ReviewRequestPayload{
		Reviewers: []protocol.ReviewerSpec{
			{Peer: "antigravity", Focus: []string{"security"}},
			{Peer: "codex", Focus: []string{"performance"}},
		},
	}, 1, "")
	if err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}

	if rev.Status != "changes_required" {
		t.Fatalf("expected changes_required status, got %s", rev.Status)
	}

	// Conservative deduplication check
	var dupCount int
	for _, f := range rev.Findings {
		if f.DuplicateOf != "" {
			dupCount++
		}
	}
	if dupCount != 1 {
		t.Fatalf("expected exactly 1 duplicate finding marked, got %d", dupCount)
	}
}

// Scenario 15: Single-writer workspace invariant rejection
func TestScenario15_SingleWriterWorkspaceInvariant(t *testing.T) {
	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{}, func(c *config.Config) {
		c.Agents["writer1"] = config.AgentConfig{Kind: "claude", Writable: true}
		c.Agents["writer2"] = config.AgentConfig{Kind: "antigravity", Writable: true}
	})
	defer st.Close()

	ctx := context.Background()
	_, err := eng.CreateSession(ctx, "illegal-multi-writer", "Must fail")
	if err == nil {
		t.Fatal("expected single writer conflict error, got nil")
	}

	var wErr *protocol.WriterConflictError
	if !errors.As(err, &wErr) {
		t.Fatalf("expected WriterConflictError, got %T: %v", err, err)
	}
}

// Acceptance Scenario B: Google Antigravity executor with Codex review
func TestAcceptanceScenarioB_AntigravityExecutorCodexReview(t *testing.T) {
	reviewJSON, _ := json.Marshal(protocol.ReviewResult{
		Verdict:  protocol.VerdictApprove,
		Summary:  "Implementation cleanly adheres to design",
		Findings: []protocol.Finding{},
	})

	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{AgentName: "codex", Text: string(reviewJSON)}, nil
		},
	}
	antigravity := &testHarness{
		name: "antigravity",
		caps: config.AgentCapabilities{ReadRepository: true, WriteRepository: true},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{
		"antigravity": antigravity,
		"codex":       codex,
	}, func(c *config.Config) {
		delete(c.Agents, "claude")
		c.Workflow.Executor = "antigravity"
		c.Agents["antigravity"] = config.AgentConfig{Kind: "antigravity", Role: "executor", Writable: true}
		c.Agents["codex"] = config.AgentConfig{Kind: "codex", Role: "reviewer", Writable: false}
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "antigravity-exec", "Antigravity task")
	if err != nil {
		t.Fatal(err)
	}

	rev, err := eng.RequestReview(ctx, sess.ID, "antigravity", protocol.ReviewRequestPayload{
		Peer: "codex",
	}, 1, "")
	if err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}

	if rev.Status != "approve" && rev.Status != "approved" {
		t.Fatalf("expected approved review, got %s", rev.Status)
	}
}

// Acceptance Scenario C: GitHub Copilot CLI executor with Codex review
func TestAcceptanceScenarioC_CopilotExecutorCodexReview(t *testing.T) {
	reviewJSON, _ := json.Marshal(protocol.ReviewResult{
		Verdict:  protocol.VerdictApprove,
		Summary:  "Copilot changes approved",
		Findings: []protocol.Finding{},
	})

	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{AgentName: "codex", Text: string(reviewJSON)}, nil
		},
	}
	copilot := &testHarness{
		name: "copilot",
		caps: config.AgentCapabilities{ReadRepository: true, WriteRepository: true},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{
		"copilot": copilot,
		"codex":   codex,
	}, func(c *config.Config) {
		delete(c.Agents, "claude")
		c.Workflow.Executor = "copilot"
		c.Agents["copilot"] = config.AgentConfig{Kind: "copilot-cli", Role: "executor", Writable: true}
		c.Agents["codex"] = config.AgentConfig{Kind: "codex", Role: "reviewer", Writable: false}
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "copilot-exec", "Copilot task")
	if err != nil {
		t.Fatal(err)
	}

	rev, err := eng.RequestReview(ctx, sess.ID, "copilot", protocol.ReviewRequestPayload{
		Peer: "codex",
	}, 1, "")
	if err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}

	if rev.Status != "approve" && rev.Status != "approved" {
		t.Fatalf("expected approved review, got %s", rev.Status)
	}
}

// Acceptance Scenario D: Parallel multi-reviewer run (Claude executor + Antigravity security + Codex performance)
func TestAcceptanceScenarioD_ParallelMultiReviewer(t *testing.T) {
	secReviewJSON, _ := json.Marshal(protocol.ReviewResult{
		Verdict:  protocol.VerdictApprove,
		Summary:  "Security verification passed",
		Findings: []protocol.Finding{},
	})
	perfReviewJSON, _ := json.Marshal(protocol.ReviewResult{
		Verdict:  protocol.VerdictApprove,
		Summary:  "Performance verification passed",
		Findings: []protocol.Finding{},
	})

	antigravity := &testHarness{
		name: "antigravity",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{AgentName: "antigravity", Text: string(secReviewJSON)}, nil
		},
	}
	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{AgentName: "codex", Text: string(perfReviewJSON)}, nil
		},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{
		"antigravity": antigravity,
		"codex":       codex,
	}, func(c *config.Config) {
		c.Collaboration.MaxParallelPeers = 3
		c.Agents["antigravity"] = config.AgentConfig{Kind: "antigravity", Role: "reviewer", Writable: false}
		c.Agents["codex"] = config.AgentConfig{Kind: "codex", Role: "reviewer", Writable: false}
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "multi-acceptance-d", "Parallel multi-review")
	if err != nil {
		t.Fatal(err)
	}

	rev, err := eng.RequestReview(ctx, sess.ID, "claude", protocol.ReviewRequestPayload{
		Reviewers: []protocol.ReviewerSpec{
			{Peer: "antigravity", Focus: []string{"security"}},
			{Peer: "codex", Focus: []string{"performance"}},
		},
	}, 1, "")
	if err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}

	if rev.Status != "approve" && rev.Status != "approved" {
		t.Fatalf("expected approved status, got %s", rev.Status)
	}
}

// Acceptance Scenario A: Claude Code executor with OpenAI Codex reviewer
func TestAcceptanceScenarioA_ClaudeCodeExecutorCodexReview(t *testing.T) {
	reviewJSON, _ := json.Marshal(protocol.ReviewResult{
		Verdict:  protocol.VerdictApprove,
		Summary:  "Implementation is clean and passes all checks",
		Findings: []protocol.Finding{},
	})

	claude := &testHarness{
		name: "claude",
		caps: config.AgentCapabilities{WriteRepository: true, RunCommands: true},
	}
	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{AgentName: "codex", Text: string(reviewJSON)}, nil
		},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{
		"claude": claude,
		"codex":  codex,
	}, func(c *config.Config) {
		c.Workflow.Executor = "claude"
		c.Workflow.Reviewer = "codex"
		c.Agents["claude"] = config.AgentConfig{Kind: "claude-code", Role: "executor", Writable: true}
		c.Agents["codex"] = config.AgentConfig{Kind: "codex", Role: "reviewer", Writable: false}
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "acceptance-a", "Claude + Codex acceptance test")
	if err != nil {
		t.Fatal(err)
	}

	rev, err := eng.RequestReview(ctx, sess.ID, "claude", protocol.ReviewRequestPayload{
		Peer: "codex",
	}, 1, "")
	if err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}

	if rev.Status != "approve" && rev.Status != "approved" {
		t.Fatalf("expected approved status, got %s", rev.Status)
	}
}

// Acceptance Scenario E: Reviewer challenge / debate loop with evidence and resolution
func TestAcceptanceScenarioE_ChallengeDebateLoop(t *testing.T) {
	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{}, func(c *config.Config) {})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "acceptance-e", "Debate loop task")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Submit finding
	fResp, err := eng.SubmitFinding(ctx, sess.ID, "codex", protocol.FindingPayload{
		ID:             "FIND-CHALLENGE-1",
		Severity:       "high",
		Category:       "correctness",
		Claim:          "Nil pointer dereference in auth handler",
		File:           "auth/auth.go",
		Line:           45,
		Evidence:       "auth/auth.go:45",
		Recommendation: "add check for nil user",
	})
	if err != nil {
		t.Fatalf("SubmitFinding failed: %v", err)
	}
	if fResp.ID != "FIND-CHALLENGE-1" {
		t.Fatalf("unexpected finding ID: %s", fResp.ID)
	}

	// 2. Challenge finding
	chalResp, err := eng.Challenge(ctx, sess.ID, "claude", protocol.ChallengePayload{
		FindingID:             "FIND-CHALLENGE-1",
		Claim:                 "User is guaranteed non-nil by upstream middleware",
		Evidence:              "middleware/auth.go:12",
		RequestedVerification: "Check middleware enforcement",
	})
	if err != nil {
		t.Fatalf("Challenge failed: %v", err)
	}
	if chalResp.FindingID != "FIND-CHALLENGE-1" {
		t.Fatalf("unexpected challenged finding ID: %s", chalResp.FindingID)
	}

	// 3. Submit evidence
	evResp, err := eng.SubmitEvidence(ctx, sess.ID, "claude", protocol.EvidencePayload{
		FindingID: "FIND-CHALLENGE-1",
		Type:      protocol.EvidenceGitDiff,
		Excerpt:   "if user == nil { return http.StatusUnauthorized }",
	})
	if err != nil {
		t.Fatalf("SubmitEvidence failed: %v", err)
	}
	if evResp.FindingID != "FIND-CHALLENGE-1" {
		t.Fatalf("unexpected evidence target finding ID: %s", evResp.FindingID)
	}

	// 4. Resolve finding based on evidence
	resResp, err := eng.Resolve(ctx, sess.ID, "codex", protocol.ResolutionPayload{
		FindingID: "FIND-CHALLENGE-1",
		Status:    protocol.ResolutionConfirmed,
		Rationale: "Verified upstream middleware enforcement",
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if resResp.Status != protocol.ResolutionConfirmed {
		t.Fatalf("expected confirmed status, got %s", resResp.Status)
	}
}

// Scenario 16: One reviewer fails during parallel review -> successful reviewer results retained
func TestScenario16_ParallelReviewPartialFailure(t *testing.T) {
	perfReviewJSON, _ := json.Marshal(protocol.ReviewResult{
		Verdict: protocol.VerdictChangesRequired,
		Summary: "Performance bottleneck detected",
		Findings: []protocol.Finding{
			{
				ID:             "PERF-100",
				Severity:       "medium",
				Claim:          "Unbuffered channel causes goroutine starvation",
				File:           "worker/pool.go",
				Line:           33,
				Evidence:       "worker/pool.go:33",
				Recommendation: "use buffered channel",
			},
		},
	})

	// antigravity fails
	antigravity := &testHarness{
		name: "antigravity",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{}, errors.New("transient remote network timeout")
		},
	}
	// codex succeeds
	codex := &testHarness{
		name: "codex",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{AgentName: "codex", Text: string(perfReviewJSON)}, nil
		},
	}

	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{
		"antigravity": antigravity,
		"codex":       codex,
	}, func(c *config.Config) {
		c.Collaboration.MaxParallelPeers = 2
		c.Agents["antigravity"] = config.AgentConfig{Kind: "antigravity", Role: "reviewer", Writable: false}
		c.Agents["codex"] = config.AgentConfig{Kind: "codex", Role: "reviewer", Writable: false}
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "partial-failure-session", "Partial failure parallel review")
	if err != nil {
		t.Fatal(err)
	}

	rev, err := eng.RequestReview(ctx, sess.ID, "claude", protocol.ReviewRequestPayload{
		Reviewers: []protocol.ReviewerSpec{
			{Peer: "antigravity", Focus: []string{"security"}},
			{Peer: "codex", Focus: []string{"performance"}},
		},
	}, 1, "")
	if err != nil {
		t.Fatalf("RequestReview should succeed despite one reviewer failing, got err: %v", err)
	}

	if rev.Status != "changes_required" {
		t.Fatalf("expected status changes_required from successful reviewer, got %s", rev.Status)
	}
	if len(rev.Findings) != 1 || rev.Findings[0].ID != "PERF-100" {
		t.Fatalf("expected PERF-100 to be preserved, got findings: %+v", rev.Findings)
	}
}

// Economy: cheapest_suitable selects cheap-reviewer for routine task
func TestScenario_Economy_CheapestSuitable_RoutineTask(t *testing.T) {
	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{}, func(c *config.Config) {
		c.Collaboration.PeerSelectionPolicy = "cheapest_suitable"
		c.Agents = map[string]config.AgentConfig{
			"cheap-reviewer": {
				Kind: "codex",
				Role: "reviewer",
				Capabilities: config.AgentCapabilities{
					Review: true,
				},
				Economy: config.EconomyProfile{
					Class:        "efficient",
					RelativeCost: 1.0,
				},
			},
			"strong-reviewer": {
				Kind: "claude",
				Role: "reviewer",
				Capabilities: config.AgentCapabilities{
					Review: true,
				},
				Economy: config.EconomyProfile{
					Class:        "capable",
					RelativeCost: 5.0,
				},
			},
		}
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "econ-routine", "Routine lint fix and style check")
	if err != nil {
		t.Fatal(err)
	}

	participant, err := eng.ResolveParticipantContext(ctx, sess.ID, "review", "", "Routine lint fix", economy.DifficultySignals{})
	if err != nil {
		t.Fatalf("ResolveParticipantContext failed: %v", err)
	}

	if participant != "cheap-reviewer" {
		t.Fatalf("expected cheap-reviewer to be selected, got: %s", participant)
	}
}

// Economy: Verification failure triggers bounded escalation to strong-reviewer
func TestScenario_Economy_VerificationFailure_Escalation(t *testing.T) {
	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{}, func(c *config.Config) {
		c.Collaboration.PeerSelectionPolicy = "cheapest_suitable"
		c.Agents = map[string]config.AgentConfig{
			"cheap-agent": {
				Kind: "codex",
				Role: "reviewer",
				Capabilities: config.AgentCapabilities{
					Review: true,
				},
				Economy: config.EconomyProfile{
					Class:        "efficient",
					RelativeCost: 1.0,
				},
			},
			"strong-agent": {
				Kind: "claude",
				Role: "reviewer",
				Capabilities: config.AgentCapabilities{
					Review: true,
				},
				Economy: config.EconomyProfile{
					Class:        "capable",
					RelativeCost: 5.0,
				},
			},
		}
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "econ-escalate", "Fix edge cases")
	if err != nil {
		t.Fatal(err)
	}

	// First selection gives cheap-agent
	p1, err := eng.ResolveParticipantContext(ctx, sess.ID, "review", "", "Routine bug fix", economy.DifficultySignals{})
	if err != nil {
		t.Fatal(err)
	}
	if p1 != "cheap-agent" {
		t.Fatalf("expected cheap-agent initially, got %s", p1)
	}

	// Verification failure triggers escalation via economy controller
	eng.EconomyController().Escalate(sess.ID, "review")

	p2, err := eng.ResolveParticipantContext(ctx, sess.ID, "review", "", "Routine bug fix", economy.DifficultySignals{})
	if err != nil {
		t.Fatalf("ResolveParticipantContext after escalation failed: %v", err)
	}
	if p2 != "strong-agent" {
		t.Fatalf("expected escalation to strong-agent, got: %s", p2)
	}
}

// Economy: Resolution triggers de-escalation back to efficient participant
func TestScenario_Economy_Resolution_Deescalation(t *testing.T) {
	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{}, func(c *config.Config) {
		c.Collaboration.PeerSelectionPolicy = "cheapest_suitable"
		c.Agents = map[string]config.AgentConfig{
			"cheap-agent": {
				Kind: "codex",
				Role: "reviewer",
				Capabilities: config.AgentCapabilities{
					Review: true,
				},
				Economy: config.EconomyProfile{
					Class:        "efficient",
					RelativeCost: 1.0,
				},
			},
			"strong-agent": {
				Kind: "claude",
				Role: "reviewer",
				Capabilities: config.AgentCapabilities{
					Review: true,
				},
				Economy: config.EconomyProfile{
					Class:        "capable",
					RelativeCost: 5.0,
				},
			},
		}
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "econ-deescalate", "De-escalate task")
	if err != nil {
		t.Fatal(err)
	}

	eng.EconomyController().Escalate(sess.ID, "review")
	eng.EconomyController().Deescalate(sess.ID, "review")

	p, err := eng.ResolveParticipantContext(ctx, sess.ID, "review", "", "Routine bug fix", economy.DifficultySignals{})
	if err != nil {
		t.Fatalf("ResolveParticipantContext failed: %v", err)
	}
	if p != "cheap-agent" {
		t.Fatalf("expected de-escalation to cheap-agent, got: %s", p)
	}
}

// Economy: Capability constraint overrides cost
func TestScenario_Economy_CapabilityConstraint_OverridesCost(t *testing.T) {
	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{}, func(c *config.Config) {
		c.Collaboration.PeerSelectionPolicy = "cheapest_suitable"
		c.Agents = map[string]config.AgentConfig{
			"cheap-general": {
				Kind: "codex",
				Role: "reviewer",
				Capabilities: config.AgentCapabilities{
					Review: false, // does not have review
				},
				Economy: config.EconomyProfile{
					Class:        "efficient",
					RelativeCost: 0.5,
				},
			},
			"capable-reviewer": {
				Kind: "claude",
				Role: "reviewer",
				Capabilities: config.AgentCapabilities{
					Review: true,
				},
				Economy: config.EconomyProfile{
					Class:        "capable",
					RelativeCost: 4.0,
				},
			},
		}
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "econ-cap", "Review task")
	if err != nil {
		t.Fatal(err)
	}

	// Request peer with capability review
	participant, err := eng.ResolveParticipantContext(ctx, sess.ID, "review", "", "Need review capability", economy.DifficultySignals{})
	if err != nil {
		t.Fatalf("ResolveParticipantContext failed: %v", err)
	}
	if participant != "capable-reviewer" {
		t.Fatalf("expected capable-reviewer because cheap-general lacks review cap, got %s", participant)
	}
}

// Economy: Data sensitivity / private repository constraint enforces local participant
func TestScenario_Economy_DataSensitivity_PrivateRepo(t *testing.T) {
	eng, st, _, _ := setupIntegrationEnv(t, map[string]agent.Harness{}, func(c *config.Config) {
		c.Collaboration.PeerSelectionPolicy = "cheapest_suitable"
		c.Agents = map[string]config.AgentConfig{
			"cloud-reviewer": {
				Kind:    "claude",
				Role:    "reviewer",
				IsLocal: false,
				Capabilities: config.AgentCapabilities{
					Review: true,
				},
				Economy: config.EconomyProfile{
					Class:        "capable",
					RelativeCost: 2.0,
				},
			},
			"local-reviewer": {
				Kind:    "antigravity",
				Role:    "reviewer",
				IsLocal: true,
				Capabilities: config.AgentCapabilities{
					Review: true,
				},
				Economy: config.EconomyProfile{
					Class:        "efficient",
					RelativeCost: 3.0,
				},
			},
		}
	})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "econ-priv", "Private task")
	if err != nil {
		t.Fatal(err)
	}

	participant, err := eng.ResolveParticipantContext(ctx, sess.ID, "review", "", "private confidential code audit", economy.DifficultySignals{HasSensitivePaths: true})
	if err != nil {
		t.Fatalf("ResolveParticipantContext failed: %v", err)
	}
	if participant != "local-reviewer" {
		t.Fatalf("expected local-reviewer for private confidential code audit, got %s", participant)
	}
}

// ModelRouting: Switchyard two-level separation (HarnessMesh selects Codex; Switchyard handles models)
func TestScenario_ModelRouting_Switchyard_TwoLevelSeparation(t *testing.T) {
	cfg := &config.Config{
		Version: 2,
		Workflow: config.WorkflowConfig{
			Executor: "claude",
			Reviewer: "codex",
		},
		ModelRoutingBackends: map[string]config.ModelRoutingBackendConfig{
			"switchyard": {
				Type:        "switchyard",
				BaseURL:     "http://127.0.0.1:4000",
				HealthCheck: true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Kind: "codex",
				Role: "reviewer",
				ModelRouting: &config.AgentModelRoutingConfig{
					Type:    "switchyard",
					Backend: "switchyard",
					Route:   "routine",
				},
			},
		},
	}

	backends := modelrouting.BuildRegistry(cfg)
	sb, ok := backends["switchyard"].(*modelrouting.SwitchyardBackend)
	if !ok {
		t.Fatal("expected switchyard backend in registry")
	}

	agentCfg := cfg.Agents["codex"]
	if err := sb.ConfigureParticipant(&agentCfg); err != nil {
		t.Fatalf("ConfigureParticipant failed: %v", err)
	}

	// Switchyard configures proxy environment without HarnessMesh picking specific models
	if !agentCfg.UseSwitchyard {
		t.Error("expected UseSwitchyard to be true")
	}
	if agentCfg.Env["OPENAI_BASE_URL"] != "http://127.0.0.1:4000/v1" {
		t.Errorf("unexpected OPENAI_BASE_URL: %s", agentCfg.Env["OPENAI_BASE_URL"])
	}
}

// ModelRouting: Switchyard disabled -> Direct operation
func TestScenario_ModelRouting_SwitchyardDisabled_DirectOperation(t *testing.T) {
	cfg := &config.Config{
		Version: 2,
		Workflow: config.WorkflowConfig{
			Executor: "claude",
			Reviewer: "codex",
		},
		ModelRoutingBackends: map[string]config.ModelRoutingBackendConfig{
			"direct": {
				Type: "fixed",
			},
		},
		Agents: map[string]config.AgentConfig{
			"claude": {
				Kind: "claude",
				Role: "executor",
				ModelRouting: &config.AgentModelRoutingConfig{
					Type:    "fixed",
					Backend: "direct",
				},
			},
		},
	}

	backends := modelrouting.BuildRegistry(cfg)
	fb, ok := backends["direct"]
	if !ok {
		t.Fatal("expected direct backend")
	}
	if fb.Type() != "fixed" {
		t.Fatalf("expected fixed type, got %s", fb.Type())
	}
	if err := fb.Health(context.Background()); err != nil {
		t.Fatalf("Health check failed: %v", err)
	}
}

// ModelRouting: Switchyard configured but unavailable -> returns typed SwitchyardUnavailableError
func TestScenario_ModelRouting_SwitchyardUnavailable_TypedError(t *testing.T) {
	sb := modelrouting.NewSwitchyardBackend("http://127.0.0.1:4000", true)
	// Use in-memory roundtripper returning 503 Service Unavailable (no network sockets)
	sb.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(bytes.NewBufferString("service unavailable")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	err := sb.Health(context.Background())
	if err == nil {
		t.Fatal("expected error from unhealthy switchyard")
	}

	var syErr *protocol.SwitchyardUnavailableError
	if !errors.As(err, &syErr) {
		t.Fatalf("expected *protocol.SwitchyardUnavailableError, got: %T (%v)", err, err)
	}
	if syErr.URL != "http://127.0.0.1:4000" {
		t.Errorf("unexpected URL in error: %s", syErr.URL)
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
