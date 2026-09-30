package provider

import (
	"encoding/json"
	"net/http"
	"testing"
)

// This file covers the re-audit's required replay/continuation fixtures:
// under store:false, OpenAI's documented pattern is "preserve every output
// item, append the next user message, and replay the complete history"
// (developers.openai.com/api/docs/guides/reasoning.md, fetched 2026-09-30).
// HarnessMesh's InputItem layer must accept these replayed items, not only
// first-turn user messages.
//
// Each fixture below is classified against SIWC's preview-limitations page
// (developers.openai.com/siwc/token-sharing-open-source/preview-limitations,
// fetched 2026-09-30):
//   - reasoning + function_call + function_call_output: SIWC-supported,
//     forwarded losslessly (function/custom tools are explicitly listed
//     under "Supported").
//   - apply_patch_call / apply_patch_call_output: a REAL, documented item
//     type (developers.openai.com/api/docs/guides/tools-apply-patch.md)
//     but explicitly named under "Unsupported": "Apply patch, local shell,
//     and other specialized execution tools". Correctly rejected with
//     UnsupportedSIWCCapabilityError, not silently allowed merely because
//     it executes client-side.
//   - local_shell_call / local_shell_call_output: same - a REAL,
//     documented item type (developers.openai.com/api/docs/guides/
//     tools-local-shell.md) explicitly named unsupported by the same
//     sentence.
//   - custom_tool_call / custom_tool_call_output: SIWC-supported (the
//     preview-limitations page's "Supported" list names "function/custom
//     tools" directly) - forwarded losslessly.
//   - multi_agent_call / agent_message: NOT found anywhere in the fetched
//     official documentation as distinct item types. The Codex config
//     reference documents "multi-agent collaboration tools" (spawn_agent,
//     send_input, resume_agent, wait_agent, close_agent) as ordinary named
//     tools - i.e. they are expected to surface as function_call/
//     function_call_output items, already supported, not a bespoke item
//     type. Separately, SIWC's preview-limitations page states the
//     top-level "multi_agent" REQUEST PARAMETER must be omitted - a
//     request-level concern, not an input-item concern, and out of this
//     file's scope. Classified per matrix below; not implemented as a
//     distinct item type since its existence could not be confirmed.
//   - program / program_output: not found anywhere in the fetched
//     documentation. Not implemented.

// TestReplay_StatelessReasoningToolLoop_TurnTwoInput proves the documented
// store:false continuation pattern: turn 2's input replays turn 1's
// reasoning item and function_call, adds this turn's function_call_output,
// and appends new user context - and that the exact replay body reaches
// SIWC unchanged where supported.
func TestReplay_StatelessReasoningToolLoop_TurnTwoInput(t *testing.T) {
	turnTwoInput := `[
		{
			"type": "message",
			"role": "user",
			"content": [{"type": "input_text", "text": "what's the weather in nyc?"}]
		},
		{
			"type": "reasoning",
			"id": "rs_turn1",
			"summary": [{"type": "summary_text", "text": "I should call get_weather."}],
			"encrypted_content": "opaque-reasoning-blob-from-turn-1"
		},
		{
			"type": "function_call",
			"id": "fc_turn1",
			"call_id": "call_1",
			"name": "get_weather",
			"arguments": "{\"city\":\"nyc\"}"
		},
		{
			"type": "function_call_output",
			"call_id": "call_1",
			"output": "72F and sunny"
		},
		{
			"type": "message",
			"role": "user",
			"content": "thanks, what about tomorrow?"
		}
	]`

	var items []InputItem
	if err := json.Unmarshal([]byte(turnTwoInput), &items); err != nil {
		t.Fatalf("unmarshal turn-2 replay input: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("expected 5 items, got %d", len(items))
	}
	wantTypes := []string{"message", "reasoning", "function_call", "function_call_output", "message"}
	for i, want := range wantTypes {
		if items[i].Type != want {
			t.Fatalf("item %d: expected type=%q, got %q", i, want, items[i].Type)
		}
	}
	if items[1].ID != "rs_turn1" {
		t.Fatalf("expected the replayed reasoning item's id preserved, got %q", items[1].ID)
	}
	if _, ok := items[1].Extra["encrypted_content"]; !ok {
		t.Fatalf("expected the replayed reasoning item's encrypted_content preserved")
	}
	if items[2].CallID != "call_1" || items[2].Arguments != `{"city":"nyc"}` {
		t.Fatalf("unexpected replayed function_call: %+v", items[2])
	}

	// Full HTTP -> backend -> captured-upstream round trip: the exact
	// replay body must reach SIWC unchanged.
	handler, fakeResponses := newSIWCTestServer(t)
	body := `{"model":"gpt-5.6-luna","store":false,"stream":true,"input":` + turnTwoInput + `}`
	rec := doRawProviderReq(t, handler, "test-provider-token", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	upstream := fakeResponses.LastBody()
	assertValidResponsesAPIJSON(t, upstream)
	assertNoEmptyType(t, upstream)

	var upstreamReq map[string]any
	if err := json.Unmarshal(upstream, &upstreamReq); err != nil {
		t.Fatalf("upstream body not valid JSON: %v", err)
	}
	upstreamInput := upstreamReq["input"].([]any)
	if len(upstreamInput) != 5 {
		t.Fatalf("expected all 5 replayed items forwarded, got %d", len(upstreamInput))
	}
	for i, want := range wantTypes {
		item := upstreamInput[i].(map[string]any)
		if item["type"] != want {
			t.Fatalf("upstream item %d: expected type=%q, got %v", i, want, item["type"])
		}
	}
	reasoningItem := upstreamInput[1].(map[string]any)
	if reasoningItem["encrypted_content"] != "opaque-reasoning-blob-from-turn-1" {
		t.Fatalf("expected reasoning encrypted_content to reach SIWC unchanged, got %v", reasoningItem["encrypted_content"])
	}
}

// TestReplay_ApplyPatchContinuation_Rejected proves apply_patch_call/
// apply_patch_call_output are recognized as real Responses item types
// (not silently mis-rejected as unknown) but correctly refused as an
// UnsupportedSIWCCapability per the preview-limitations page's explicit
// "Apply patch, local shell, and other specialized execution tools" text.
func TestReplay_ApplyPatchContinuation_Rejected(t *testing.T) {
	cases := []string{
		`{"id":"apc_1","type":"apply_patch_call","status":"completed","call_id":"call_ap1","operation":{"type":"update_file","diff":"...","path":"lib/fib.py"}}`,
		`{"type":"apply_patch_call_output","call_id":"call_ap1","status":"failed","output":"could not apply patch"}`,
	}
	for _, src := range cases {
		var it InputItem
		err := json.Unmarshal([]byte(src), &it)
		if err == nil {
			t.Fatalf("expected apply_patch item to be rejected under SIWC, parsed successfully: %+v", it)
		}
		if _, ok := err.(*UnsupportedSIWCCapabilityError); !ok {
			t.Fatalf("expected *UnsupportedSIWCCapabilityError, got %T: %v", err, err)
		}
	}

	// Full HTTP path: the request must be refused (not silently forwarded
	// with the apply_patch item stripped, and not forwarded at all).
	handler, fakeResponses := newSIWCTestServer(t)
	body := `{"model":"x","input":[
		{"type":"message","role":"user","content":"apply this patch"},
		{"id":"apc_1","type":"apply_patch_call","status":"completed","call_id":"call_ap1","operation":{"type":"update_file","diff":"...","path":"lib/fib.py"}}
	]}`
	rec := doRawProviderReq(t, handler, "test-provider-token", body)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected apply_patch_call to be refused locally, got 200: %s", rec.Body.String())
	}
	if len(fakeResponses.LastBody()) != 0 {
		t.Fatalf("expected the request to never reach the upstream SIWC endpoint, but it did: %s", fakeResponses.LastBody())
	}
}

// TestReplay_LocalShellContinuation_Rejected proves local_shell_call/
// local_shell_call_output are recognized real item types but correctly
// refused under SIWC's preview-limitations text.
func TestReplay_LocalShellContinuation_Rejected(t *testing.T) {
	cases := []string{
		`{"type":"local_shell_call","call_id":"call_sh1","action":{"command":["ls","-la"],"working_directory":"/repo"}}`,
		`{"type":"local_shell_call_output","id":"call_sh1","output":"total 0\n"}`,
	}
	for _, src := range cases {
		var it InputItem
		err := json.Unmarshal([]byte(src), &it)
		if err == nil {
			t.Fatalf("expected local_shell item to be rejected under SIWC, parsed successfully: %+v", it)
		}
		if _, ok := err.(*UnsupportedSIWCCapabilityError); !ok {
			t.Fatalf("expected *UnsupportedSIWCCapabilityError, got %T: %v", err, err)
		}
	}
}

// TestReplay_CustomToolContinuation proves custom_tool_call/
// custom_tool_call_output - a SIWC-supported, client-executed mechanism
// distinct from apply_patch/local_shell - are forwarded losslessly end to
// end, including the real documented example's exact fields.
func TestReplay_CustomToolContinuation(t *testing.T) {
	input := `[
		{"type":"message","role":"user","content":"run this: print('hello world')"},
		{"id":"ctc_6890e975e86c819c9338825b3e1994810694874912ae0ea6","type":"custom_tool_call","status":"completed","call_id":"call_aGiFQkRWSWAIsMQ19fKqxUgb","input":"print(\"hello world\")","name":"code_exec"},
		{"type":"custom_tool_call_output","call_id":"call_aGiFQkRWSWAIsMQ19fKqxUgb","output":"hello world\n"}
	]`
	var items []InputItem
	if err := json.Unmarshal([]byte(input), &items); err != nil {
		t.Fatalf("expected custom_tool_call items to be accepted, got: %v", err)
	}
	if items[1].Type != "custom_tool_call" || items[1].Input != `print("hello world")` || items[1].Name != "code_exec" {
		t.Fatalf("unexpected custom_tool_call item: %+v", items[1])
	}
	if items[2].Type != "custom_tool_call_output" || items[2].Output != "hello world\n" {
		t.Fatalf("unexpected custom_tool_call_output item: %+v", items[2])
	}

	handler, fakeResponses := newSIWCTestServer(t)
	body := `{"model":"x","store":false,"stream":true,"input":` + input + `}`
	rec := doRawProviderReq(t, handler, "test-provider-token", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a SIWC-supported custom tool continuation, got %d: %s", rec.Code, rec.Body.String())
	}
	upstream := fakeResponses.LastBody()
	assertNoEmptyType(t, upstream)
	var upstreamReq map[string]any
	if err := json.Unmarshal(upstream, &upstreamReq); err != nil {
		t.Fatalf("upstream body not valid JSON: %v", err)
	}
	upstreamInput := upstreamReq["input"].([]any)
	callItem := upstreamInput[1].(map[string]any)
	if callItem["type"] != "custom_tool_call" || callItem["input"] != `print("hello world")` {
		t.Fatalf("expected custom_tool_call forwarded verbatim, got %v", callItem)
	}
}

// TestReplay_ProgramItems_NotImplemented documents that "program"/
// "program_output" could not be confirmed as real Responses item types in
// the fetched official documentation, and are therefore NOT implemented -
// they remain rejected as a genuinely unknown type, per "do not invent
// unsupported types."
func TestReplay_ProgramItems_NotImplemented(t *testing.T) {
	for _, typ := range []string{"program", "program_output"} {
		t.Run(typ, func(t *testing.T) {
			var it InputItem
			err := json.Unmarshal([]byte(`{"type":"`+typ+`"}`), &it)
			if err == nil {
				t.Fatalf("expected %q to be rejected (not confirmed in official docs), parsed successfully: %+v", typ, it)
			}
			if _, ok := err.(*UnsupportedResponsesInputItemTypeError); !ok {
				t.Fatalf("expected *UnsupportedResponsesInputItemTypeError for unconfirmed type %q, got %T: %v", typ, err, err)
			}
		})
	}
}

// TestReplay_MultiAgentCallItems_NotImplemented documents that
// "multi_agent_call"/"multi_agent_call_output"/"agent_message" could not
// be confirmed as distinct Responses item types. The Codex config
// reference documents "multi-agent collaboration tools" (spawn_agent,
// send_input, resume_agent, wait_agent, close_agent) as ordinary named
// tools, which are expected to surface through the already-supported
// function_call/function_call_output item types - not a bespoke item type
// this package must separately implement. They remain rejected as
// genuinely unknown item types if ever sent literally.
func TestReplay_MultiAgentCallItems_NotImplemented(t *testing.T) {
	for _, typ := range []string{"multi_agent_call", "multi_agent_call_output", "agent_message"} {
		t.Run(typ, func(t *testing.T) {
			var it InputItem
			err := json.Unmarshal([]byte(`{"type":"`+typ+`"}`), &it)
			if err == nil {
				t.Fatalf("expected %q to be rejected (not confirmed in official docs as a distinct item type), parsed successfully: %+v", typ, it)
			}
			if _, ok := err.(*UnsupportedResponsesInputItemTypeError); !ok {
				t.Fatalf("expected *UnsupportedResponsesInputItemTypeError for unconfirmed type %q, got %T: %v", typ, err, err)
			}
		})
	}
}

// TestReplay_MultiAgentTools_ViaFunctionCall proves the actual, confirmed
// mechanism: multi-agent collaboration tools (spawn_agent, etc.) work
// through the ordinary, already-supported function_call/
// function_call_output items - no special-casing needed.
func TestReplay_MultiAgentTools_ViaFunctionCall(t *testing.T) {
	src := `{"type":"function_call","call_id":"call_1","name":"spawn_agent","arguments":"{\"task\":\"review the diff\"}"}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("expected a spawn_agent function_call to be accepted via the ordinary function_call mechanism, got: %v", err)
	}
	if it.Name != "spawn_agent" {
		t.Fatalf("expected Name=spawn_agent, got %q", it.Name)
	}
}
