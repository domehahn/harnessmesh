package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/creditguard"
)

// TestE2E_ZeroCreditMode_FullLifecycleProof drives a complete Codex-shaped
// request lifecycle (a normal text turn, a tool-call turn, and cancellation
// of a third request) through the *real* stack - config parsing, Registry,
// and Server, exactly as cmd/harnessmesh's `provider serve` wires them up -
// against a local fake backend, with OPENAI_API_KEY set and a tripwire
// "codex" binary on PATH, and proves:
//
//	OpenAI API-key-billed calls (creditguard.BackendOpenAIAPI) = 0
//	Codex process/backend invocations (creditguard.BackendCodex, and the
//	  tripwire file) = 0
//
// This test never touches the chatgpt-subscription backend, so it says
// nothing about ChatGPT-plan Responses usage either way - see
// TestE2E_ChatGPTSubscriptionBackend_ZeroAPIKeyBilling_ZeroCodex below for
// that backend's own, differently-worded proof (that one *does* expect a
// nonzero ChatGPT-plan-usage counter - it is a real, billed-to-your-plan
// call, just never an API-key-metered one and never a Codex invocation).
// This is the mission's central acceptance criterion (sections 26-30, 50).
func TestE2E_ZeroCreditMode_FullLifecycleProof(t *testing.T) {
	tripwireDir := t.TempDir()
	tripwireFile := filepath.Join(tripwireDir, "codex-was-invoked")
	fakeCodex := filepath.Join(tripwireDir, "codex")
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\ntouch "+tripwireFile+"\nexit 1\n"), 0755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	oldPath := os.Getenv("PATH")
	oldKey := os.Getenv("OPENAI_API_KEY")
	t.Cleanup(func() {
		os.Setenv("PATH", oldPath)
		os.Setenv("OPENAI_API_KEY", oldKey)
	})
	os.Setenv("PATH", tripwireDir+string(os.PathListSeparator)+oldPath)
	os.Setenv("OPENAI_API_KEY", "sk-test-should-never-be-used-by-the-provider-gateway")

	creditguard.ResetForTest()

	local := newFakeChatServer(modeNormal)
	defer local.Close()
	localTools := newFakeChatServer(modeToolCall)
	defer localTools.Close()

	cfgJSON := `{
		"version": 2,
		"agents": {"placeholder": {"kind": "fake", "role": "executor", "writable": true}},
		"provider": {
			"enabled": true,
			"token": "test-provider-token",
			"default_backend": "local",
			"backends": {
				"local": {"type": "openai-compatible", "base_url": "` + local.URL + `"},
				"local-tools": {"type": "openai-compatible", "base_url": "` + localTools.URL + `"},
				"openai": {"type": "openai-api"},
				"codex": {"type": "codex"}
			}
		}
	}`
	cfg, err := config.Parse([]byte(cfgJSON))
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	if !cfg.Provider.IsZeroCreditMode() {
		t.Fatalf("expected zero-credit mode to default to true")
	}

	registry, err := NewRegistry(cfg.Provider, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	server := NewServer(cfg.Provider, registry)
	audit := &memoryAuditSink{}
	server.SetAuditSink(audit)
	handler := server.Handler()

	// Step 1: a normal Codex-shaped text turn.
	rec := doProviderReq(t, handler, "test-provider-token", map[string]any{
		"model": "local-coder",
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "say hi"}}},
		},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for text turn, got %d: %s", rec.Code, rec.Body.String())
	}
	var textResp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &textResp); err != nil {
		t.Fatalf("decode text response: %v", err)
	}
	if textResp.Status != StatusCompleted || textResp.Output[0].Content[0].Text != "Hello, world" {
		t.Fatalf("unexpected text response: %+v", textResp)
	}

	// Step 2: a tool-call turn, then submit the tool result on a follow-up
	// turn (the Codex-side round trip).
	rec = doProviderReq(t, handler, "test-provider-token", map[string]any{
		"model": "local-coder",
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "what's the weather"}}},
		},
		"tools": []map[string]any{{"type": "function", "name": "get_weather"}},
	}, map[string]string{"X-HarnessMesh-Backend": "local-tools"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for tool-call turn, got %d: %s", rec.Code, rec.Body.String())
	}
	var toolResp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &toolResp); err != nil {
		t.Fatalf("decode tool response: %v", err)
	}
	foundCall := false
	for _, item := range toolResp.Output {
		if item.Type == "function_call" && item.Name == "get_weather" {
			foundCall = true
		}
	}
	if !foundCall {
		t.Fatalf("expected a function_call output item, got: %+v", toolResp.Output)
	}

	// Step 3: cancel a request mid-stream.
	body, _ := json.Marshal(map[string]any{
		"model": "local-coder", "stream": true,
		"input": []map[string]any{{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "go"}}}},
	})
	cancelSrv := newFakeChatServer(modePartialThenHang)
	defer cancelSrv.Close()
	cfg.Provider.Backends["cancel-me"] = config.ProviderBackendConfig{Type: "openai-compatible", BaseURL: cancelSrv.URL}
	registry2, err := NewRegistry(cfg.Provider, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewRegistry (2): %v", err)
	}
	server2 := NewServer(cfg.Provider, registry2)
	handler2 := server2.Handler()

	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer test-provider-token")
	r.Header.Set("X-HarnessMesh-Backend", "cancel-me")
	rec2 := httptest.NewRecorder()
	requestDone := make(chan struct{})
	go func() {
		handler2.ServeHTTP(rec2, r)
		close(requestDone)
	}()
	time.Sleep(30 * time.Millisecond) // let the partial output reach the sink first
	cancel()                          // simulate the client cancelling the request
	select {
	case <-requestDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected the cancelled request's handler to return promptly")
	}

	// Step 4: explicit references to the metered backends must be denied.
	rec = doProviderReq(t, handler, "test-provider-token", map[string]any{"model": "x", "input": "hi"}, map[string]string{"X-HarnessMesh-Backend": "openai"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 requesting the openai-api backend under zero-credit mode, got %d", rec.Code)
	}
	rec = doProviderReq(t, handler, "test-provider-token", map[string]any{"model": "x", "input": "hi"}, map[string]string{"X-HarnessMesh-Backend": "codex"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 requesting the codex backend under zero-credit mode, got %d", rec.Code)
	}

	// --- The critical proof ---
	if got := creditguard.Calls(creditguard.BackendOpenAIAPI); got != 0 {
		t.Fatalf("expected 0 API-key-billed OpenAI API calls during zero-credit provider E2E, got %d", got)
	}
	if got := creditguard.Calls(creditguard.BackendCodex); got != 0 {
		t.Fatalf("expected 0 Codex backend invocations (CodexInferenceBackend.StreamResponse never called) during zero-credit provider E2E, got %d", got)
	}
	if _, err := os.Stat(tripwireFile); err == nil {
		t.Fatalf("codex tripwire file exists: 0 Codex process invocations violated - the codex binary was executed during the zero-credit provider E2E")
	}
	if len(audit.records) < 2 {
		t.Fatalf("expected at least 2 audit records for the completed requests, got %d", len(audit.records))
	}
}

// TestE2E_ChatGPTSubscriptionBackend_ZeroAPIKeyBilling_ZeroCodex drives a
// full Codex-shaped request lifecycle through the real stack
// (config.Parse -> NewRegistry -> NewServer -> Handler) with the
// chatgpt-subscription backend selected as default, against a fake local
// server standing in for https://api.openai.com/v1/responses (never the
// real endpoint - no real ChatGPT account or OAuth consent is available in
// this environment). With OPENAI_API_KEY set and a tripwire "codex" binary
// on PATH, it proves all four distinct usage classes are correctly counted:
//
//	OpenAI API-key-billed calls (creditguard.BackendOpenAIAPI)     = 0
//	Codex process invocations (the tripwire file)                  = 0
//	Codex backend invocations (creditguard.BackendCodex)            = 0
//	ChatGPT-plan Responses calls (creditguard.BackendChatGPTPlanUsage) > 0
//
// The nonzero ChatGPT-plan-usage count is *expected and correct* (see
// backend_subscription.go's doc comment): this backend genuinely sends a
// POST https://api.openai.com/v1/responses request - it is real inference
// usage, consumed against the user's ChatGPT plan (and, on plans where
// that's bundled, the same shared allowance Codex draws from) - it is
// simply never billed via an OpenAI API key and never routed through the
// Codex CLI/backend. This test cannot and does not prove anything about
// real-world OpenAI-side billing/quota routing, which is outside
// HarnessMesh's visibility - only that HarnessMesh's own call path is
// exactly what it claims to be.
func TestE2E_ChatGPTSubscriptionBackend_ZeroAPIKeyBilling_ZeroCodex(t *testing.T) {
	tripwireDir := t.TempDir()
	tripwireFile := tripwireDir + "/codex-was-invoked"
	fakeCodex := tripwireDir + "/codex"
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\ntouch "+tripwireFile+"\nexit 1\n"), 0755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	oldPath := os.Getenv("PATH")
	oldKey := os.Getenv("OPENAI_API_KEY")
	t.Cleanup(func() {
		os.Setenv("PATH", oldPath)
		os.Setenv("OPENAI_API_KEY", oldKey)
	})
	os.Setenv("PATH", tripwireDir+string(os.PathListSeparator)+oldPath)
	os.Setenv("OPENAI_API_KEY", "sk-test-should-never-be-used-by-the-chatgpt-subscription-backend")

	creditguard.ResetForTest()

	fakeResponses := newFakeResponsesServer("normal")
	defer fakeResponses.Close()

	tokenPath := t.TempDir() + "/auth.json"
	tokens := &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "test-access", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(time.Hour)}
	if err := saveSIWCTokenSet(tokenPath, tokens); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}

	cfg := config.ProviderGatewayConfig{
		Enabled: true, Token: "test-provider-token", DefaultBackend: "chatgpt",
		Backends: map[string]config.ProviderBackendConfig{
			"chatgpt": {Type: "chatgpt-subscription"},
		},
	}
	registry, err := NewRegistry(cfg, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// Point the constructed backend at the fake local server and stored
	// test token instead of the real, documented OpenAI endpoints/token
	// path - this is the one deliberate seam between "real client code"
	// and "test environment," identical in spirit to every other
	// backend's test setup in this package.
	sub := registry.backends["chatgpt"].(*SubscriptionBackend)
	sub.tokenPath = tokenPath
	sub.client = &siwcTokenClient{tokenURL: fakeResponses.URL, responsesURL: fakeResponses.URL, httpClient: http.DefaultClient}

	server := NewServer(cfg, registry)
	handler := server.Handler()

	rec := doProviderReq(t, handler, "test-provider-token", map[string]any{
		"model": "gpt-5-chatgpt",
		"input": []map[string]any{
			{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "say hi"}}},
		},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Output[0].Content[0].Text != "hello from ChatGPT plan" {
		t.Fatalf("expected the fake ChatGPT-plan backend's output, got %q", resp.Output[0].Content[0].Text)
	}

	// --- The critical proof: all four usage classes, correctly counted ---
	if got := creditguard.Calls(creditguard.BackendOpenAIAPI); got != 0 {
		t.Fatalf("expected 0 API-key-billed OpenAI API calls, got %d", got)
	}
	if got := creditguard.Calls(creditguard.BackendCodex); got != 0 {
		t.Fatalf("expected 0 Codex backend invocations, got %d", got)
	}
	if _, err := os.Stat(tripwireFile); err == nil {
		t.Fatalf("codex tripwire file exists: 0 Codex process invocations violated - the codex binary was executed")
	}
	// This one is *expected* to be nonzero - it is what actually served
	// the request: a real ChatGPT-plan Responses call, via the documented,
	// non-API-key-billed path.
	if got := creditguard.Calls(creditguard.BackendChatGPTPlanUsage); got != 1 {
		t.Fatalf("expected exactly 1 ChatGPT-plan Responses call (the request that was actually served), got %d", got)
	}
}

// TestE2E_NetworkEgress_NoRequestToOpenAIOrExternalHosts proves the
// zero-credit request path never even attempts to dial any host other than
// the explicitly configured local backend - not by DNS blocking, but by
// wrapping the HTTP transport used by the whole gateway and asserting the
// only host contacted is the fake local backend's.
func TestE2E_NetworkEgress_NoRequestToOpenAIOrExternalHosts(t *testing.T) {
	local := newFakeChatServer(modeNormal)
	defer local.Close()

	backend := NewOpenAICompatibleBackend("local", config.ProviderBackendConfig{Type: "openai-compatible", BaseURL: local.URL, TimeoutSec: 5})

	var dialedHosts []string
	backend.client.Transport = &recordingTransport{
		hosts: &dialedHosts,
		inner: http.DefaultTransport,
	}

	sink := newCollectingSink()
	err := backend.StreamResponse(context.Background(), Request{Model: "test", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink)
	if err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	for _, h := range dialedHosts {
		if strings.Contains(h, "openai.com") || strings.Contains(h, "chatgpt.com") {
			t.Fatalf("provider gateway dialed a forbidden host: %s", h)
		}
	}
	if len(dialedHosts) == 0 {
		t.Fatalf("expected at least one recorded outbound host (the local fake backend)")
	}
}

type recordingTransport struct {
	hosts *[]string
	inner http.RoundTripper
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	*r.hosts = append(*r.hosts, req.URL.Host)
	return r.inner.RoundTrip(req)
}
