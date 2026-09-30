package provider

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestSIWCNormalize_ForbiddenField_NeverReachesUpstream is the full-path
// proof (HTTP -> backend -> captured-upstream) that a forbidden field
// rejected by normalizeForSIWC never reaches the real SIWC endpoint - not
// merely that the unit-level normalizer returns an error.
func TestSIWCNormalize_ForbiddenField_NeverReachesUpstream(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	body := `{"model":"gpt-5.6-luna","input":"hi","temperature":0.7}`
	rec := doRawProviderReq(t, handler, "test-provider-token", body)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected the request to be refused locally, got 200: %s", rec.Body.String())
	}
	if len(fakeResponses.LastBody()) != 0 {
		t.Fatalf("expected the request to never reach the upstream SIWC endpoint, but it did: %s", fakeResponses.LastBody())
	}
}

// TestSIWCNormalize_PreviousResponseID_NeverReachesUpstream is the same
// full-path proof for previous_response_id specifically.
func TestSIWCNormalize_PreviousResponseID_NeverReachesUpstream(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	body := `{"model":"gpt-5.6-luna","input":"hi","previous_response_id":"resp_abc"}`
	rec := doRawProviderReq(t, handler, "test-provider-token", body)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected the request to be refused locally, got 200: %s", rec.Body.String())
	}
	if len(fakeResponses.LastBody()) != 0 {
		t.Fatalf("expected the request to never reach the upstream SIWC endpoint, but it did: %s", fakeResponses.LastBody())
	}
}

// TestSIWCStatelessContinuation_FullReplay is the required store:false
// continuation test: request 1 is a message that (in a real account)
// would produce reasoning + a function_call; request 2 replays that
// reasoning item and function_call, adds this turn's function_call_output,
// and appends new context. Both requests are driven through the full HTTP
// -> backend -> captured-upstream path.
func TestSIWCStatelessContinuation_FullReplay(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	// --- Request 1: a plain first turn. ---
	req1Body := `{"model":"gpt-5.6-luna","input":[{"role":"user","content":"what's the weather in nyc?"}]}`
	rec1 := doRawProviderReq(t, handler, "test-provider-token", req1Body)
	if rec1.Code != http.StatusOK {
		t.Fatalf("request 1: expected 200, got %d: %s", rec1.Code, rec1.Body.String())
	}
	upstream1 := fakeResponses.LastBody()
	assertValidResponsesAPIJSON(t, upstream1)
	var upstream1Req map[string]any
	if err := json.Unmarshal(upstream1, &upstream1Req); err != nil {
		t.Fatalf("request 1: upstream body not valid JSON: %v", err)
	}
	if _, present := upstream1Req["previous_response_id"]; present {
		t.Fatalf("request 1: previous_response_id must be absent, got %v", upstream1Req["previous_response_id"])
	}
	if upstream1Req["store"] != false {
		t.Fatalf("request 1: expected store=false, got %v", upstream1Req["store"])
	}

	// --- Request 2: replays request 1's reasoning + function_call, adds
	// this turn's function_call_output, appends new user context. ---
	req2Input := `[
		{"role":"user","content":"what's the weather in nyc?"},
		{
			"type": "reasoning",
			"id": "rs_turn1",
			"summary": [{"type": "summary_text", "text": "I should call get_weather."}],
			"encrypted_content": "opaque-reasoning-blob-turn-1"
		},
		{
			"type": "function_call",
			"id": "fc_turn1",
			"call_id": "call_1",
			"name": "get_weather",
			"arguments": "{\"city\":\"nyc\"}",
			"caller": "codex-agent"
		},
		{
			"type": "function_call_output",
			"call_id": "call_1",
			"output": "72F and sunny"
		},
		{"role": "user", "content": "thanks, and tomorrow?"}
	]`
	req2Body := `{"model":"gpt-5.6-luna","input":` + req2Input + `}`
	rec2 := doRawProviderReq(t, handler, "test-provider-token", req2Body)
	if rec2.Code != http.StatusOK {
		t.Fatalf("request 2: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp2 Response
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("request 2: decode response: %v", err)
	}
	if resp2.Status != StatusCompleted {
		t.Fatalf("request 2: expected status=completed, got %q", resp2.Status)
	}

	upstream2 := fakeResponses.LastBody()
	assertValidResponsesAPIJSON(t, upstream2)
	assertNoEmptyType(t, upstream2)
	var upstream2Req map[string]any
	if err := json.Unmarshal(upstream2, &upstream2Req); err != nil {
		t.Fatalf("request 2: upstream body not valid JSON: %v", err)
	}

	// previous_response_id absent.
	if _, present := upstream2Req["previous_response_id"]; present {
		t.Fatalf("request 2: previous_response_id must be absent, got %v", upstream2Req["previous_response_id"])
	}
	if upstream2Req["store"] != false || upstream2Req["stream"] != true {
		t.Fatalf("request 2: expected store=false, stream=true, got store=%v stream=%v", upstream2Req["store"], upstream2Req["stream"])
	}

	// Full replay preserved in order: message, reasoning, function_call,
	// function_call_output, message.
	upstreamInput := upstream2Req["input"].([]any)
	if len(upstreamInput) != 5 {
		t.Fatalf("request 2: expected all 5 replayed items forwarded, got %d", len(upstreamInput))
	}
	wantOrder := []string{"message", "reasoning", "function_call", "function_call_output", "message"}
	for i, want := range wantOrder {
		item := upstreamInput[i].(map[string]any)
		if item["type"] != want {
			t.Fatalf("request 2: item %d: expected type=%q, got %v", i, want, item["type"])
		}
	}

	reasoningItem := upstreamInput[1].(map[string]any)
	if reasoningItem["id"] != "rs_turn1" {
		t.Fatalf("request 2: expected reasoning id preserved, got %v", reasoningItem["id"])
	}
	if reasoningItem["encrypted_content"] != "opaque-reasoning-blob-turn-1" {
		t.Fatalf("request 2: expected reasoning encrypted_content preserved, got %v", reasoningItem["encrypted_content"])
	}

	fnCallItem := upstreamInput[2].(map[string]any)
	if fnCallItem["call_id"] != "call_1" {
		t.Fatalf("request 2: expected call_id preserved, got %v", fnCallItem["call_id"])
	}
	if fnCallItem["caller"] != "codex-agent" {
		t.Fatalf("request 2: expected the caller field preserved (via Extra), got %v", fnCallItem["caller"])
	}

	fnOutputItem := upstreamInput[3].(map[string]any)
	if fnOutputItem["call_id"] != "call_1" || fnOutputItem["output"] != "72F and sunny" {
		t.Fatalf("request 2: unexpected function_call_output: %v", fnOutputItem)
	}
}
