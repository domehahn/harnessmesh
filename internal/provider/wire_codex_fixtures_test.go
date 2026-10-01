package provider

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// This file captures representative input shapes the currently supported
// Codex client actually produces during an agent/tool loop under a
// stateless (store:false) SIWC backend: a user message, an assistant
// function_call echoed back from the previous turn, this turn's
// function_call_output (the tool result), and a reasoning item carried
// forward for continuation. HarnessMesh must accept and correctly forward
// all of them - it must not claim Codex compatibility while only
// supporting plain string input.

// codexToolLoopFixture is the representative multi-item input Codex sends
// when continuing a tool-using conversation: the original user message,
// the reasoning item from the previous assistant turn, the function call
// that turn made, and this turn's tool result.
const codexToolLoopFixture = `{
	"model": "gpt-5.6-luna",
	"store": false,
	"stream": true,
	"input": [
		{
			"type": "message",
			"role": "user",
			"content": [{"type": "input_text", "text": "what's the weather in nyc?"}]
		},
		{
			"type": "reasoning",
			"id": "rs_abc123",
			"summary": [{"type": "summary_text", "text": "I should call get_weather."}],
			"encrypted_content": "opaque-reasoning-blob"
		},
		{
			"type": "function_call",
			"id": "fc_1",
			"call_id": "call_1",
			"name": "get_weather",
			"arguments": "{\"city\":\"nyc\"}",
			"status": "completed"
		},
		{
			"type": "function_call_output",
			"call_id": "call_1",
			"output": "72F and sunny"
		}
	],
	"tools": [
		{"type": "function", "name": "get_weather", "description": "Get the weather for a city"}
	]
}`

func TestCodexFixture_ToolLoop_ParsesAllFourItemTypes(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(codexToolLoopFixture), &req); err != nil {
		t.Fatalf("unmarshal Codex tool-loop fixture: %v", err)
	}
	if len(req.Input) != 4 {
		t.Fatalf("expected 4 input items, got %d", len(req.Input))
	}

	msg, reasoning, fnCall, fnOutput := req.Input[0], req.Input[1], req.Input[2], req.Input[3]

	if msg.Type != "message" || msg.Role != "user" || msg.Content.PlainText() != "what's the weather in nyc?" {
		t.Fatalf("unexpected message item: %+v", msg)
	}
	if reasoning.Type != "reasoning" || reasoning.ID != "rs_abc123" {
		t.Fatalf("unexpected reasoning item: %+v", reasoning)
	}
	if _, ok := reasoning.Extra["summary"]; !ok {
		t.Fatalf("expected reasoning summary payload to be preserved")
	}
	if _, ok := reasoning.Extra["encrypted_content"]; !ok {
		t.Fatalf("expected reasoning encrypted_content to be preserved")
	}
	if fnCall.Type != "function_call" || fnCall.CallID != "call_1" || fnCall.Name != "get_weather" || fnCall.Arguments != `{"city":"nyc"}` {
		t.Fatalf("unexpected function_call item: %+v", fnCall)
	}
	if fnOutput.Type != "function_call_output" || fnOutput.CallID != "call_1" || fnOutput.Output.PlainText() != "72F and sunny" {
		t.Fatalf("unexpected function_call_output item: %+v", fnOutput)
	}

	// Round-trip: forwarded correctly, never with a serialized "type":"".
	out, err := json.Marshal(req.Input)
	if err != nil {
		t.Fatalf("marshal round-trip: %v", err)
	}
	if bytes.Contains(out, []byte(`"type":""`)) {
		t.Fatalf(`round-tripped Codex fixture must never contain "type":"", got: %s`, out)
	}
	var items []map[string]any
	if err := json.Unmarshal(out, &items); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	wantTypes := []string{"message", "reasoning", "function_call", "function_call_output"}
	for i, want := range wantTypes {
		if items[i]["type"] != want {
			t.Fatalf("item %d: expected type=%q, got %v", i, want, items[i]["type"])
		}
	}
}

// TestCodexFixture_ToolLoop_FullSuccessfulContinuation drives the full HTTP
// stack (server -> SubscriptionBackend -> captured upstream request) with
// the tool-loop fixture, proving a function/tool continuation round trip
// succeeds end to end and the upstream request is valid.
func TestCodexFixture_ToolLoop_FullSuccessfulContinuation(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	rec := doRawProviderReq(t, handler, "test-provider-token", codexToolLoopFixture)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("response.completed")) {
		t.Fatalf("expected the SSE stream to reach response.completed, got: %s", rec.Body.String())
	}

	upstream := fakeResponses.LastBody()
	assertValidResponsesAPIJSON(t, upstream)
	assertNoEmptyType(t, upstream)

	var upstreamReq map[string]any
	if err := json.Unmarshal(upstream, &upstreamReq); err != nil {
		t.Fatalf("upstream body is not valid JSON: %v", err)
	}
	inputArr, ok := upstreamReq["input"].([]any)
	if !ok || len(inputArr) != 4 {
		t.Fatalf("expected all 4 input items forwarded upstream, got %v", upstreamReq["input"])
	}
	for i, want := range []string{"message", "reasoning", "function_call", "function_call_output"} {
		item := inputArr[i].(map[string]any)
		if item["type"] != want {
			t.Fatalf("upstream item %d: expected type=%q, got %v", i, want, item["type"])
		}
	}
	if _, ok := upstreamReq["tools"]; !ok {
		t.Fatalf("expected tools to be forwarded upstream")
	}
}

// TestCodexFixture_UnknownItemType_Rejected proves HarnessMesh does not
// silently accept an item type the current Codex contract is not known to
// send and this package does not claim to support.
func TestCodexFixture_UnknownItemType_Rejected(t *testing.T) {
	body := `{"model":"x","input":[{"type":"multi_agent_call","agents":["a","b"]}]}`
	var req Request
	err := json.Unmarshal([]byte(body), &req)
	if err == nil {
		t.Fatalf("expected an unsupported item type to be rejected, not silently accepted")
	}
	if _, ok := err.(*UnsupportedResponsesInputItemTypeError); !ok {
		// UnmarshalJSON on InputItems may wrap other errors, but an
		// unsupported-type error must always surface as this exact type.
		t.Fatalf("expected *UnsupportedResponsesInputItemTypeError, got %T: %v", err, err)
	}
}

// TestCodexFixture_HostedItemType_RejectedAsUnsupportedSIWCCapability
// proves a real, documented Responses item type SIWC's preview-limitations
// page explicitly lists as unsupported ("native computer use") is rejected
// with the specific UnsupportedSIWCCapabilityError, not conflated with a
// genuinely unknown/non-schema type.
func TestCodexFixture_HostedItemType_RejectedAsUnsupportedSIWCCapability(t *testing.T) {
	body := `{"model":"x","input":[{"type":"computer_call","status":"completed"}]}`
	var req Request
	err := json.Unmarshal([]byte(body), &req)
	if err == nil {
		t.Fatalf("expected a hosted computer_call item to be rejected, not silently accepted")
	}
	if _, ok := err.(*UnsupportedSIWCCapabilityError); !ok {
		t.Fatalf("expected *UnsupportedSIWCCapabilityError, got %T: %v", err, err)
	}
}
