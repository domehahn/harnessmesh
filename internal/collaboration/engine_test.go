package collaboration

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

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
