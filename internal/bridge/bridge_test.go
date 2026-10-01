package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

func setupTestBridge(t *testing.T) (*Server, *collaboration.Engine, string) {
	t.Helper()
	tmpDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "bridge.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{
		Version: 2,
		Agents: map[string]config.AgentConfig{
			"claude-executor": {Role: "executor", ExecutionMode: "managed", Writable: true},
		},
	}
	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Projector: contextpack.New(tmpDir, cfg.Context),
	})
	t.Cleanup(eng.Close)

	sess, err := eng.CreateSession(context.Background(), "sess_bridge", "bridge test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	space, err := eng.SpaceService().CreateSpace(context.Background(), "space_bridge", tmpDir, "Bridge Space", "test", "claude-executor", nil)
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	_ = sess

	token := "bridge-test-token"
	s := NewServer(eng, Config{Token: token, Caller: "claude-executor", WebSocketEnabled: true})
	t.Cleanup(s.Close)
	return s, eng, space.ID
}

func doReq(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestBridge_RequiresBearerToken(t *testing.T) {
	s, _, _ := setupTestBridge(t)
	h := s.Handler()

	rec := doReq(t, h, http.MethodGet, "/api/v1/workspaces", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no token, got %d", rec.Code)
	}
	rec = doReq(t, h, http.MethodGet, "/api/v1/workspaces", "wrong-token", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", rec.Code)
	}
	rec = doReq(t, h, http.MethodGet, "/api/v1/workspaces", "bridge-test-token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct token, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBridge_ServeRefusesUnauthenticated(t *testing.T) {
	s, _, _ := setupTestBridge(t)
	s.cfg.Token = ""
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Serve(ctx); err == nil {
		t.Fatal("expected Serve to refuse an unconfigured (empty-token) bridge")
	}
}

func TestBridge_CORSDeniesUnknownOrigin(t *testing.T) {
	s, _, _ := setupTestBridge(t)
	s.allowedOrigins = []string{"vscode-webview://trusted"}
	h := s.Handler()

	r := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
	r.Header.Set("Authorization", "Bearer bridge-test-token")
	r.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed origin, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "*" {
		t.Fatalf("must never set Access-Control-Allow-Origin: * on an authenticated endpoint")
	}
}

func TestBridge_RateLimitBounded(t *testing.T) {
	s, _, _ := setupTestBridge(t)
	s.cfg.RateLimit = 3
	h := s.Handler()

	var lastCode int
	for i := 0; i < 5; i++ {
		lastCode = doReq(t, h, http.MethodGet, "/api/v1/workspaces", "bridge-test-token", nil).Code
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exceeding rate limit, got %d", lastCode)
	}
}

func TestBridge_TaskLifecycle(t *testing.T) {
	s, _, spaceID := setupTestBridge(t)
	h := s.Handler()

	rec := doReq(t, h, http.MethodPost, "/api/v1/tasks", "bridge-test-token", map[string]any{
		"space_id": spaceID,
		"title":    "Inspect auth middleware",
		"intent":   "security review",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating task, got %d: %s", rec.Code, rec.Body.String())
	}
	var created protocol.MeshChange
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created task: %v", err)
	}
	if created.ID == "" || created.AuthorParticipant != "claude-executor" {
		t.Fatalf("unexpected created task: %+v", created)
	}

	rec = doReq(t, h, http.MethodGet, "/api/v1/tasks/"+created.ID, "bridge-test-token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 getting task, got %d", rec.Code)
	}

	rec = doReq(t, h, http.MethodPatch, "/api/v1/tasks/"+created.ID, "bridge-test-token", map[string]any{"action": "abort", "reason": "no longer needed"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 aborting task, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBridge_IdempotentTaskCreate(t *testing.T) {
	s, _, spaceID := setupTestBridge(t)
	h := s.Handler()

	body := map[string]any{"space_id": spaceID, "title": "Idempotent task", "intent": "test"}
	r1 := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewReader(mustJSON(t, body)))
	r1.Header.Set("Authorization", "Bearer bridge-test-token")
	r1.Header.Set("Content-Type", "application/json")
	r1.Header.Set("Idempotency-Key", "idem-1")
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, r1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("expected 201 on first create, got %d: %s", rec1.Code, rec1.Body.String())
	}

	r2 := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewReader(mustJSON(t, body)))
	r2.Header.Set("Authorization", "Bearer bridge-test-token")
	r2.Header.Set("Content-Type", "application/json")
	r2.Header.Set("Idempotency-Key", "idem-1")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, r2)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("expected replayed 201 on retry, got %d", rec2.Code)
	}
	if rec2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("expected Idempotency-Replayed header on retry")
	}
	if rec1.Body.String() != rec2.Body.String() {
		t.Fatalf("expected identical body on idempotent replay:\n%s\nvs\n%s", rec1.Body.String(), rec2.Body.String())
	}

	tasks := doReq(t, h, http.MethodGet, "/api/v1/tasks?space_id="+spaceID, "bridge-test-token", nil)
	var listed struct {
		Tasks []protocol.MeshChange `json:"tasks"`
	}
	if err := json.Unmarshal(tasks.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	if len(listed.Tasks) != 1 {
		t.Fatalf("expected exactly 1 task after idempotent retry, got %d", len(listed.Tasks))
	}
}

func TestBridge_IdempotentTaskCreate_ConcurrentRetriesProduceOneRecord(t *testing.T) {
	s, _, spaceID := setupTestBridge(t)
	h := s.Handler()

	body := mustJSON(t, map[string]any{"space_id": spaceID, "title": "Concurrent idempotent task", "intent": "test"})

	const n = 20
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer bridge-test-token")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", "concurrent-idem-1")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	for _, c := range codes {
		if c != http.StatusCreated {
			t.Fatalf("expected all concurrent idempotent requests to return 201, got %d", c)
		}
	}

	tasks := doReq(t, h, http.MethodGet, "/api/v1/tasks?space_id="+spaceID, "bridge-test-token", nil)
	var listed struct {
		Tasks []protocol.MeshChange `json:"tasks"`
	}
	if err := json.Unmarshal(tasks.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	if len(listed.Tasks) != 1 {
		t.Fatalf("expected exactly 1 task after %d concurrent identical idempotent requests, got %d", n, len(listed.Tasks))
	}
}

func TestBridge_MessagesAndFindings(t *testing.T) {
	s, _, spaceID := setupTestBridge(t)
	h := s.Handler()

	rec := doReq(t, h, http.MethodPost, "/api/v1/messages", "bridge-test-token", map[string]any{
		"space_id":   spaceID,
		"channel_id": "general",
		"message":    "hello from the bridge",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 posting message, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doReq(t, h, http.MethodPost, "/api/v1/findings", "bridge-test-token", map[string]any{
		"session_id": "sess_bridge",
		"id":         "finding_bridge_1",
		"severity":   "low",
		"category":   "style",
		"claim":      "minor style nit",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 posting finding, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doReq(t, h, http.MethodGet, "/api/v1/findings?session_id=sess_bridge", "bridge-test-token", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "finding_bridge_1") {
		t.Fatalf("expected finding to be listed, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBridge_IdentitySpoofingRejected(t *testing.T) {
	s, _, spaceID := setupTestBridge(t)
	h := s.Handler()

	rec := doReq(t, h, http.MethodPost, "/api/v1/tasks", "bridge-test-token", map[string]any{
		"space_id":           spaceID,
		"title":              "spoofed",
		"author_participant": "someone-else",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for spoofed author, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBridge_WebSocketBroadcastAndReplay(t *testing.T) {
	s, eng, spaceID := setupTestBridge(t)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/events/ws?token=bridge-test-token&space_id=" + spaceID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer conn.Close()

	// give the read pump a moment to register before we publish
	time.Sleep(20 * time.Millisecond)

	if _, err := eng.Publish(context.Background(), &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "general",
		From:      "claude-executor",
		Message:   "ws broadcast test",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	found := false
	for i := 0; i < 5 && !found; i++ {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read ws message: %v", err)
		}
		var env Event
		if err := json.Unmarshal(msg, &env); err != nil {
			t.Fatalf("decode event envelope: %v", err)
		}
		if env.Protocol != BridgeProtocolVersion {
			t.Fatalf("expected protocol %q, got %q", BridgeProtocolVersion, env.Protocol)
		}
		if env.Type == protocol.EventMessageCreated && env.WorkspaceID == spaceID {
			found = true
		}
	}
	if !found {
		t.Fatal("expected to receive the published message event over the websocket")
	}
}

func TestBridge_WebSocketSlowConsumerBounded(t *testing.T) {
	s, eng, spaceID := setupTestBridge(t)

	c := &wsClient{send: make(chan []byte, 2), spaceID: spaceID, hub: s.hub}
	s.hub.register(c)

	for i := 0; i < 50; i++ {
		if err := eng.Store().EmitEvent(context.Background(), spaceID, "message.created", map[string]any{"i": i}); err != nil {
			t.Fatalf("EmitEvent: %v", err)
		}
		s.bridgeEventListener(context.Background(), &protocol.CollaborationEvent{
			ID: "evt_slow_" + string(rune('a'+i%26)), SpaceID: spaceID, Type: "message.created", Timestamp: time.Now(),
		})
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.hub.mu.RLock()
		_, stillRegistered := s.hub.clients[c]
		s.hub.mu.RUnlock()
		if !stillRegistered {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.hub.mu.RLock()
	_, stillRegistered := s.hub.clients[c]
	s.hub.mu.RUnlock()
	if stillRegistered {
		t.Fatal("expected slow consumer with a full queue to be disconnected, but it is still registered")
	}
	if s.hub.droppedSlowClients.Load() == 0 {
		t.Fatal("expected droppedSlowClients metric to be incremented")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
