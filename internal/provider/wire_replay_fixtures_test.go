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

// TestReplay_ProgramItems_RejectedAsUnsupportedSIWCCapability corrects an
// earlier pass of this audit, which wrongly claimed "program"/
// "program_output" were not confirmed in official documentation. A
// targeted fetch of developers.openai.com/api/docs/guides/
// tools-programmatic-tool-calling.md (2026-09-30) confirmed both are real,
// documented item types (program: {type, id, call_id, code, fingerprint};
// program_output: {type, id, call_id, result, status}) produced by the
// "programmatic tool calling" feature - which SIWC's preview-limitations
// page explicitly lists as unsupported ("programmatic_tool_calling at the
// top level"). They are therefore recognized (not genuinely unknown) but
// rejected as UnsupportedSIWCCapability, not UnsupportedResponsesInputItemType.
func TestReplay_ProgramItems_RejectedAsUnsupportedSIWCCapability(t *testing.T) {
	for _, typ := range []string{"program", "program_output"} {
		t.Run(typ, func(t *testing.T) {
			var it InputItem
			err := json.Unmarshal([]byte(`{"type":"`+typ+`"}`), &it)
			if err == nil {
				t.Fatalf("expected %q to be rejected under SIWC, parsed successfully: %+v", typ, it)
			}
			if _, ok := err.(*UnsupportedSIWCCapabilityError); !ok {
				t.Fatalf("expected *UnsupportedSIWCCapabilityError for %q, got %T: %v", typ, err, err)
			}
		})
	}
}

// TestReplay_MultiAgentItems_RejectedAsUnsupportedSIWCCapability also
// corrects an earlier pass: a targeted fetch of developers.openai.com/api/
// docs/guides/agents-api/multi-agent.md (2026-09-30) confirmed real
// multi-agent item types exist - but NOT named "multi_agent_call"/
// "multi_agent_call_output" (an incorrect guess from that earlier pass).
// The real, confirmed type strings are create_subagent_call,
// send_subagent_input_call, wait_for_subagents_call,
// interrupt_subagent_call, and agent_message. SIWC's preview-limitations
// page states "the multi_agent parameter must be omitted from requests" -
// the top-level parameter that enables this feature at all - so these
// item types are recognized but rejected as UnsupportedSIWCCapability.
func TestReplay_MultiAgentItems_RejectedAsUnsupportedSIWCCapability(t *testing.T) {
	for _, typ := range []string{
		"create_subagent_call", "send_subagent_input_call",
		"wait_for_subagents_call", "interrupt_subagent_call", "agent_message",
	} {
		t.Run(typ, func(t *testing.T) {
			var it InputItem
			err := json.Unmarshal([]byte(`{"type":"`+typ+`"}`), &it)
			if err == nil {
				t.Fatalf("expected %q to be rejected under SIWC, parsed successfully: %+v", typ, it)
			}
			if _, ok := err.(*UnsupportedSIWCCapabilityError); !ok {
				t.Fatalf("expected *UnsupportedSIWCCapabilityError for %q, got %T: %v", typ, err, err)
			}
		})
	}
}

// TestReplay_MultiAgentTools_ViaFunctionCall proves the OTHER, confirmed
// mechanism the Codex config reference documents: "multi-agent
// collaboration tools" (spawn_agent, send_input, resume_agent, wait_agent,
// close_agent) work through the ordinary, already-supported function_call/
// function_call_output items - a separate path from the native
// create_subagent_call/etc. item types above, and one SIWC does support
// (function/custom tools are explicitly permitted).
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

// TestReplay_ShellCallContinuation_Rejected proves shell_call/
// shell_call_output (developers.openai.com/api/docs/guides/tools-shell.md,
// fetched 2026-09-30: distinct from local_shell_call, but documented as
// sharing output item types - "Hosted shell and local shell use the same
// output item types") are recognized but refused under SIWC's "Apply
// patch, local shell, and other specialized execution tools" text.
func TestReplay_ShellCallContinuation_Rejected(t *testing.T) {
	cases := []string{
		`{"type":"shell_call","call_id":"call_sh1","action":{"commands":["ls -l"],"timeout_ms":120000},"status":"in_progress"}`,
		`{"type":"shell_call_output","call_id":"call_sh1","output":[{"stdout":"...","stderr":"","outcome":{"type":"exit","exit_code":0}}]}`,
	}
	for _, src := range cases {
		var it InputItem
		err := json.Unmarshal([]byte(src), &it)
		if err == nil {
			t.Fatalf("expected shell item to be rejected under SIWC, parsed successfully: %+v", it)
		}
		if _, ok := err.(*UnsupportedSIWCCapabilityError); !ok {
			t.Fatalf("expected *UnsupportedSIWCCapabilityError, got %T: %v", err, err)
		}
	}
}

// TestReplay_ConfigurationUpdate_Accepted proves configuration_update
// (developers.openai.com/api/docs/guides/reasoning.md, fetched
// 2026-09-30: {"type":"configuration_update","reasoning":{"effort":"high"}},
// added before the next user message to change reasoning effort
// mid-conversation) is recognized and forwarded losslessly. SIWC status is
// UNVERIFIED (absence from the "Unsupported" list is not proof of
// support) - this test proves HarnessMesh's own parser/forwarder behavior,
// not that OpenAI's real endpoint accepts it.
func TestReplay_ConfigurationUpdate_Accepted(t *testing.T) {
	src := `{"type":"configuration_update","reasoning":{"effort":"high"}}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("expected configuration_update to be accepted, got: %v", err)
	}
	if it.Type != "configuration_update" {
		t.Fatalf("expected type=configuration_update, got %q", it.Type)
	}
	if _, ok := it.Extra["reasoning"]; !ok {
		t.Fatalf("expected the reasoning payload preserved via Extra, got %+v", it.Extra)
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	reasoning, ok := back["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("expected reasoning.effort=high preserved on round-trip, got %v", back["reasoning"])
	}
}

// TestReplay_CompactionTrigger_Accepted proves compaction_trigger
// (developers.openai.com/api/docs/guides/reasoning.md, fetched
// 2026-09-30: "You can still explicitly compact history by including a
// compaction_trigger item in a /responses request") is recognized and
// forwarded. SIWC status is UNVERIFIED (absence from the "Unsupported"
// list is not proof of support) - this test proves HarnessMesh's own
// parser/forwarder behavior, not that OpenAI's real endpoint accepts it.
func TestReplay_CompactionTrigger_Accepted(t *testing.T) {
	src := `{"type":"compaction_trigger"}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("expected compaction_trigger to be accepted, got: %v", err)
	}
	if it.Type != "compaction_trigger" {
		t.Fatalf("expected type=compaction_trigger, got %q", it.Type)
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	assertNoEmptyType(t, out)
}
