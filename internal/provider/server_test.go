package provider

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
)

func testProviderConfig(token string) config.ProviderGatewayConfig {
	return config.ProviderGatewayConfig{Enabled: true, Token: token, DefaultBackend: "local"}
}

// newTestRegistry builds a Registry directly from already-constructed
// fakeInferenceBackends (see registry_test.go's newRegistryForTest),
// bypassing NewRegistry's config.ProviderBackendConfig-driven construction.
func newTestRegistry(t *testing.T, backends map[string]InferenceBackend, policy Policy) *Registry {
	t.Helper()
	return newRegistryForTest(backends, policy)
}

func doProviderReq(t *testing.T, h http.Handler, token string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestServer_RequiresAuth(t *testing.T) {
	backend := &fakeInferenceBackend{name: "local", typ: "openai-compatible", events: simpleTextEvents("resp_1", "hi")}
	reg := newTestRegistry(t, map[string]InferenceBackend{"local": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	rec := doProviderReq(t, h, "", map[string]any{"model": "test", "input": "hi"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no token, got %d", rec.Code)
	}
	rec = doProviderReq(t, h, "wrong", map[string]any{"model": "test", "input": "hi"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", rec.Code)
	}
}

func TestServer_ServeRefusesUnauthenticated(t *testing.T) {
	backend := &fakeInferenceBackend{name: "local", typ: "openai-compatible"}
	reg := newTestRegistry(t, map[string]InferenceBackend{"local": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})
	s := NewServer(testProviderConfig(""), reg)
	if err := s.Serve(nil, "127.0.0.1:0"); err == nil { //nolint:staticcheck
		t.Fatalf("expected Serve to refuse an unconfigured (empty-token) gateway")
	}
}

func TestServer_NonStreamingResponse(t *testing.T) {
	backend := &fakeInferenceBackend{name: "local", typ: "openai-compatible", events: simpleTextEvents("resp_1", "hello there")}
	reg := newTestRegistry(t, map[string]InferenceBackend{"local": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	rec := doProviderReq(t, h, "secret", map[string]any{"model": "test", "input": "hi"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != StatusCompleted || len(resp.Output) == 0 || resp.Output[0].Content[0].Text != "hello there" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if backend.invocations != 1 {
		t.Fatalf("expected exactly 1 backend invocation, got %d", backend.invocations)
	}
}

func TestServer_StreamingResponseSSE(t *testing.T) {
	backend := &fakeInferenceBackend{name: "local", typ: "openai-compatible", events: simpleTextEvents("resp_1", "streamed")}
	reg := newTestRegistry(t, map[string]InferenceBackend{"local": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	b, _ := json.Marshal(map[string]any{"model": "test", "input": "hi", "stream": true})
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: response.created") || !strings.Contains(body, "event: response.completed") {
		t.Fatalf("expected named SSE events in body, got: %s", body)
	}

	scanner := bufio.NewScanner(strings.NewReader(body))
	eventCount := 0
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "event:") {
			eventCount++
		}
	}
	if eventCount == 0 {
		t.Fatalf("expected at least one SSE event")
	}
}

func TestServer_MissingModelRejected(t *testing.T) {
	backend := &fakeInferenceBackend{name: "local", typ: "openai-compatible"}
	reg := newTestRegistry(t, map[string]InferenceBackend{"local": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	rec := doProviderReq(t, h, "secret", map[string]any{"input": "hi"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing model, got %d", rec.Code)
	}
}

func TestServer_OversizedPayloadRejected(t *testing.T) {
	backend := &fakeInferenceBackend{name: "local", typ: "openai-compatible"}
	reg := newTestRegistry(t, map[string]InferenceBackend{"local": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})
	cfg := testProviderConfig("secret")
	cfg.RequestMaxBytes = 100
	s := NewServer(cfg, reg)
	h := s.Handler()

	hugeInput := strings.Repeat("x", 10000)
	rec := doProviderReq(t, h, "secret", map[string]any{"model": "test", "input": hugeInput}, nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized payload, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestServer_RateLimited(t *testing.T) {
	backend := &fakeInferenceBackend{name: "local", typ: "openai-compatible", events: simpleTextEvents("resp_1", "hi")}
	reg := newTestRegistry(t, map[string]InferenceBackend{"local": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	var lastCode int
	for i := 0; i < defaultRateLimitPerMin+5; i++ {
		lastCode = doProviderReq(t, h, "secret", map[string]any{"model": "test", "input": "hi"}, nil).Code
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exceeding rate limit, got %d", lastCode)
	}
}

func TestServer_ExplicitBackendSelection(t *testing.T) {
	backendA := &fakeInferenceBackend{name: "a", typ: "openai-compatible", events: simpleTextEvents("resp_a", "from A")}
	backendB := &fakeInferenceBackend{name: "b", typ: "openai-compatible", events: simpleTextEvents("resp_b", "from B")}
	reg := newTestRegistry(t, map[string]InferenceBackend{"a": backendA, "b": backendB}, Policy{ZeroCreditMode: true, DefaultBackend: "a"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	rec := doProviderReq(t, h, "secret", map[string]any{"model": "test", "input": "hi"}, map[string]string{"X-HarnessMesh-Backend": "b"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Output[0].Content[0].Text != "from B" {
		t.Fatalf("expected explicit backend selection to route to B, got %q", resp.Output[0].Content[0].Text)
	}
	if backendA.invocations != 0 {
		t.Fatalf("expected backend A to never be invoked, got %d invocations", backendA.invocations)
	}
}

func TestServer_ZeroCreditMode_DeniesOpenAIAndCodexBackends(t *testing.T) {
	openaiBackend := &fakeInferenceBackend{name: "openai", typ: "openai-api", events: simpleTextEvents("resp_1", "should never run")}
	codexBackend := &fakeInferenceBackend{name: "codex", typ: "codex", events: simpleTextEvents("resp_2", "should never run")}
	localBackend := &fakeInferenceBackend{name: "local", typ: "openai-compatible", events: simpleTextEvents("resp_3", "ok")}

	reg := newTestRegistry(t, map[string]InferenceBackend{
		"openai": openaiBackend, "codex": codexBackend, "local": localBackend,
	}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	rec := doProviderReq(t, h, "secret", map[string]any{"model": "test", "input": "hi"}, map[string]string{"X-HarnessMesh-Backend": "openai"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for explicit openai-api backend under zero-credit mode, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "metered_backend_denied" {
		t.Fatalf("expected error code metered_backend_denied, got %+v", body)
	}

	rec = doProviderReq(t, h, "secret", map[string]any{"model": "test", "input": "hi"}, map[string]string{"X-HarnessMesh-Backend": "codex"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for explicit codex backend under zero-credit mode, got %d", rec.Code)
	}

	if openaiBackend.invocations != 0 || codexBackend.invocations != 0 {
		t.Fatalf("expected zero invocations of denied backends, got openai=%d codex=%d", openaiBackend.invocations, codexBackend.invocations)
	}

	// The allowed local backend still works normally.
	rec = doProviderReq(t, h, "secret", map[string]any{"model": "test", "input": "hi"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected local backend to succeed, got %d", rec.Code)
	}
}

func TestServer_BackendFailure_NoSilentFallbackToForbidden(t *testing.T) {
	failingLocal := &fakeInferenceBackend{name: "local", typ: "openai-compatible", streamErr: &BackendUnavailableError{Backend: "local", Reason: "connection refused"}}
	openaiBackend := &fakeInferenceBackend{name: "openai", typ: "openai-api", events: simpleTextEvents("resp_1", "would be a policy violation")}

	reg := newTestRegistry(t, map[string]InferenceBackend{"local": failingLocal, "openai": openaiBackend}, Policy{
		ZeroCreditMode: true, DefaultBackend: "local", FallbackEnabled: true, FallbackOrder: []string{"openai"},
	})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	rec := doProviderReq(t, h, "secret", map[string]any{"model": "test", "input": "hi"}, nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected the request to fail rather than silently fall back to the forbidden openai-api backend")
	}
	if openaiBackend.invocations != 0 {
		t.Fatalf("expected zero invocations of the forbidden fallback backend, got %d", openaiBackend.invocations)
	}
}

func TestServer_Readyz_NotReadyWithOnlyForbiddenBackend(t *testing.T) {
	openaiBackend := &fakeInferenceBackend{name: "openai", typ: "openai-api"}
	reg := newTestRegistry(t, map[string]InferenceBackend{"openai": openaiBackend}, Policy{ZeroCreditMode: true, DefaultBackend: "openai"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when only a forbidden backend is configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestServer_Readyz_ReadyWithAllowedHealthyBackend(t *testing.T) {
	local := &fakeInferenceBackend{name: "local", typ: "openai-compatible"}
	reg := newTestRegistry(t, map[string]InferenceBackend{"local": local}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when an allowed backend is healthy, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestServer_AuditSinkRecordsRequests(t *testing.T) {
	backend := &fakeInferenceBackend{name: "local", typ: "openai-compatible", events: simpleTextEvents("resp_1", "hi")}
	reg := newTestRegistry(t, map[string]InferenceBackend{"local": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})
	s := NewServer(testProviderConfig("secret"), reg)
	audit := &memoryAuditSink{}
	s.SetAuditSink(audit)
	h := s.Handler()

	doProviderReq(t, h, "secret", map[string]any{"model": "test", "input": "hi"}, nil)
	if len(audit.records) != 1 {
		t.Fatalf("expected exactly 1 audit record, got %d", len(audit.records))
	}
	if audit.records[0].Result != "ok" || audit.records[0].Backend != "local" {
		t.Fatalf("unexpected audit record: %+v", audit.records[0])
	}
}

type memoryAuditSink struct {
	records []AuditRecord
}

func (m *memoryAuditSink) Record(r AuditRecord) { m.records = append(m.records, r) }
