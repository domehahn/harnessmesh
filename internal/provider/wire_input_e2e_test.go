package provider

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
)

// This file drives full end-to-end regression tests for the two real-
// account Responses API compatibility defects found after a real,
// successful SIWC inference: HTTP handler -> request parser -> canonical
// provider model -> SubscriptionBackend -> captured upstream request. Each
// test inspects the ACTUAL bytes HarnessMesh sent upstream (via
// fakeResponsesServer.LastBody), not a re-parsed/re-marshaled copy, so a
// regression that reintroduces a serialized "type":"" cannot hide behind a
// round-trip through Go structs.

func newSIWCTestServer(t *testing.T) (http.Handler, *fakeResponsesServer) {
	t.Helper()
	fakeResponses := newFakeResponsesServer("normal")
	t.Cleanup(fakeResponses.Close)

	tokenPath := t.TempDir() + "/auth.json"
	tokens := &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "test-access", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(time.Hour)}
	if err := saveSIWCTokenSet(tokenPath, tokens); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}

	cfg := config.ProviderGatewayConfig{
		Enabled: true, Token: "test-provider-token", DefaultBackend: "chatgpt",
		Backends: map[string]config.ProviderBackendConfig{"chatgpt": {Type: "chatgpt-subscription"}},
	}
	registry, err := NewRegistry(cfg, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	sub := registry.backends["chatgpt"].(*SubscriptionBackend)
	sub.tokenPath = tokenPath
	sub.client = &siwcTokenClient{tokenURL: fakeResponses.URL, responsesURL: fakeResponses.URL, httpClient: http.DefaultClient}

	server := NewServer(cfg, registry)
	return server.Handler(), fakeResponses
}

func doRawProviderReq(t *testing.T, h http.Handler, token string, rawBody string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(rawBody)))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// TestE2E_OfficialSIWCDocExample_EasyMessage is based directly on the
// current official SIWC example:
//
//	{
//	  "model": "<model>",
//	  "input": [{"role": "user", "content": "Say exactly: Hello, world!"}],
//	  "store": false,
//	  "stream": true
//	}
func TestE2E_OfficialSIWCDocExample_EasyMessage(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	rawBody := `{
		"model": "gpt-5.6-luna",
		"input": [
			{"role": "user", "content": "Say exactly: Hello, world!"}
		],
		"store": false,
		"stream": true
	}`

	rec := doRawProviderReq(t, handler, "test-provider-token", rawBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// The request set "stream": true, so the client receives an SSE stream,
	// not a single JSON body - assert it reached response.completed.
	if !bytes.Contains(rec.Body.Bytes(), []byte("response.completed")) {
		t.Fatalf("expected the SSE stream to reach response.completed, got: %s", rec.Body.String())
	}

	// The captured upstream request must remain valid Responses API JSON,
	// and must never contain a serialized "type":"".
	upstream := fakeResponses.LastBody()
	assertValidResponsesAPIJSON(t, upstream)
	assertNoEmptyType(t, upstream)

	var upstreamReq map[string]any
	if err := json.Unmarshal(upstream, &upstreamReq); err != nil {
		t.Fatalf("upstream body is not valid JSON: %v\nbody: %s", err, upstream)
	}
	inputArr, ok := upstreamReq["input"].([]any)
	if !ok || len(inputArr) != 1 {
		t.Fatalf("expected exactly one upstream input item, got %v", upstreamReq["input"])
	}
	item, ok := inputArr[0].(map[string]any)
	if !ok {
		t.Fatalf("expected the input item to be a JSON object, got %v", inputArr[0])
	}
	if item["type"] != "message" {
		t.Fatalf(`expected the upstream item to carry "type":"message", got %v`, item["type"])
	}
	if item["role"] != "user" {
		t.Fatalf(`expected "role":"user", got %v`, item["role"])
	}
	if item["content"] != "Say exactly: Hello, world!" {
		t.Fatalf("expected the plain-string content to be forwarded verbatim, got %v", item["content"])
	}
}

// TestE2E_StructuredMessageInput_NeverEmitsEmptyTypeUpstream is the
// structured-message regression test: the exact shape that was locally
// accepted but forwarded upstream with a serialized "type":"", which
// OpenAI rejected with "Invalid value: ”. Supported values are: ...
// 'message', ...".
func TestE2E_StructuredMessageInput_NeverEmitsEmptyTypeUpstream(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	rawBody := `{
		"model": "gpt-5.6-luna",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Say exactly: HARNESSMESH_SIWC_OK"}
				]
			}
		],
		"store": false,
		"stream": true
	}`

	rec := doRawProviderReq(t, handler, "test-provider-token", rawBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	upstream := fakeResponses.LastBody()
	assertValidResponsesAPIJSON(t, upstream)
	assertNoEmptyType(t, upstream)

	var upstreamReq map[string]any
	if err := json.Unmarshal(upstream, &upstreamReq); err != nil {
		t.Fatalf("upstream body is not valid JSON: %v\nbody: %s", err, upstream)
	}
	inputArr := upstreamReq["input"].([]any)
	item := inputArr[0].(map[string]any)
	if item["type"] != "message" {
		t.Fatalf(`expected the upstream item to carry "type":"message" (never ""), got %v`, item["type"])
	}
	contentArr, ok := item["content"].([]any)
	if !ok || len(contentArr) != 1 {
		t.Fatalf("expected the content-parts array to be forwarded as an array, got %v", item["content"])
	}
	part := contentArr[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "Say exactly: HARNESSMESH_SIWC_OK" {
		t.Fatalf("expected the content part to be forwarded verbatim, got %v", part)
	}
}

// TestE2E_TopLevelStringInput_NeverEmitsEmptyTypeUpstream covers the
// simplest documented shorthand: a bare string "input".
func TestE2E_TopLevelStringInput_NeverEmitsEmptyTypeUpstream(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	rawBody := `{"model": "gpt-5.6-luna", "input": "Say exactly: HARNESSMESH_SIWC_OK", "store": false, "stream": true}`
	rec := doRawProviderReq(t, handler, "test-provider-token", rawBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	upstream := fakeResponses.LastBody()
	assertValidResponsesAPIJSON(t, upstream)
	assertNoEmptyType(t, upstream)
}

// assertValidResponsesAPIJSON is a minimal structural sanity check: the
// captured upstream body must be valid JSON and must be a JSON object (not
// e.g. a truncated/malformed fragment).
func assertValidResponsesAPIJSON(t *testing.T, body []byte) {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("captured upstream body is not valid Responses API JSON: %v\nbody: %s", err, body)
	}
	if _, ok := v["model"]; !ok {
		t.Fatalf("expected the upstream body to carry \"model\", got: %s", body)
	}
	if _, ok := v["input"]; !ok {
		t.Fatalf("expected the upstream body to carry \"input\", got: %s", body)
	}
}
