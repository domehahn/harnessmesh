package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// capturingRequestShapeDiagnosticsSink records every RequestShapeDiagnostic
// it receives, for tests that assert on the redacted request-shape
// diagnostic instrumentation itself.
type capturingRequestShapeDiagnosticsSink struct {
	records []RequestShapeDiagnostic
}

func (s *capturingRequestShapeDiagnosticsSink) RecordRequestShape(d RequestShapeDiagnostic) {
	s.records = append(s.records, d)
}

// TestRequestShapeDiagnostic_CapturesRealisticCodexShape drives a request
// matching the documented example Codex request shape - keys model/input/
// instructions/tools/tool_choice/reasoning/stream/store, input item types
// message/reasoning/additional_tools, tool types function/custom - and
// proves the diagnostic captures exactly that shape, with nothing else.
func TestRequestShapeDiagnostic_CapturesRealisticCodexShape(t *testing.T) {
	fakeResponses := newFakeResponsesServer("normal")
	defer fakeResponses.Close()
	tokenPath := t.TempDir() + "/auth.json"
	if err := saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: fakeResponses.URL, responsesURL: fakeResponses.URL, httpClient: http.DefaultClient}
	sink := &capturingRequestShapeDiagnosticsSink{}
	b.requestDiagnostics = sink

	body := `{
		"model": "gpt-5.6-luna",
		"instructions": "you are a coding agent",
		"reasoning": {"effort": "medium"},
		"tool_choice": "auto",
		"stream": true,
		"store": false,
		"tools": [{"type":"function","name":"get_weather"}],
		"input": [
			{"role":"user","content":"do the thing"},
			{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thinking"}]},
			{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"code_exec"}]}
		]
	}`
	var req Request
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	collectingSink := newCollectingSink()
	if err := b.StreamResponse(context.Background(), req, collectingSink); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	if len(sink.records) != 1 {
		t.Fatalf("expected exactly 1 diagnostic record, got %d", len(sink.records))
	}
	d := sink.records[0]
	if !d.Accepted {
		t.Fatalf("expected the request to be accepted, got rejection=%q", d.RejectionReason)
	}
	if !d.Stream || d.Store {
		t.Fatalf("expected stream=true store=false, got stream=%v store=%v", d.Stream, d.Store)
	}
	wantKeys := []string{"input", "instructions", "model", "reasoning", "stream", "store", "tool_choice", "tools"}
	if !sameStringSet(d.TopLevelKeys, wantKeys) {
		t.Fatalf("unexpected top-level keys: got %v want (set) %v", d.TopLevelKeys, wantKeys)
	}
	wantItemTypes := []string{"message", "reasoning", "additional_tools"}
	if !sameStringSet(d.InputItemTypes, wantItemTypes) {
		t.Fatalf("unexpected input item types: got %v want (set) %v", d.InputItemTypes, wantItemTypes)
	}
	wantToolTypes := []string{"function", "custom"}
	if !sameStringSet(d.ToolTypes, wantToolTypes) {
		t.Fatalf("unexpected tool types: got %v want (set) %v", d.ToolTypes, wantToolTypes)
	}
}

func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	set := make(map[string]bool, len(want))
	for _, w := range want {
		set[w] = true
	}
	for _, g := range got {
		if !set[g] {
			return false
		}
	}
	return true
}

// TestRequestShapeDiagnostic_NeverLogsSensitiveValues proves the diagnostic
// record - and the production logger's rendering of it - never contains
// the user's actual text, tool arguments, or the bearer token: only key/
// type names and booleans.
func TestRequestShapeDiagnostic_NeverLogsSensitiveValues(t *testing.T) {
	const secretUserText = "SECRET_USER_PROMPT_MUST_NEVER_APPEAR_IN_LOGS"
	const secretToolArgs = `{"password":"hunter2-should-never-log"}`
	const secretAccessToken = "super-secret-access-token"

	fakeResponses := newFakeResponsesServer("normal")
	defer fakeResponses.Close()
	tokenPath := t.TempDir() + "/auth.json"
	if err := saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: secretAccessToken, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: fakeResponses.URL, responsesURL: fakeResponses.URL, httpClient: http.DefaultClient}
	sink := &capturingRequestShapeDiagnosticsSink{}
	b.requestDiagnostics = sink

	req := Request{
		Model: "x",
		Input: InputItems{
			{Type: "message", Role: "user", Content: NewTextContent(secretUserText)},
			{Type: "function_call", CallID: "c1", Name: "do_thing", Arguments: secretToolArgs},
		},
	}
	collectingSink := newCollectingSink()
	if err := b.StreamResponse(context.Background(), req, collectingSink); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	if len(sink.records) != 1 {
		t.Fatalf("expected exactly 1 diagnostic record, got %d", len(sink.records))
	}
	d := sink.records[0]

	// Directly inspect every string-bearing field the record exposes.
	allFields := strings.Join(append(append([]string{d.RejectionReason}, d.TopLevelKeys...), append(d.InputItemTypes, d.ToolTypes...)...), "|")
	if strings.Contains(allFields, secretUserText) {
		t.Fatalf("diagnostic record must never contain user text, got: %s", allFields)
	}
	if strings.Contains(allFields, "hunter2") {
		t.Fatalf("diagnostic record must never contain tool arguments, got: %s", allFields)
	}
	if strings.Contains(allFields, secretAccessToken) {
		t.Fatalf("diagnostic record must never contain the access token, got: %s", allFields)
	}

	// Also exercise the actual production logger's rendered line.
	rendered := captureStderr(t, func() {
		stderrRequestShapeDiagnosticsLogger{}.RecordRequestShape(d)
	})
	if strings.Contains(rendered, secretUserText) || strings.Contains(rendered, "hunter2") || strings.Contains(rendered, secretAccessToken) {
		t.Fatalf("production logger output must never contain sensitive values, got: %s", rendered)
	}
}

// captureStderr redirects os.Stderr for the duration of fn and returns
// what was written, for asserting on the production stderr logger's exact
// output without interleaving with other tests (tests in this package do
// not run this one in parallel).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = old
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(out)
}
