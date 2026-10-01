package provider

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
)

// newSIWCHandlerPointingAt builds a full provider HTTP handler wired to a
// chatgpt-subscription backend whose upstream responsesURL points at the
// given URL (a caller-controlled fake server, e.g. newRawSSEUpstream),
// mirroring newSIWCTestServer's setup but without owning the upstream
// server itself.
func newSIWCHandlerPointingAt(t *testing.T, tokenPath, upstreamURL string) (http.Handler, *SubscriptionBackend) {
	t.Helper()
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
	sub.client = &siwcTokenClient{tokenURL: upstreamURL, responsesURL: upstreamURL, httpClient: http.DefaultClient}

	server := NewServer(cfg, registry)
	return server.Handler(), sub
}

func fixedFutureExpiry() time.Time { return time.Now().Add(time.Hour) }

// This file reproduces and proves the fix for a real Codex VS Code
// interoperability defect: relaying a real upstream Responses SSE stream
// through StreamEvent's typed struct (parse, reconstruct, re-marshal)
// silently dropped fields the struct doesn't model. The real, observed
// symptoms were:
//   - Codex app-server logging repeatedly "OutputTextDelta without active
//     item" (the reconstructed stream lost fields the item lifecycle
//     depends on), ending the turn with task_complete/
//     last_agent_message=null.
//   - a forced function_call turn's response.completed arriving with
//     "output": [] even though a function_call was emitted mid-stream.
//
// The fix (stream.go's RawEventSink + backend_subscription.go's
// relaySIWCStream) relays each event's raw JSON bytes verbatim for any
// sink that supports it - which the real HTTP path's sseSink always does
// - rather than round-tripping through StreamEvent. These tests drive a
// captured/fake upstream SSE fixture through the COMPLETE provider HTTP
// path (real server, real sseSink, httptest.ResponseRecorder) and assert
// byte-for-byte fidelity.

func newRawSSEUpstream(t *testing.T, sseBody string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte(sseBody))
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sseDataLine extracts the raw bytes after "data: " on one SSE line.
func sseDataLines(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE body: %v", err)
	}
	return out
}

func sseEventTypeLines(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event:") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "event:")))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE body: %v", err)
	}
	return out
}

// realisticTextStreamFixture is a captured/fake upstream SSE fixture
// representing a normal assistant text response, matching the documented
// event lifecycle exactly: response.created, response.in_progress,
// output_item.added(message), content_part.added(output_text), multiple
// output_text.delta, output_text.done, content_part.done,
// output_item.done, response.completed - with monotonic sequence_number
// and a stable item id threaded through every event that references it.
const realisticTextStreamFixture = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_text1","object":"response","status":"in_progress","output":[]}}

event: response.in_progress
data: {"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_text1","object":"response","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}

event: response.content_part.added
data: {"type":"response.content_part.added","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_1","output_index":0,"content_index":0,"delta":", world"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":6,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"!"}

event: response.output_text.done
data: {"type":"response.output_text.done","sequence_number":7,"item_id":"msg_1","output_index":0,"content_index":0,"text":"Hello, world!"}

event: response.content_part.done
data: {"type":"response.content_part.done","sequence_number":8,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello, world!"}}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":9,"output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, world!"}]}}

event: response.completed
data: {"type":"response.completed","sequence_number":10,"response":{"id":"resp_text1","object":"response","status":"completed","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello, world!"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}

`

// TestSSELossless_RealisticTextStream_FullHTTPPath is the Mission 4
// regression test.
func TestSSELossless_RealisticTextStream_FullHTTPPath(t *testing.T) {
	upstream := newRawSSEUpstream(t, realisticTextStreamFixture)

	tokenPath := t.TempDir() + "/auth.json"
	if err := saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "a", ExpiresAt: fixedFutureExpiry()}); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	handler, sub := newSIWCHandlerPointingAt(t, tokenPath, upstream.URL)
	_ = sub

	rec := doRawProviderReq(t, handler, "test-provider-token", `{"model":"gpt-5.6-luna","input":"hi","stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	wantData := sseDataLines(t, realisticTextStreamFixture)
	gotData := sseDataLines(t, rec.Body.String())
	if len(gotData) != len(wantData) {
		t.Fatalf("event count mismatch: got %d, want %d\ngot body:\n%s", len(gotData), len(wantData), rec.Body.String())
	}
	// 3. sequence numbers identical / 1. event names identical / 2. event
	// order identical - all implied by byte equality per line, asserted
	// explicitly below too for clarity.
	for i := range wantData {
		if gotData[i] != wantData[i] {
			t.Fatalf("data line %d not byte-identical:\n got:  %s\n want: %s", i, gotData[i], wantData[i])
		}
	}
	wantTypes := sseEventTypeLines(t, realisticTextStreamFixture)
	gotTypes := sseEventTypeLines(t, rec.Body.String())
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("event type line count mismatch: got %v want %v", gotTypes, wantTypes)
	}
	for i := range wantTypes {
		if gotTypes[i] != wantTypes[i] {
			t.Fatalf("event %d: type mismatch got %q want %q", i, gotTypes[i], wantTypes[i])
		}
	}

	// 4,5,6,7: output_index/content_index/item ids/item_id references -
	// spot-check via structural decode of a couple of representative
	// events, on top of the byte-equality check above.
	var addedEv, deltaEv map[string]any
	if err := json.Unmarshal([]byte(gotData[2]), &addedEv); err != nil {
		t.Fatalf("decode output_item.added: %v", err)
	}
	if err := json.Unmarshal([]byte(gotData[4]), &deltaEv); err != nil {
		t.Fatalf("decode output_text.delta: %v", err)
	}
	addedItemID := addedEv["item"].(map[string]any)["id"]
	if addedItemID != "msg_1" {
		t.Fatalf("expected output_item.added item.id=msg_1, got %v", addedItemID)
	}
	if deltaEv["item_id"] != "msg_1" {
		t.Fatalf("expected output_text.delta item_id=msg_1 matching the added item, got %v", deltaEv["item_id"])
	}

	// Mission 2 / 8: response.completed.output remains intact - not
	// synthesized as an empty array.
	var completedEv map[string]any
	if err := json.Unmarshal([]byte(gotData[len(gotData)-1]), &completedEv); err != nil {
		t.Fatalf("decode response.completed: %v", err)
	}
	output := completedEv["response"].(map[string]any)["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected response.completed.output to retain the 1 emitted item, got %d: %v", len(output), output)
	}
	outputItem := output[0].(map[string]any)
	if outputItem["id"] != "msg_1" || outputItem["status"] != "completed" {
		t.Fatalf("expected the output item preserved intact, got %v", outputItem)
	}
}

// TestSSELossless_FunctionCallStream_FullHTTPPath is the Mission 5
// regression test: response.output_item.added(function_call),
// function_call_arguments.delta/done, output_item.done, response.completed
// - asserting call_id, item id, function name, and the final output object
// are preserved, and specifically that response.completed.output is not
// emptied (the exact real defect reported).
const functionCallStreamFixture = `event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp_fn1","object":"response","status":"in_progress","output":[]}}

event: response.in_progress
data: {"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_fn1","object":"response","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_abc123","name":"get_weather","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc_1","output_index":0,"delta":"{\"city\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":4,"item_id":"fc_1","output_index":0,"delta":"\"nyc\"}"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","sequence_number":5,"item_id":"fc_1","output_index":0,"arguments":"{\"city\":\"nyc\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_abc123","name":"get_weather","arguments":"{\"city\":\"nyc\"}"}}

event: response.completed
data: {"type":"response.completed","sequence_number":7,"response":{"id":"resp_fn1","object":"response","status":"completed","output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_abc123","name":"get_weather","arguments":"{\"city\":\"nyc\"}"}]}}

`

func TestSSELossless_FunctionCallStream_FullHTTPPath(t *testing.T) {
	upstream := newRawSSEUpstream(t, functionCallStreamFixture)

	tokenPath := t.TempDir() + "/auth.json"
	if err := saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "a", ExpiresAt: fixedFutureExpiry()}); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	handler, _ := newSIWCHandlerPointingAt(t, tokenPath, upstream.URL)

	rec := doRawProviderReq(t, handler, "test-provider-token", `{"model":"gpt-5.6-luna","input":"what's the weather?","stream":true,"tools":[{"type":"function","name":"get_weather"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	wantData := sseDataLines(t, functionCallStreamFixture)
	gotData := sseDataLines(t, rec.Body.String())
	if len(gotData) != len(wantData) {
		t.Fatalf("event count mismatch: got %d, want %d\ngot body:\n%s", len(gotData), len(wantData), rec.Body.String())
	}
	for i := range wantData {
		if gotData[i] != wantData[i] {
			t.Fatalf("data line %d not byte-identical:\n got:  %s\n want: %s", i, gotData[i], wantData[i])
		}
	}

	var completedEv map[string]any
	if err := json.Unmarshal([]byte(gotData[len(gotData)-1]), &completedEv); err != nil {
		t.Fatalf("decode response.completed: %v", err)
	}
	output := completedEv["response"].(map[string]any)["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected response.completed.output to retain the function_call item (the exact reported defect: output was observed as []), got %d: %v", len(output), output)
	}
	fc := output[0].(map[string]any)
	if fc["call_id"] != "call_abc123" {
		t.Fatalf("expected call_id preserved, got %v", fc["call_id"])
	}
	if fc["id"] != "fc_1" {
		t.Fatalf("expected item id preserved, got %v", fc["id"])
	}
	if fc["name"] != "get_weather" {
		t.Fatalf("expected function name preserved, got %v", fc["name"])
	}
	if fc["arguments"] != `{"city":"nyc"}` {
		t.Fatalf("expected final arguments preserved, got %v", fc["arguments"])
	}
}
