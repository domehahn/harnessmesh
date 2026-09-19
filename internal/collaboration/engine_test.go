package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

type mockHarness struct {
	name        string
	caps        config.AgentCapabilities
	invokeFunc  func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error)
	invocations int
}

func (m *mockHarness) ID() string                                               { return m.name }
func (m *mockHarness) AdapterType() string                                      { return "mock" }
func (m *mockHarness) Name() string                                             { return m.name }
func (m *mockHarness) Capabilities() config.AgentCapabilities                   { return m.caps }
func (m *mockHarness) Health(ctx context.Context) error                         { return nil }
func (m *mockHarness) Start(ctx context.Context, repo string) (string, error)   { return "", nil }
func (m *mockHarness) Resume(ctx context.Context, sessionID, repo string) error { return nil }
func (m *mockHarness) StartSession(ctx context.Context, repo string) (string, error) {
	return "mock-session-1", nil
}
func (m *mockHarness) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return nil
}
func (m *mockHarness) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}
func (m *mockHarness) Invoke(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
	m.invocations++
	if m.invokeFunc != nil {
		return m.invokeFunc(ctx, req)
	}
	return agent.InvokeResult{AgentName: m.name, Text: "mock answer"}, nil
}
func (m *mockHarness) Run(ctx context.Context, req agent.Request) (protocol.AgentResult, error) {
	inv, err := m.Invoke(ctx, agent.InvokeRequest{
		Name:         req.Name,
		Repo:         req.Repo,
		Prompt:       req.Prompt,
		SessionID:    req.SessionID,
		ReviewMode:   req.ReviewMode,
		ReviewSchema: req.ReviewSchema,
	})
	return protocol.AgentResult{
		AgentName: inv.AgentName,
		Text:      inv.Text,
	}, err
}

func setupTestEngine(t *testing.T, harnesses map[string]agent.Harness) (*Engine, store.Store, string) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "collab.db")
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
			MaxRounds:    3,
			StopOnRepeat: true,
		},
		Agents: map[string]config.AgentConfig{
			"claude": {
				Kind:     "claude",
				Role:     "executor",
				Writable: true,
			},
			"codex": {
				Kind:     "codex",
				Role:     "reviewer",
				Writable: false,
			},
		},
	}

	proj := contextpack.New(tmpDir, cfg.Context)
	eng := NewEngine(EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Projector: proj,
		Harnesses: harnesses,
	})

	return eng, st, tmpDir
}

func TestEngine_AskAndReply(t *testing.T) {
	codexMock := &mockHarness{
		name: "codex",
		caps: config.AgentCapabilities{
			AnswerQuestions: true,
		},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{
				AgentName: "codex",
				SessionID: "codex-thread-1",
				Text:      "Concurrency check passed: mutex is held properly.",
			}, nil
		},
	}

	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": codexMock})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_001", "Implement auth")
	if err != nil {
		t.Fatal(err)
	}

	askReq := protocol.AskRequest{
		Peer:     "codex",
		Question: "Check refresh token mutex",
		Context: protocol.AskContextOptions{
			IncludeDiff: false,
		},
	}

	ans, err := eng.Ask(ctx, sess.ID, "claude", askReq, 1, "idem_ask_1")
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}
	if ans.Answer != "Concurrency check passed: mutex is held properly." {
		t.Fatalf("unexpected answer: %s", ans.Answer)
	}

	// Test idempotency: calling with same key returns cached result without re-invoking mock
	ans2, err := eng.Ask(ctx, sess.ID, "claude", askReq, 1, "idem_ask_1")
	if err != nil {
		t.Fatalf("Ask with idempotency failed: %v", err)
	}
	if ans2.Answer != ans.Answer {
		t.Fatalf("expected identical cached answer, got: %s", ans2.Answer)
	}
	if codexMock.invocations != 1 {
		t.Fatalf("expected 1 invocation due to idempotency, got %d", codexMock.invocations)
	}
}

func TestEngine_DepthLimitReentrancy(t *testing.T) {
	codexMock := &mockHarness{
		name: "codex",
		caps: config.AgentCapabilities{
			AnswerQuestions: true,
		},
	}
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": codexMock})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_depth", "Depth test")
	if err != nil {
		t.Fatal(err)
	}

	// Attempt with depth=2 when MaxPeerDepth=2
	askReq := protocol.AskRequest{Peer: "codex", Question: "Help"}
	_, err = eng.Ask(ctx, sess.ID, "claude", askReq, 2, "")
	if err == nil {
		t.Fatal("expected depth exceeded error, got nil")
	}

	var depthErr *protocol.PeerDepthExceededError
	if !errors.As(err, &depthErr) {
		t.Fatalf("expected PeerDepthExceededError, got %T: %v", err, err)
	}
}

func TestEngine_RequestReviewAndStall(t *testing.T) {
	reviewJSON1, _ := json.Marshal(protocol.ReviewResult{
		Verdict: protocol.VerdictChangesRequired,
		Summary: "Found race condition",
		Findings: []protocol.Finding{
			{
				ID:             "HM-RACE-001",
				Severity:       "high",
				Claim:          "unprotected write",
				Evidence:       "token.go:10",
				Recommendation: "add lock",
			},
		},
	})

	codexMock := &mockHarness{
		name: "codex",
		caps: config.AgentCapabilities{
			Review: true,
		},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{
				AgentName: "codex",
				Text:      string(reviewJSON1),
			}, nil
		},
	}

	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": codexMock})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "hm_review", "Review task")
	if err != nil {
		t.Fatal(err)
	}

	req := protocol.ReviewRequestPayload{
		Peer:  "codex",
		Focus: []string{"concurrency"},
	}

	// Round 1
	rev1, err := eng.RequestReview(ctx, sess.ID, "claude", req, 1, "")
	if err != nil {
		t.Fatalf("Round 1 review failed: %v", err)
	}
	if rev1.Status != "changes_required" || len(rev1.Findings) != 1 {
		t.Fatalf("unexpected round 1 review: %+v", rev1)
	}

	// Round 2 with identical findings should trigger stall detection
	_, err = eng.RequestReview(ctx, sess.ID, "claude", req, 1, "")
	if err == nil {
		t.Fatal("expected stall error on repeated findings, got nil")
	}

	var stallErr *protocol.StalledError
	if !errors.As(err, &stallErr) {
		t.Fatalf("expected StalledError, got %T: %v", err, err)
	}
}

func TestEngine_ResolveParticipantAndCapabilities(t *testing.T) {
	h1 := &mockHarness{
		name: "antigravity",
		caps: config.AgentCapabilities{Review: true},
	}
	h2 := &mockHarness{
		name: "codex",
		caps: config.AgentCapabilities{Review: true},
	}

	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"antigravity": h1, "codex": h2})
	defer st.Close()

	// Update config agents and capability routing
	eng.cfg.Agents = map[string]config.AgentConfig{
		"antigravity": {
			Kind:  "antigravity",
			Role:  "reviewer",
			Roles: []string{"security_review", "reviewer"},
		},
		"codex": {
			Kind:  "codex",
			Role:  "reviewer",
			Roles: []string{"performance_review", "reviewer"},
		},
	}
	eng.cfg.CapabilityRouting = map[string][]string{
		"security_review": {"antigravity"},
	}

	ctx := context.Background()

	// 1. ListParticipants
	list, err := eng.ListParticipants(ctx, "")
	if err != nil {
		t.Fatalf("ListParticipants failed: %v", err)
	}
	if len(list.Participants) != 2 {
		t.Fatalf("expected 2 participants, got %d", len(list.Participants))
	}

	// 2. DiscoverCapabilities
	caps, err := eng.DiscoverCapabilities(ctx, protocol.PeerCapabilitiesRequest{Capability: "security_review"})
	if err != nil {
		t.Fatalf("DiscoverCapabilities failed: %v", err)
	}
	if len(caps.Matches) != 1 || caps.Matches[0].Participant != "antigravity" {
		t.Fatalf("expected antigravity to match security_review, got %+v", caps)
	}

	// 3. ResolveParticipant by capability
	resolved, err := eng.ResolveParticipant("security_review", "")
	if err != nil {
		t.Fatalf("ResolveParticipant failed: %v", err)
	}
	if resolved != "antigravity" {
		t.Fatalf("expected antigravity, got %s", resolved)
	}

	// 4. ResolveParticipant with unknown capability
	_, err = eng.ResolveParticipant("non_existent_cap", "")
	if err == nil {
		t.Fatal("expected error for non_existent_cap, got nil")
	}
}

func TestEngine_MultiReview_ParallelAndDeduplication(t *testing.T) {
	reviewJSON1, _ := json.Marshal(protocol.ReviewResult{
		Verdict: protocol.VerdictChangesRequired,
		Summary: "Security issue found",
		Findings: []protocol.Finding{
			{
				ID:             "SEC-001",
				Severity:       "high",
				Claim:          "SQL injection risk in query",
				File:           "db/query.go",
				Line:           42,
				Evidence:       "db/query.go:42",
				Recommendation: "use parameterized query",
			},
		},
	})

	reviewJSON2, _ := json.Marshal(protocol.ReviewResult{
		Verdict: protocol.VerdictChangesRequired,
		Summary: "Security review findings",
		Findings: []protocol.Finding{
			{
				ID:             "CODEX-SEC-99",
				Severity:       "high",
				Claim:          "SQL injection risk in query", // identical claim, file, line -> duplicate
				File:           "db/query.go",
				Line:           42,
				Evidence:       "db/query.go:42",
				Recommendation: "parameterize SQL",
			},
			{
				ID:             "CODEX-PERF-01",
				Severity:       "medium",
				Claim:          "N+1 query pattern in loop",
				File:           "db/fetch.go",
				Line:           88,
				Evidence:       "db/fetch.go:88",
				Recommendation: "batch query",
			},
		},
	})

	h1 := &mockHarness{
		name: "antigravity",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{AgentName: "antigravity", Text: string(reviewJSON1)}, nil
		},
	}
	h2 := &mockHarness{
		name: "codex",
		caps: config.AgentCapabilities{Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{AgentName: "codex", Text: string(reviewJSON2)}, nil
		},
	}

	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"antigravity": h1, "codex": h2})
	defer st.Close()

	eng.cfg.Agents = map[string]config.AgentConfig{
		"claude": {
			Kind:     "claude",
			Role:     "executor",
			Writable: true,
		},
		"antigravity": {
			Kind: "antigravity",
			Role: "reviewer",
			Capabilities: config.AgentCapabilities{
				Review: true,
			},
		},
		"codex": {
			Kind: "codex",
			Role: "reviewer",
			Capabilities: config.AgentCapabilities{
				Review: true,
			},
		},
	}

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "multi_rev", "Multi review task")
	if err != nil {
		t.Fatal(err)
	}

	// Request parallel multi-review
	revReq := protocol.ReviewRequestPayload{
		Reviewers: []protocol.ReviewerSpec{
			{Peer: "antigravity", Focus: []string{"security"}},
			{Peer: "codex", Focus: []string{"performance"}},
		},
	}

	res, err := eng.RequestReview(ctx, sess.ID, "claude", revReq, 1, "")
	if err != nil {
		t.Fatalf("RequestReview failed: %v", err)
	}

	if res.Status != "changes_required" {
		t.Fatalf("expected status changes_required, got %s", res.Status)
	}

	// Should have findings from both reviewers, and deduplication should mark CODEX-SEC-99 as duplicate
	if len(res.Findings) < 2 {
		t.Fatalf("expected at least 2 findings, got %d", len(res.Findings))
	}

	var duplicateFound bool
	for _, f := range res.Findings {
		if f.DuplicateOf != "" {
			duplicateFound = true
			if f.DuplicateOf != "SEC-001" {
				t.Errorf("expected DuplicateOf to be SEC-001, got %s", f.DuplicateOf)
			}
		}
	}
	if !duplicateFound {
		t.Error("expected duplicate finding to be detected and marked")
	}
}

func TestEngine_SingleWriterEnforcement(t *testing.T) {
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{})
	defer st.Close()

	// Configure two writable agents
	eng.cfg.Agents = map[string]config.AgentConfig{
		"agent1": {Kind: "claude", Writable: true},
		"agent2": {Kind: "codex", Writable: true},
	}

	ctx := context.Background()
	_, err := eng.CreateSession(ctx, "session_conflict", "Test conflict")
	if err == nil {
		t.Fatal("expected error on multiple writable agents, got nil")
	}

	var wErr *protocol.WriterConflictError
	if !errors.As(err, &wErr) {
		t.Fatalf("expected WriterConflictError, got %T: %v", err, err)
	}
}

func TestEngine_BudgetEnforcement(t *testing.T) {
	mockPeer := &mockHarness{
		name: "codex",
		caps: config.AgentCapabilities{
			AnswerQuestions: true,
			Review:          true,
		},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{
				Text: "Review ok",
				Usage: map[string]any{
					"input_tokens":  float64(500),
					"output_tokens": float64(200),
					"cost_usd":      float64(0.05),
				},
			}, nil
		},
	}

	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": mockPeer})
	defer st.Close()

	eng.cfg.Collaboration.MaxTotalTokens = 1000
	eng.cfg.Collaboration.MaxCostUSD = 0.10

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "session_budget", "Budget test")
	if err != nil {
		t.Fatal(err)
	}

	// 1st ask should succeed (500 in + 200 out = 700 tokens, $0.05)
	_, err = eng.Ask(ctx, sess.ID, "user", protocol.AskRequest{
		Peer:     "codex",
		Question: "Test question",
	}, 0, "idem_b1")
	if err != nil {
		t.Fatalf("unexpected error on first ask: %v", err)
	}

	// 2nd ask must fail because committed usage (700 + 700 = 1400) strictly exceeds 1000 max_total_tokens
	_, err = eng.Ask(ctx, sess.ID, "user", protocol.AskRequest{
		Peer:     "codex",
		Question: "Test question 2",
	}, 0, "idem_b2")
	if err == nil {
		t.Fatal("expected BudgetExceededError on second ask exceeding hard limit, got nil")
	}

	var bErr *protocol.BudgetExceededError
	if !errors.As(err, &bErr) {
		t.Fatalf("expected BudgetExceededError, got %T: %v", err, err)
	}
}

func TestEngine_ChannelAuthorization(t *testing.T) {
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{})
	defer st.Close()

	ctx := context.Background()
	parts := map[string]protocol.SpaceParticipant{
		"claude": {ID: "claude", Adapter: "claude", Roles: []string{"executor"}, Mode: protocol.ParticipantModeActive, Writable: true, Capabilities: []string{"write", "read"}},
		"codex":  {ID: "codex", Adapter: "codex", Roles: []string{"reviewer"}, Mode: protocol.ParticipantModeActive, Writable: false, Capabilities: []string{"read", "review", "security"}},
		"guest":  {ID: "guest", Adapter: "guest", Roles: []string{"peer"}, Mode: protocol.ParticipantModeActive, Writable: false, Capabilities: []string{"read"}},
	}
	space, err := eng.SpaceService().CreateSpace(ctx, "space_auth_test", ".", "Auth test", "Purpose", "claude", parts)
	if err != nil {
		t.Fatal(err)
	}

	// Create restricted channel: only "codex" allowed
	err = eng.Store().CreateChannel(ctx, &protocol.Channel{
		ID:                  "restricted",
		SpaceID:             space.ID,
		Name:                "restricted",
		Visibility:          protocol.ChannelVisibilitySelectedParticipants,
		AllowedParticipants: []string{"codex"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Guest publishes to restricted channel -> must fail
	_, err = eng.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   space.ID,
		ChannelID: "restricted",
		From:      "guest",
		Message:   "Unauthorized access attempt",
	})
	if err == nil {
		t.Fatal("expected error for unauthorized participant publishing to restricted channel")
	}

	// Codex publishes to restricted channel -> must succeed
	_, err = eng.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   space.ID,
		ChannelID: "restricted",
		From:      "codex",
		Message:   "Authorized access",
	})
	if err != nil {
		t.Fatalf("expected codex to be authorized, got error: %v", err)
	}
}

func TestEngine_ChannelRecipientAuthorization(t *testing.T) {
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{})
	defer st.Close()

	ctx := context.Background()
	parts := map[string]protocol.SpaceParticipant{
		"claude": {ID: "claude", Adapter: "claude", Roles: []string{"executor"}, Mode: protocol.ParticipantModeActive, Writable: true, Capabilities: []string{"write", "read"}},
		"codex":  {ID: "codex", Adapter: "codex", Roles: []string{"reviewer"}, Mode: protocol.ParticipantModeActive, Writable: false, Capabilities: []string{"read", "review", "security"}},
		"guest":  {ID: "guest", Adapter: "guest", Roles: []string{"peer"}, Mode: protocol.ParticipantModeActive, Writable: false, Capabilities: []string{"read"}},
	}
	space, err := eng.SpaceService().CreateSpace(ctx, "space_recipient_auth", ".", "Recipient Auth test", "Purpose", "claude", parts)
	if err != nil {
		t.Fatal(err)
	}

	// Guest subscribes to wildcard channels "*"
	err = eng.Subscribe(ctx, &protocol.Subscription{
		ID:            "sub_guest_all",
		SpaceID:       space.ID,
		ParticipantID: "guest",
		Channels:      []string{"*"},
		EventTypes:    []string{"*"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create restricted channel: only "codex" allowed
	err = eng.Store().CreateChannel(ctx, &protocol.Channel{
		ID:                  "secret-room",
		SpaceID:             space.ID,
		Name:                "secret-room",
		Visibility:          protocol.ChannelVisibilitySelectedParticipants,
		AllowedParticipants: []string{"codex"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Codex publishes to secret-room mentioning @guest
	pubResp, err := eng.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   space.ID,
		ChannelID: "secret-room",
		From:      "codex",
		Message:   "Top secret msg @guest",
		Mentions:  []string{"guest"},
	})
	if err != nil {
		t.Fatalf("unexpected publish error: %v", err)
	}

	// Verify guest did NOT receive it because guest is not authorized for secret-room
	for _, recipient := range pubResp.DeliveredTo {
		if recipient == "guest" {
			t.Fatal("guest should not receive message from restricted channel secret-room")
		}
	}
	for _, p := range pubResp.PendingInbox {
		if p == "guest" {
			t.Fatal("guest should not have pending inbox for unauthorized restricted channel")
		}
	}
}

func TestEngine_Converse_IdempotencyAndBudget(t *testing.T) {
	harness := &mockHarness{
		name: "codex",
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{
				AgentName: "codex",
				Text:      "LGTM! Everything is clean.",
				Usage: map[string]any{
					"input_tokens":  int64(200),
					"output_tokens": int64(100),
					"cost_usd":      0.005,
				},
			}, nil
		},
	}
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": harness})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "sess_conv_test", "Converse test")
	if err != nil {
		t.Fatal(err)
	}

	// Set budget limit of 700 total tokens (Turn 1 uses 300, Turn 2 uses 300, Turn 3 exceeds)
	sess.Budget.Known = true
	sess.Budget.MaxTotalTokens = 700
	_ = eng.Store().SaveSession(ctx, sess)

	// Turn 1 with idempotency key
	resp1, err := eng.Converse(ctx, sess.ID, "user", protocol.ConverseRequest{
		Peer:    "codex",
		Message: "Please check this design.",
	}, 0, "conv_idem_1")
	if err != nil {
		t.Fatalf("unexpected converse error: %v", err)
	}
	if resp1.Response != "LGTM! Everything is clean." {
		t.Fatalf("unexpected response: %s", resp1.Response)
	}

	// Replay Turn 1 with same idempotency key -> returns cached response without invoking harness
	resp1Replay, err := eng.Converse(ctx, sess.ID, "user", protocol.ConverseRequest{
		Peer:    "codex",
		Message: "Please check this design.",
	}, 0, "conv_idem_1")
	if err != nil {
		t.Fatalf("unexpected converse replay error: %v", err)
	}
	if resp1Replay.MessageID != resp1.MessageID {
		t.Fatalf("expected replayed message ID %s, got %s", resp1.MessageID, resp1Replay.MessageID)
	}

	// Turn 2 with new idempotency key -> total tokens used (300) + est (est ~10) < 350 -> runs, but pushes total to 600
	resp2, err := eng.Converse(ctx, sess.ID, "user", protocol.ConverseRequest{
		Peer:    "codex",
		Message: "Turn 2 message",
	}, 0, "conv_idem_2")
	if err != nil {
		t.Fatalf("unexpected turn 2 error: %v", err)
	}
	if resp2 == nil {
		t.Fatal("expected resp2 not nil")
	}

	// Turn 3 -> budget exceeded (Used total tokens 600 > 350 limit)
	_, err = eng.Converse(ctx, sess.ID, "user", protocol.ConverseRequest{
		Peer:    "codex",
		Message: "Turn 3 message",
	}, 0, "conv_idem_3")
	if err == nil {
		t.Fatal("expected BudgetExceededError for turn 3, got nil")
	}
	var bErr *protocol.BudgetExceededError
	if !errors.As(err, &bErr) {
		t.Fatalf("expected BudgetExceededError, got %T: %v", err, err)
	}
}

func TestEngine_MaxOutputTokensBudget(t *testing.T) {
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{})
	defer st.Close()

	sess := &store.Session{
		ID:     "sess_output_tok",
		Status: "active",
		Budget: protocol.BudgetStatus{
			Known:            true,
			MaxOutputTokens:  1000,
			UsedOutputTokens: 1000, // already reached max
		},
	}
	err := eng.checkBudget(sess, 50)
	if err == nil {
		t.Fatal("expected BudgetExceededError when UsedOutputTokens >= MaxOutputTokens")
	}
	var bErr *protocol.BudgetExceededError
	if !errors.As(err, &bErr) || bErr.Metric != "output_tokens" {
		t.Fatalf("expected output_tokens BudgetExceededError, got %v", err)
	}
}

func TestActivationController_CloudAdapterPolicy(t *testing.T) {
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{})
	defer st.Close()

	actCtrl := eng.ActivationController()

	copilotParticipant := &protocol.SpaceParticipant{
		ID:      "copilot-audit",
		Adapter: "copilot",
	}

	allowed, reason := actCtrl.CheckPrivacyPolicy(copilotParticipant, []string{".env.production"})
	if allowed {
		t.Fatal("expected cloud adapter copilot to be denied access to sensitive .env file")
	}
	if !strings.Contains(reason, "cloud adapter") {
		t.Fatalf("unexpected denial reason: %s", reason)
	}

	localParticipant := &protocol.SpaceParticipant{
		ID:      "local-dev",
		Adapter: "local",
	}
	allowedLocal, _ := actCtrl.CheckPrivacyPolicy(localParticipant, []string{".env.production"})
	if !allowedLocal {
		t.Fatal("expected local adapter to be allowed access")
	}
}

func TestEventBus_Close(t *testing.T) {
	tmpDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "eb_test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eb := NewEventBus(st, 100*time.Millisecond)

	eb.AddListener("space1", func(ctx context.Context, event *protocol.CollaborationEvent) {})
	eb.PublishRepoChange(context.Background(), "space1", "git", "main.go", map[string]any{})

	// Calling Close() should drain/stop coalesce timers cleanly without panic or leak
	eb.Close()

	if len(eb.coalesceWindows) != 0 {
		t.Fatalf("expected coalesceWindows to be empty after Close(), got %d", len(eb.coalesceWindows))
	}
	if len(eb.listeners) != 0 {
		t.Fatalf("expected listeners to be empty after Close(), got %d", len(eb.listeners))
	}
}

func TestEngine_ConcurrentIdempotencySerialization(t *testing.T) {
	var invokeCount int64
	harness := &mockHarness{
		name: "codex",
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			atomic.AddInt64(&invokeCount, 1)
			time.Sleep(50 * time.Millisecond) // simulate harness latency
			return agent.InvokeResult{
				AgentName: "codex",
				Text:      "Serialized response",
			}, nil
		},
	}
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": harness})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "sess_concurrent_idem", "Concurrent idempotency test")
	if err != nil {
		t.Fatal(err)
	}

	const concurrency = 5
	var wg sync.WaitGroup
	responses := make([]*protocol.ConverseResponse, concurrency)
	errorsList := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := eng.Converse(ctx, sess.ID, "user", protocol.ConverseRequest{
				Peer:    "codex",
				Message: "Check concurrency",
			}, 0, "shared_key_123")
			responses[idx] = resp
			errorsList[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errorsList {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v", i, err)
		}
	}
	if atomic.LoadInt64(&invokeCount) != 1 {
		t.Fatalf("expected exactly 1 harness invocation, got %d", atomic.LoadInt64(&invokeCount))
	}
	for i := 1; i < concurrency; i++ {
		if responses[i].MessageID != responses[0].MessageID {
			t.Fatalf("expected matching message IDs, got %s and %s", responses[i].MessageID, responses[0].MessageID)
		}
		if responses[i].Response != "Serialized response" {
			t.Fatalf("unexpected response: %s", responses[i].Response)
		}
	}
}

func TestEngine_EventBusRestrictedChannelAndNoDoubleInvocation(t *testing.T) {
	var codexInvokes int64
	codexHarness := &mockHarness{
		name: "codex",
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			atomic.AddInt64(&codexInvokes, 1)
			return agent.InvokeResult{
				AgentName: "codex",
				Text:      "codex response",
			}, nil
		},
	}
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": codexHarness})
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_restricted_ch"
	sp := &protocol.CollaborationSpace{
		ID:             spaceID,
		WorkspaceID:    ".",
		Title:          "Restricted Channel Space",
		LifecycleState: protocol.SpaceStateActive,
		Participants: map[string]protocol.SpaceParticipant{
			"antigravity": {ID: "antigravity", Adapter: "antigravity", Mode: protocol.ParticipantModeActive},
			"codex":       {ID: "codex", Adapter: "codex", Mode: protocol.ParticipantModeActive},
		},
		Channels: map[string]protocol.Channel{
			"admin-only": {
				ID:                  "admin-only",
				SpaceID:             spaceID,
				Name:                "admin-only",
				Visibility:          protocol.ChannelVisibilitySelectedParticipants,
				AllowedParticipants: []string{"antigravity"},
			},
			"general": {
				ID:         "general",
				SpaceID:    spaceID,
				Name:       "general",
				Visibility: protocol.ChannelVisibilityAll,
			},
		},
	}
	if err := eng.Store().SaveSpace(ctx, sp); err != nil {
		t.Fatal(err)
	}

	// Subscribe codex to all channels
	sub := &protocol.Subscription{
		ID:            "sub_codex_all",
		SpaceID:       spaceID,
		ParticipantID: "codex",
		Channels:      []string{"*"},
		EventTypes:    []string{protocol.EventRepoChanged, protocol.EventMessageCreated},
	}
	if err := eng.Store().SaveSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}

	// 1. Dispatch event on restricted channel "admin-only" where codex is not allowed
	restrictedEvt := &protocol.CollaborationEvent{
		ID:        "evt_restr_1",
		SpaceID:   spaceID,
		Type:      protocol.EventRepoChanged,
		Source:    "antigravity",
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"channel_id": "admin-only",
		},
	}
	eng.handleCollaborationEvent(ctx, restrictedEvt)

	// Verify codex was NOT invoked due to channel authorization
	if atomic.LoadInt64(&codexInvokes) != 0 {
		t.Fatalf("expected 0 invokes for restricted channel, got %d", atomic.LoadInt64(&codexInvokes))
	}

	// 2. Publish message to "general" mentioning codex
	pubResp, err := eng.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "general",
		From:      "antigravity",
		Subject:   "Check",
		Message:   "Hello @codex",
		Mentions:  []string{"codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pubResp == nil {
		t.Fatal("expected pubResp not nil")
	}

	// Exactly 1 invocation occurred via direct Publish execution (NOT double invocation via EventMessageCreated)
	if atomic.LoadInt64(&codexInvokes) != 1 {
		t.Fatalf("expected exactly 1 invoke (direct publish without duplicate via message.created), got %d", atomic.LoadInt64(&codexInvokes))
	}
}

func TestEngine_DLP_RedactionInPrompts(t *testing.T) {
	var capturedAskPrompt, capturedConversePrompt string
	harness := &mockHarness{
		name: "codex",
		caps: config.AgentCapabilities{AnswerQuestions: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			if strings.Contains(req.Prompt, "requesting your assistance") {
				capturedAskPrompt = req.Prompt
			} else {
				capturedConversePrompt = req.Prompt
			}
			return agent.InvokeResult{
				AgentName: "codex",
				Text:      "Clean response",
			}, nil
		},
	}
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": harness})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "sess_dlp_test", "Deploy secret sk-proj-1234567890abcdef1234567890")
	if err != nil {
		t.Fatal(err)
	}

	// Test Ask redaction in Question and Task
	_, err = eng.Ask(ctx, sess.ID, "antigravity", protocol.AskRequest{
		Peer:     "codex",
		Question: "How do I securely use ghp_1111222233334444555566667777888899990000 and AKIAIOSFODNN7EXAMPLE?",
	}, 0, "ask_dlp_1")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(capturedAskPrompt, "sk-proj-") || strings.Contains(capturedAskPrompt, "ghp_") || strings.Contains(capturedAskPrompt, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("Ask prompt contained unredacted secrets:\n%s", capturedAskPrompt)
	}
	if !strings.Contains(capturedAskPrompt, "[REDACTED_API_KEY]") || !strings.Contains(capturedAskPrompt, "[REDACTED_TOKEN]") || !strings.Contains(capturedAskPrompt, "[REDACTED_AWS_KEY]") {
		t.Fatalf("Ask prompt missing expected redacted placeholders:\n%s", capturedAskPrompt)
	}

	// Test Converse redaction
	_, err = eng.Converse(ctx, sess.ID, "antigravity", protocol.ConverseRequest{
		Peer:    "codex",
		Message: "Here is another token: Bearer my-secret-bearer-token-1234567890",
	}, 0, "conv_dlp_1")
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(capturedConversePrompt, "my-secret-bearer-token-1234567890") {
		t.Fatalf("Converse prompt contained unredacted bearer token:\n%s", capturedConversePrompt)
	}
	if !strings.Contains(capturedConversePrompt, "[REDACTED_BEARER_TOKEN]") {
		t.Fatalf("Converse prompt missing expected bearer redaction placeholder:\n%s", capturedConversePrompt)
	}
}

func TestEngine_SpaceTriggered_BudgetEnforcement(t *testing.T) {
	var invokeCount int64
	mockPeer := &mockHarness{
		name: "codex",
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			atomic.AddInt64(&invokeCount, 1)
			return agent.InvokeResult{
				Text: "Review ok",
				Usage: map[string]any{
					"input_tokens":  int64(200),
					"output_tokens": int64(200),
					"cost_usd":      0.01,
				},
			}, nil
		},
	}

	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": mockPeer})
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_budget_enforced"
	sp := &protocol.CollaborationSpace{
		ID:             spaceID,
		WorkspaceID:    ".",
		Title:          "Budgeted Space",
		LifecycleState: protocol.SpaceStateActive,
		Budget: protocol.BudgetStatus{
			Known:          true,
			MaxTotalTokens: 500, // 1st invocation uses 400, 2nd cannot reserve 400
		},
		Participants: map[string]protocol.SpaceParticipant{
			"user":  {ID: "user", Adapter: "user", Mode: protocol.ParticipantModeActive, Writable: true},
			"codex": {ID: "codex", Adapter: "codex", Mode: protocol.ParticipantModeActive},
		},
		Channels: map[string]protocol.Channel{
			"general": {
				ID:         "general",
				SpaceID:    spaceID,
				Name:       "general",
				Visibility: protocol.ChannelVisibilityAll,
			},
		},
	}
	if err := eng.Store().SaveSpace(ctx, sp); err != nil {
		t.Fatal(err)
	}

	// 1st publish triggers codex and consumes 400 tokens
	_, err := eng.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "general",
		From:      "user",
		Subject:   "Task 1",
		Message:   "Please check @codex",
		Mentions:  []string{"codex"},
	})
	if err != nil {
		t.Fatalf("first publish failed: %v", err)
	}
	if atomic.LoadInt64(&invokeCount) != 1 {
		t.Fatalf("expected 1 invocation, got %d", atomic.LoadInt64(&invokeCount))
	}

	// Verify budget usage was recorded in space
	spAfter, err := eng.Store().GetSpace(ctx, spaceID)
	if err != nil {
		t.Fatal(err)
	}
	if spAfter.Budget.UsedTotalTokens != 400 {
		t.Fatalf("expected 400 used tokens in space budget, got %d", spAfter.Budget.UsedTotalTokens)
	}

	// 2nd publish should be skipped due to budget exceeded
	pubResp2, err := eng.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "general",
		From:      "user",
		Subject:   "Task 2",
		Message:   "Another request @codex",
		Mentions:  []string{"codex"},
	})
	if err != nil {
		t.Fatalf("second publish returned unexpected error: %v", err)
	}
	// Invocation count should remain 1 because delivery was skipped due to budget
	if atomic.LoadInt64(&invokeCount) != 1 {
		t.Fatalf("expected still 1 invocation after budget exceeded, got %d", atomic.LoadInt64(&invokeCount))
	}
	if pubResp2 == nil {
		t.Fatal("expected pubResp2 not nil")
	}
}

func TestEngine_Converse_DLP_FailClosedOnContextRejection(t *testing.T) {
	mockPeer := &mockHarness{
		name: "codex",
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			return agent.InvokeResult{
				Text: "Should not be called",
			}, nil
		},
	}
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": mockPeer})
	defer st.Close()

	ctx := context.Background()
	sess, err := eng.CreateSession(ctx, "sess_dlp_failclose", "DLP security test")
	if err != nil {
		t.Fatal(err)
	}

	// Calling Converse with a denied file (.env) must fail closed with ContextRejectedError
	_, err = eng.Converse(ctx, sess.ID, "antigravity", protocol.ConverseRequest{
		Peer:    "codex",
		Message: "Inspect this secret file",
		Scope:   []string{".env"},
	}, 0, "conv_reject_1")
	if err == nil {
		t.Fatal("expected ContextRejectedError, got nil")
	}

	var rejErr *protocol.ContextRejectedError
	if !errors.As(err, &rejErr) {
		t.Fatalf("expected ContextRejectedError, got %T: %v", err, err)
	}
}

func TestEngine_EventBus_UnknownChannel_NotDelivered(t *testing.T) {
	var invokeCount int64
	mockPeer := &mockHarness{
		name: "codex",
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			atomic.AddInt64(&invokeCount, 1)
			return agent.InvokeResult{Text: "ok"}, nil
		},
	}
	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": mockPeer})
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_unknown_ch"
	sp := &protocol.CollaborationSpace{
		ID:             spaceID,
		WorkspaceID:    ".",
		Title:          "Space",
		LifecycleState: protocol.SpaceStateActive,
		Participants: map[string]protocol.SpaceParticipant{
			"codex": {ID: "codex", Adapter: "codex", Mode: protocol.ParticipantModeActive},
		},
	}
	_ = eng.Store().SaveSpace(ctx, sp)

	sub := &protocol.Subscription{
		ID:            "sub_all",
		SpaceID:       spaceID,
		ParticipantID: "codex",
		Channels:      []string{"*"},
		EventTypes:    []string{protocol.EventRepoChanged},
	}
	_ = eng.Store().SaveSubscription(ctx, sub)

	// Event with nonexistent channel_id must NOT be delivered to wildcard subscriber
	evt := &protocol.CollaborationEvent{
		ID:        "evt_bad_ch",
		SpaceID:   spaceID,
		Type:      protocol.EventRepoChanged,
		Source:    "user",
		Timestamp: time.Now().UTC(),
		Payload: map[string]any{
			"channel_id": "nonexistent_channel_12345",
		},
	}
	eng.handleCollaborationEvent(ctx, evt)

	if atomic.LoadInt64(&invokeCount) != 0 {
		t.Fatalf("expected 0 invocations for unknown channel, got %d", atomic.LoadInt64(&invokeCount))
	}
}

func TestEngine_BudgetCommitExceeded_NoAccountingCorruption(t *testing.T) {
	mockPeer := &mockHarness{
		name: "codex",
		caps: config.AgentCapabilities{AnswerQuestions: true, Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			// Returns actual usage that exceeds max budget limit (500)
			return agent.InvokeResult{
				Text: "Over budget response",
				Usage: map[string]any{
					"input_tokens":  int64(300),
					"output_tokens": int64(350), // total = 650 > 500
					"cost_usd":      0.02,
				},
			}, nil
		},
	}

	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": mockPeer})
	defer st.Close()

	ctx := context.Background()
	sessionID := "sess_budget_commit"
	sess := &store.Session{
		ID:        sessionID,
		Status:    "active",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Budget: protocol.BudgetStatus{
			Known:          true,
			MaxTotalTokens: 500, // Limit is 500. Initial estimate is ~200, but actual is 650.
		},
	}
	if err := eng.Store().SaveSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	// Ask triggers codex. Reservation estimate is ~200, which passes.
	// But commit actual is 650, which exceeds 500.
	_, err := eng.Ask(ctx, sessionID, "antigravity", protocol.AskRequest{
		Peer:     "codex",
		Question: "Check this function",
	}, 0, "")
	if err == nil {
		t.Fatal("expected Ask to return error on budget exceeded commit, got nil")
	}

	// Verify session budget in store:
	// The actual usage (650) must be recorded, and the deferred rollback must NOT have corrupted it (e.g. by subtracting estimate)
	sessAfter, err := eng.Store().GetSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sessAfter.Budget.UsedTotalTokens != 650 {
		t.Fatalf("expected exactly 650 used tokens recorded, got %d (corrupted by rollback?)", sessAfter.Budget.UsedTotalTokens)
	}
	if sessAfter.Budget.UsedInputTokens != 300 {
		t.Fatalf("expected 300 input tokens, got %d", sessAfter.Budget.UsedInputTokens)
	}
	if sessAfter.Budget.UsedOutputTokens != 350 {
		t.Fatalf("expected 350 output tokens, got %d", sessAfter.Budget.UsedOutputTokens)
	}

	// Subsequent calls must be blocked at reservation
	_, err = eng.Ask(ctx, sessionID, "antigravity", protocol.AskRequest{
		Peer:     "codex",
		Question: "Another",
	}, 0, "")
	var bErr *protocol.BudgetExceededError
	if !errors.As(err, &bErr) {
		t.Fatalf("expected BudgetExceededError on subsequent call, got %v", err)
	}
}

func TestEngine_Publish_CommitBudgetExceeded_SkipsDelivery(t *testing.T) {
	mockPeer := &mockHarness{
		name: "codex",
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			// Returns over-budget usage and a review finding
			return agent.InvokeResult{
				Text: `{"verdict":"CHANGES_REQUESTED","findings":[{"id":"f_leak","title":"Leak","severity":"HIGH","status":"OPEN"}]}`,
				Usage: map[string]any{
					"input_tokens":  int64(400),
					"output_tokens": int64(400), // total = 800 > 500
					"cost_usd":      0.05,
				},
			}, nil
		},
	}

	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": mockPeer})
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_commit_budget"
	sp := &protocol.CollaborationSpace{
		ID:             spaceID,
		WorkspaceID:    ".",
		Title:          "Publish Budget Commit Test",
		LifecycleState: protocol.SpaceStateActive,
		Budget: protocol.BudgetStatus{
			Known:          true,
			MaxTotalTokens: 500, // Limit is 500, invocation consumes 800
		},
		Participants: map[string]protocol.SpaceParticipant{
			"user":  {ID: "user", Adapter: "user", Mode: protocol.ParticipantModeActive, Writable: true},
			"codex": {ID: "codex", Adapter: "codex", Mode: protocol.ParticipantModeActive},
		},
		Channels: map[string]protocol.Channel{
			"general": {
				ID:         "general",
				SpaceID:    spaceID,
				Name:       "general",
				Visibility: protocol.ChannelVisibilityAll,
			},
			"findings": {
				ID:         "findings",
				SpaceID:    spaceID,
				Name:       "findings",
				Visibility: protocol.ChannelVisibilityAll,
			},
		},
	}
	if err := eng.Store().SaveSpace(ctx, sp); err != nil {
		t.Fatal(err)
	}

	pubResp, err := eng.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "general",
		From:      "user",
		Subject:   "Review request",
		Message:   "Please check @codex",
		Mentions:  []string{"codex"},
	})
	if err != nil {
		t.Fatalf("Publish returned error: %v", err)
	}

	// Invocations exceeded budget on commit:
	// 1. Space budget should reflect actuals (800)
	spAfter, err := eng.Store().GetSpace(ctx, spaceID)
	if err != nil {
		t.Fatal(err)
	}
	if spAfter.Budget.UsedTotalTokens != 800 {
		t.Fatalf("expected 800 tokens recorded, got %d", spAfter.Budget.UsedTotalTokens)
	}

	// 2. Findings channel must NOT contain the response message because delivery was skipped
	msgs, err := eng.Store().GetMessages(ctx, spaceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.ChannelID == "findings" {
			t.Fatalf("unexpected message in findings channel when commit budget exceeded: %+v", m)
		}
	}

	// 3. Finding f_leak must NOT have been saved
	f, err := eng.Store().GetFinding(ctx, spaceID, "f_leak")
	if err == nil && f != nil {
		t.Fatalf("finding f_leak should not have been saved when commit budget exceeded: %+v", f)
	}

	// 4. Audit fields check: result_message_id must be empty, skip_reason must be "budget_exceeded"
	del, err := eng.Store().GetEventDelivery(ctx, pubResp.MessageID, "codex")
	if err != nil || del == nil {
		t.Fatalf("expected event delivery record for codex, got %v, err: %v", del, err)
	}
	if del.Status != protocol.DeliverySkipped {
		t.Fatalf("expected DeliverySkipped, got %s", del.Status)
	}
	if del.SkipReason != "budget_exceeded" {
		t.Fatalf("expected skipReason 'budget_exceeded', got %q", del.SkipReason)
	}
	if del.ResultMessageID != "" {
		t.Fatalf("expected empty ResultMessageID on skipped delivery, got %q", del.ResultMessageID)
	}
}

func TestEngine_HardBudget_RejectsBeforeInvocationWhenRemainingInsufficient(t *testing.T) {
	var invokeCount int64
	mockPeer := &mockHarness{
		name: "codex",
		caps: config.AgentCapabilities{AnswerQuestions: true, Review: true},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			atomic.AddInt64(&invokeCount, 1)
			return agent.InvokeResult{
				Text: "Should not be called if remaining budget cannot cover invocation",
				Usage: map[string]any{
					"input_tokens":  int64(50),
					"output_tokens": int64(50),
				},
			}, nil
		},
	}

	eng, st, _ := setupTestEngine(t, map[string]agent.Harness{"codex": mockPeer})
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_hard_ceiling"
	sp := &protocol.CollaborationSpace{
		ID:             spaceID,
		WorkspaceID:    ".",
		Title:          "Hard Ceiling Space",
		LifecycleState: protocol.SpaceStateActive,
		Budget: protocol.BudgetStatus{
			Known:           true,
			MaxTotalTokens:  200, // Very tight hard ceiling: 200 tokens total
			UsedTotalTokens: 160, // 160 already used, remaining is only 40 (< minViableOutput 100)
		},
		Participants: map[string]protocol.SpaceParticipant{
			"user":  {ID: "user", Adapter: "user", Mode: protocol.ParticipantModeActive, Writable: true},
			"codex": {ID: "codex", Adapter: "codex", Mode: protocol.ParticipantModeActive},
		},
		Channels: map[string]protocol.Channel{
			"general": {
				ID:         "general",
				SpaceID:    spaceID,
				Name:       "general",
				Visibility: protocol.ChannelVisibilityAll,
			},
		},
	}
	if err := eng.Store().SaveSpace(ctx, sp); err != nil {
		t.Fatal(err)
	}

	// Publish should detect that remaining budget (40) cannot safely cover minimum output,
	// and reject BEFORE invoking the harness, preventing any token or monetary spend.
	pubResp, err := eng.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "general",
		From:      "user",
		Subject:   "Task",
		Message:   "Please check @codex",
		Mentions:  []string{"codex"},
	})
	if err != nil {
		t.Fatalf("Publish returned unexpected error: %v", err)
	}

	// Invocations must remain 0 - no model was called, no spend occurred!
	if atomic.LoadInt64(&invokeCount) != 0 {
		t.Fatalf("expected 0 invocations due to hard ceiling check, got %d", atomic.LoadInt64(&invokeCount))
	}

	// Verify delivery record was recorded as skipped with budget_exceeded and empty resultMsgID
	del, err := eng.Store().GetEventDelivery(ctx, pubResp.MessageID, "codex")
	if err != nil || del == nil {
		t.Fatalf("expected delivery record for codex, got %v, err: %v", del, err)
	}
	if del.Status != protocol.DeliverySkipped {
		t.Fatalf("expected status DeliverySkipped, got %s", del.Status)
	}
	if del.SkipReason != "budget_exceeded" {
		t.Fatalf("expected skipReason 'budget_exceeded', got %q", del.SkipReason)
	}
	if del.ResultMessageID != "" {
		t.Fatalf("expected empty ResultMessageID, got %q", del.ResultMessageID)
	}
}

func TestEngine_CommitBudget_FailClosed(t *testing.T) {
	eng, st, _ := setupTestEngine(t, nil)
	defer st.Close()
	ctx := context.Background()

	// Commit on nonexistent session must fail closed with error, not return nil
	res := &budgetReservation{
		sessionID: "nonexistent_session_id",
		estInput:  50,
		estOutput: 50,
	}
	err := eng.commitBudget(ctx, res, 50, 50, 0.01)
	if err == nil {
		t.Fatal("expected error on nonexistent session budget commit, got nil")
	}
	// Reservation must NOT have been discharged on failure
	if res.sessionID == "" {
		t.Fatal("reservation should not be discharged when commitBudget fails")
	}
}

func TestEngine_RollbackBudget_SuccessAndFailure(t *testing.T) {
	eng, st, _ := setupTestEngine(t, nil)
	defer st.Close()
	ctx := context.Background()

	sess, err := eng.CreateSession(ctx, "rollback_test", "task")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Success case
	sess.Budget = protocol.BudgetStatus{
		Known:          true,
		MaxTotalTokens: 10000,
		MaxCostUSD:     10.0,
	}
	if err := eng.Store().SaveSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	res, err := eng.reserveBudget(ctx, sess.ID, 200, 300)
	if err != nil {
		t.Fatalf("reserveBudget failed: %v", err)
	}
	if res.maxCostUSD <= 0 {
		t.Fatalf("expected positive maxCostUSD, got %f", res.maxCostUSD)
	}

	// Verify reservation took effect in store
	s1, err := eng.Store().GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Budget.UsedTotalTokens != 500 {
		t.Fatalf("expected 500 UsedTotalTokens, got %d", s1.Budget.UsedTotalTokens)
	}

	// Rollback
	if err := eng.rollbackBudget(ctx, res); err != nil {
		t.Fatalf("rollbackBudget failed: %v", err)
	}
	if res.sessionID != "" {
		t.Fatal("expected res.sessionID to be discharged after successful rollback")
	}

	s2, err := eng.Store().GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Budget.UsedTotalTokens != 0 {
		t.Fatalf("expected 0 UsedTotalTokens after rollback, got %d", s2.Budget.UsedTotalTokens)
	}

	// 2. Failure case with injected UpdateBudget error
	fStore := &faultyBudgetStore{Store: st, failUpdateBudget: false}
	eng.store = fStore

	res2, err := eng.reserveBudget(ctx, sess.ID, 150, 250)
	if err != nil {
		t.Fatalf("reserveBudget failed: %v", err)
	}
	// Enable failure on UpdateBudget
	fStore.failUpdateBudget = true

	rbErr := eng.rollbackBudget(ctx, res2)
	if rbErr == nil {
		t.Fatal("expected error on rollbackBudget when UpdateBudget fails, got nil")
	}
	if !strings.Contains(rbErr.Error(), "injected database disk I/O failure") {
		t.Fatalf("expected injected error message, got %v", rbErr)
	}
	// Reservation state must NOT be discharged so caller knows rollback failed
	if res2.sessionID == "" {
		t.Fatal("reservation sessionID should not be cleared on rollback failure")
	}

	// Verify budget.rollback_failed event was emitted to the store
	evts, err := st.GetEvents(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundRollbackFailed := false
	for _, e := range evts {
		if e.EventType == "budget.rollback_failed" {
			foundRollbackFailed = true
			break
		}
	}
	if !foundRollbackFailed {
		t.Fatal("expected budget.rollback_failed event to be recorded in store")
	}

	// 3. Operational caller propagation: Ask must propagate rollback failure
	fStore.failUpdateBudget = false
	mockPeer := &mockHarness{
		name: "failing-peer",
		caps: config.AgentCapabilities{
			AnswerQuestions:        true,
			HardTokenLimitEnforced: true,
		},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			// Trigger store failure right after model failure, when rollbackBudget executes
			fStore.failUpdateBudget = true
			return agent.InvokeResult{}, errors.New("model execution timed out")
		},
	}
	if eng.harnesses == nil {
		eng.harnesses = make(map[string]agent.Harness)
	}
	eng.harnesses["failing-peer"] = mockPeer

	_, askErr := eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{
		Peer:     "failing-peer",
		Question: "test rollback propagation",
	}, 0, "")
	if askErr == nil {
		t.Fatal("expected askErr, got nil")
	}
	if !strings.Contains(askErr.Error(), "model execution timed out") || !strings.Contains(askErr.Error(), "rollback error") {
		t.Fatalf("expected askErr to include both invocation and rollback errors, got: %v", askErr)
	}
	// 4. Fail-closed rollback lookups
	resNonExistent := &budgetReservation{
		sessionID: "definitely-does-not-exist-session-id",
		estInput:  100,
		estOutput: 100,
	}
	lookupErr := eng.rollbackBudget(ctx, resNonExistent)
	if lookupErr == nil {
		t.Fatal("expected rollbackBudget to fail closed on nonexistent session lookup, got nil")
	}
	if resNonExistent.sessionID == "" {
		t.Fatal("expected resNonExistent.sessionID to NOT be discharged on lookup failure")
	}
}

type faultyBudgetStore struct {
	store.Store
	failUpdateBudget bool
}

func (s *faultyBudgetStore) UpdateBudget(ctx context.Context, id string, budget protocol.BudgetStatus) error {
	if s.failUpdateBudget {
		return errors.New("injected database disk I/O failure")
	}
	return s.Store.UpdateBudget(ctx, id, budget)
}

func TestEngine_StrictTokenCeiling_RejectsNonEnforcingAdapters(t *testing.T) {
	ctx := context.Background()

	var codexInvoked int
	var fakePassedMaxTokens int64

	codexMock := &mockHarness{
		name: "codex-cli",
		caps: config.AgentCapabilities{
			AnswerQuestions:        true,
			HardTokenLimitEnforced: false, // CLI adapter cannot guarantee token ceiling
		},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			codexInvoked++
			return agent.InvokeResult{AgentName: "codex-cli", Text: "codex response"}, nil
		},
	}

	fakeMock := &mockHarness{
		name: "fake-enforcer",
		caps: config.AgentCapabilities{
			AnswerQuestions:        true,
			HardTokenLimitEnforced: true, // Guarantees strict token bounds
		},
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			fakePassedMaxTokens = req.MaxTokens
			return agent.InvokeResult{AgentName: "fake-enforcer", Text: "fake response"}, nil
		},
	}

	harnesses := map[string]agent.Harness{
		"codex-cli":     codexMock,
		"fake-enforcer": fakeMock,
	}

	eng, st, _ := setupTestEngine(t, harnesses)
	defer st.Close()

	// 1. Configure engine collaboration config with StrictTokenCeiling and MaxOutputTokens
	eng.cfg.Collaboration.StrictTokenCeiling = true
	eng.cfg.Collaboration.MaxOutputTokens = 500
	eng.spaceService.cfg = *eng.cfg

	// Test production creation paths: CreateSession and CreateSpace must copy StrictTokenCeiling
	sess, err := eng.CreateSession(ctx, "strict_token_test", "Strict ceiling test")
	if err != nil {
		t.Fatal(err)
	}
	if !sess.Budget.StrictTokenCeiling || sess.Budget.MaxOutputTokens != 500 {
		t.Fatalf("expected CreateSession to inherit StrictTokenCeiling=true and MaxOutputTokens=500, got: %+v", sess.Budget)
	}

	space, err := eng.SpaceService().CreateSpace(ctx, "space_strict", "/tmp/repo", "Strict Space", "Purpose", "codex-cli", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !space.Budget.StrictTokenCeiling || space.Budget.MaxOutputTokens != 500 {
		t.Fatalf("expected CreateSpace to inherit StrictTokenCeiling=true and MaxOutputTokens=500, got: %+v", space.Budget)
	}

	// Attempting to invoke codex-cli under strict token ceiling must be rejected
	_, err = eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{
		Peer:     "codex-cli",
		Question: "can you enforce 500 tokens?",
	}, 0, "")
	if err == nil {
		t.Fatal("expected error when invoking non-enforcing adapter under strict token ceiling, got nil")
	}
	var unsuppErr *protocol.HardTokenLimitUnsupportedError
	if !errors.As(err, &unsuppErr) {
		t.Fatalf("expected HardTokenLimitUnsupportedError, got %T: %v", err, err)
	}
	if codexInvoked != 0 {
		t.Fatalf("expected 0 invocations of non-enforcing codex adapter, got %d", codexInvoked)
	}

	// Invoking fake-enforcer under strict ceiling must succeed and pass MaxTokens=500
	ans, err := eng.Ask(ctx, sess.ID, "claude", protocol.AskRequest{
		Peer:     "fake-enforcer",
		Question: "enforce 500 tokens please",
	}, 0, "")
	if err != nil {
		t.Fatalf("unexpected error invoking enforcing adapter: %v", err)
	}
	if ans.Answer != "fake response" {
		t.Fatalf("unexpected answer: %s", ans.Answer)
	}
	if fakePassedMaxTokens != 500 {
		t.Fatalf("expected fake-enforcer to receive MaxTokens=500, got %d", fakePassedMaxTokens)
	}

	// 2. Soft limits mode (StrictTokenCeiling = false)
	eng.cfg.Collaboration.StrictTokenCeiling = false
	eng.cfg.Collaboration.MaxOutputTokens = 500

	sessSoft, err := eng.CreateSession(ctx, "soft_token_test", "Soft ceiling test")
	if err != nil {
		t.Fatal(err)
	}
	if sessSoft.Budget.StrictTokenCeiling {
		t.Fatal("expected soft session to have StrictTokenCeiling=false")
	}

	// Under soft limits, invocation to codex-cli succeeds, but MaxTokens passed to non-enforcing CLI is 0
	var codexReqMaxTokens int64 = -1
	codexMock.invokeFunc = func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
		codexReqMaxTokens = req.MaxTokens
		return agent.InvokeResult{AgentName: "codex-cli", Text: "soft limit codex answer"}, nil
	}

	ansSoft, err := eng.Ask(ctx, sessSoft.ID, "claude", protocol.AskRequest{
		Peer:     "codex-cli",
		Question: "soft limits test",
	}, 0, "")
	if err != nil {
		t.Fatalf("expected soft limits invocation to succeed, got: %v", err)
	}
	if ansSoft.Answer != "soft limit codex answer" {
		t.Fatalf("unexpected answer: %s", ansSoft.Answer)
	}
	if codexReqMaxTokens != 0 {
		t.Fatalf("expected non-enforcing adapter under soft limits to receive MaxTokens=0, got %d", codexReqMaxTokens)
	}
}

func TestEngine_StrictTokenCeiling_EventDispatcherRollbackErrorCapture(t *testing.T) {
	ctx := context.Background()

	codexMock := &mockHarness{
		name: "codex-cli",
		caps: config.AgentCapabilities{
			AnswerQuestions:        true,
			HardTokenLimitEnforced: false,
		},
	}

	harnesses := map[string]agent.Harness{
		"codex-cli": codexMock,
	}

	eng, st, _ := setupTestEngine(t, harnesses)
	defer st.Close()

	eng.cfg.Collaboration.StrictTokenCeiling = true
	eng.cfg.Collaboration.MaxOutputTokens = 500
	eng.spaceService.cfg = *eng.cfg

	participants := map[string]protocol.SpaceParticipant{
		"antigravity": {ID: "antigravity", Roles: []string{"lead"}, Mode: protocol.ParticipantModeActive, Writable: true},
		"codex-cli":   {ID: "codex-cli", Roles: []string{"reviewer"}, Mode: protocol.ParticipantModeActive, Adapter: "codex-cli"},
	}

	space, err := eng.SpaceService().CreateSpace(ctx, "space_dispatcher_test", "/tmp/repo", "Test", "Test", "antigravity", participants)
	if err != nil {
		t.Fatal(err)
	}

	// Wrap store with faulty store to trigger rollback failure on UpdateBudget
	fStore := &faultyBudgetStore{Store: st, failUpdateBudget: false}
	eng.store = fStore

	// Intercept and fail UpdateBudget only during rollback
	var updateBudgetCount int
	fStore.Store = &faultyCallbackStore{
		Store: st,
		onUpdateBudget: func() error {
			updateBudgetCount++
			if updateBudgetCount > 1 { // 1st call is reserveBudget; 2nd call is rollbackBudget
				return errors.New("injected rollback store failure")
			}
			return nil
		},
	}

	pubResp, err := eng.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   space.ID,
		ChannelID: "general",
		From:      "antigravity",
		Message:   "check strict token ceiling rollback capture",
		Mentions:  []string{"codex-cli"},
	})
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// Verify delivery record was recorded as skipped with skip reason containing both strict limit and rollback error
	del, err := eng.Store().GetEventDelivery(ctx, pubResp.MessageID, "codex-cli")
	if err != nil || del == nil {
		t.Fatalf("expected delivery record for codex-cli, got %v, err: %v", del, err)
	}
	if del.Status != protocol.DeliverySkipped {
		t.Fatalf("expected DeliverySkipped, got %s", del.Status)
	}
	if !strings.Contains(del.SkipReason, "hard_token_limit_unsupported") || !strings.Contains(del.SkipReason, "rollback error") {
		t.Fatalf("expected SkipReason to capture both strict rejection and rollback error, got: %q", del.SkipReason)
	}
}

type faultyCallbackStore struct {
	store.Store
	onUpdateBudget func() error
}

func (s *faultyCallbackStore) UpdateBudget(ctx context.Context, id string, budget protocol.BudgetStatus) error {
	if s.onUpdateBudget != nil {
		if err := s.onUpdateBudget(); err != nil {
			return err
		}
	}
	return s.Store.UpdateBudget(ctx, id, budget)
}
