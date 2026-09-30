package provider

import (
	"encoding/json"
	"net/http"
	"testing"
)

// This file reproduces and proves the fix for the next real Codex VS Code
// E2E defect: the live tool loop progressed all the way to a tool-result
// replay request, and HarnessMesh rejected it with:
//
//	invalid request body: input: expected string or array of input
//	items: input item (custom_tool_call_output): "output" must be a
//	string: json: cannot unmarshal array into Go value of type string
//
// This is a real, confirmed defect - the current Codex client encodes
// function_call_output/custom_tool_call_output's "output" field as either
// a plain string OR an array of content items (e.g.
// {"type":"input_text","text":"..."}), not string-only. The fix
// (CallOutput in wire.go) models this as a lossless union, never
// flattening an array into a string.

// realCustomToolCallOutputFixture is the REDACTED structural shape of the
// exact real item captured in a local Codex session rollout
// (~/.codex-harnessmesh/sessions/2026/09/30/rollout-2026-09-30T23-19-52-...
// .jsonl, "response_item" entry with payload.type=="custom_tool_call_output",
// immediately followed by a task_complete event whose error field is
// exactly the rejection message above). The real id/call_id formats,
// two-content-item output array shape, and the extra
// "internal_chat_message_metadata_passthrough" field the real item also
// carried are preserved exactly; the actual script/file output text
// (which included real repository file contents) has been replaced with
// deterministic, harmless fixture text.
const realCustomToolCallOutputFixture = `{
	"type": "custom_tool_call_output",
	"id": "ctco_test0000-0000-0000-0000-000000000000",
	"call_id": "call_test0000000000000000000",
	"output": [
		{"type": "input_text", "text": "Script completed\nWall time 0.1 seconds\nOutput:\n"},
		{"type": "input_text", "text": "fixture line one\nfixture line two\n"}
	],
	"internal_chat_message_metadata_passthrough": {
		"turn_id": "test-turn-id-0000",
		"create_time": 1700000000.0
	}
}`

// TestCallOutput_RealFixture_Parses proves the exact redacted real fixture
// parses successfully with the production parser (it must no longer
// produce a string-unmarshal error).
func TestCallOutput_RealFixture_Parses(t *testing.T) {
	var it InputItem
	if err := json.Unmarshal([]byte(realCustomToolCallOutputFixture), &it); err != nil {
		t.Fatalf("expected the real custom_tool_call_output fixture to parse successfully, got: %v", err)
	}
	if it.Type != "custom_tool_call_output" || it.CallID != "call_test0000000000000000000" {
		t.Fatalf("unexpected parsed item: %+v", it)
	}
	if it.Output.IsText() {
		t.Fatalf("expected the output to be the array form, not text")
	}
	if len(it.Output.Items()) != 2 {
		t.Fatalf("expected 2 output content items, got %d", len(it.Output.Items()))
	}
	if _, ok := it.Extra["internal_chat_message_metadata_passthrough"]; !ok {
		t.Fatalf("expected the real item's extra metadata field preserved via Extra")
	}
}

// --- Mission 1/2 unit tests: the string-only decoder is gone. ---

// A. custom_tool_call_output with string output.
func TestCallOutput_A_CustomToolCallOutput_StringOutput_Accepted(t *testing.T) {
	src := `{"type":"custom_tool_call_output","call_id":"call_1","output":"hello"}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !it.Output.IsText() || it.Output.Text() != "hello" {
		t.Fatalf("expected text output=hello, got %+v", it.Output)
	}
}

// B. custom_tool_call_output with text-content array.
func TestCallOutput_B_CustomToolCallOutput_ContentArray_Accepted(t *testing.T) {
	src := `{"type":"custom_tool_call_output","call_id":"call_2","output":[{"type":"input_text","text":"hello"}]}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.Output.IsText() {
		t.Fatalf("expected array output, got text")
	}
	items := it.Output.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(items))
	}
	var part map[string]any
	if err := json.Unmarshal(items[0], &part); err != nil {
		t.Fatalf("unmarshal item: %v", err)
	}
	if part["type"] != "input_text" || part["text"] != "hello" {
		t.Fatalf("expected input_text/hello, got %v", part)
	}
}

// C. function_call_output with string output.
func TestCallOutput_C_FunctionCallOutput_StringOutput_Accepted(t *testing.T) {
	src := `{"type":"function_call_output","call_id":"call_3","output":"72F and sunny"}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !it.Output.IsText() || it.Output.Text() != "72F and sunny" {
		t.Fatalf("expected text output, got %+v", it.Output)
	}
}

// D. function_call_output with content array.
func TestCallOutput_D_FunctionCallOutput_ContentArray_Accepted(t *testing.T) {
	src := `{"type":"function_call_output","call_id":"call_4","output":[{"type":"input_text","text":"72F and sunny"}]}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.Output.IsText() {
		t.Fatalf("expected array output, got text")
	}
	if len(it.Output.Items()) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(it.Output.Items()))
	}
}

// E. mixed content array (text + a non-text content item, e.g. an image
// reference) - preserved losslessly, not flattened or rejected.
func TestCallOutput_E_MixedContentArray_Preserved(t *testing.T) {
	src := `{"type":"custom_tool_call_output","call_id":"call_5","output":[
		{"type":"input_text","text":"here is the screenshot"},
		{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}
	]}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	items := it.Output.Items()
	if len(items) != 2 {
		t.Fatalf("expected 2 content items, got %d", len(items))
	}
	var img map[string]any
	if err := json.Unmarshal(items[1], &img); err != nil {
		t.Fatalf("unmarshal image item: %v", err)
	}
	if img["type"] != "input_image" || img["image_url"] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("expected the image content item preserved with its own fields, got %v", img)
	}
	// PlainText must not silently lose the fact there was a non-text item
	// by inventing text for it - it should only surface the real "text"
	// fields, proving no normalization/flattening replaced the image item.
	if it.Output.PlainText() != "here is the screenshot" {
		t.Fatalf("expected PlainText to reflect only the text-bearing item, got %q", it.Output.PlainText())
	}
}

// F. invalid output primitives (number, boolean) must be rejected with a
// typed, actionable error - never silently accepted.
func TestCallOutput_F_InvalidPrimitives_Rejected(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"number", `{"type":"function_call_output","call_id":"c1","output":42}`},
		{"boolean", `{"type":"function_call_output","call_id":"c1","output":true}`},
		{"null", `{"type":"function_call_output","call_id":"c1","output":null}`},
		{"array_of_primitives", `{"type":"custom_tool_call_output","call_id":"c1","output":[1,2,3]}`},
		{"array_item_missing_type", `{"type":"custom_tool_call_output","call_id":"c1","output":[{"text":"no type"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var it InputItem
			err := json.Unmarshal([]byte(tc.src), &it)
			if err == nil {
				t.Fatalf("expected invalid output primitive to be rejected, parsed successfully: %+v", it)
			}
			if _, ok := err.(*MalformedInputItemError); !ok {
				t.Fatalf("expected *MalformedInputItemError, got %T: %v", err, err)
			}
		})
	}
}

// Missing "output" entirely is also malformed (it is a required field).
func TestCallOutput_MissingOutput_Rejected(t *testing.T) {
	for _, typ := range []string{"function_call_output", "custom_tool_call_output"} {
		t.Run(typ, func(t *testing.T) {
			var it InputItem
			err := json.Unmarshal([]byte(`{"type":"`+typ+`","call_id":"c1"}`), &it)
			if err == nil {
				t.Fatalf("expected missing output to be rejected")
			}
			if _, ok := err.(*MalformedInputItemError); !ok {
				t.Fatalf("expected *MalformedInputItemError, got %T: %v", err, err)
			}
		})
	}
}

// --- Round-trip / no-flattening proof. ---

func TestCallOutput_ArrayNeverFlattenedToString(t *testing.T) {
	src := `{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":"hello"}]}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	outputArr, ok := back["output"].([]any)
	if !ok {
		t.Fatalf(`expected "output" to remain a JSON array after round-trip (never flattened to a string), got %v (%T)`, back["output"], back["output"])
	}
	if len(outputArr) != 1 {
		t.Fatalf("expected 1 item preserved, got %d", len(outputArr))
	}
	part := outputArr[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "hello" {
		t.Fatalf("expected the content item preserved verbatim, got %v", part)
	}
}

// --- Mission 7: full two-turn replay through the complete HTTP path. ---

// TestCallOutput_TwoTurnReplay_FullHTTPPath drives turn 1 (a message that
// would, on a real account, produce a custom_tool_call), then turn 2
// replaying the full history - including a custom_tool_call and this
// fixture's array-shaped custom_tool_call_output - through the full
// HarnessMesh HTTP -> normalize -> SubscriptionBackend path, and asserts
// the replay is accepted and forwarded losslessly.
func TestCallOutput_TwoTurnReplay_FullHTTPPath(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	// Turn 1: a plain first turn (in a real account this would produce a
	// custom_tool_call - simulated directly in turn 2's replay below,
	// matching how Codex constructs its own replay history).
	turn1Body := `{"model":"gpt-5.6-luna","stream":true,"input":[{"role":"user","content":"run the version script"}]}`
	rec1 := doRawProviderReq(t, handler, "test-provider-token", turn1Body)
	if rec1.Code != http.StatusOK {
		t.Fatalf("turn 1: expected 200, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// Turn 2: replay full history including the custom_tool_call and its
	// array-shaped output (the real captured fixture), plus new context.
	turn2Input := `[
		{"role":"user","content":"run the version script"},
		{"type":"custom_tool_call","id":"ctc_1","call_id":"call_test0000000000000000000","name":"run_script","input":"print(version())"},
		` + realCustomToolCallOutputFixture + `,
		{"role":"user","content":"thanks, what version is it?"}
	]`
	turn2Body := `{"model":"gpt-5.6-luna","stream":true,"input":` + turn2Input + `}`
	rec2 := doRawProviderReq(t, handler, "test-provider-token", turn2Body)
	if rec2.Code != http.StatusOK {
		t.Fatalf("turn 2 (the replay): expected 200 (accepted), got %d: %s", rec2.Code, rec2.Body.String())
	}

	upstream := fakeResponses.LastBody()
	assertValidResponsesAPIJSON(t, upstream)
	assertNoEmptyType(t, upstream)

	var upstreamReq map[string]any
	if err := json.Unmarshal(upstream, &upstreamReq); err != nil {
		t.Fatalf("upstream body not valid JSON: %v", err)
	}
	inputArr, ok := upstreamReq["input"].([]any)
	if !ok || len(inputArr) != 4 {
		t.Fatalf("expected all 4 replayed items forwarded, got %v", upstreamReq["input"])
	}
	wantOrder := []string{"message", "custom_tool_call", "custom_tool_call_output", "message"}
	for i, want := range wantOrder {
		item := inputArr[i].(map[string]any)
		if item["type"] != want {
			t.Fatalf("item %d: expected type=%q, got %v", i, want, item["type"])
		}
	}

	outputItem := inputArr[2].(map[string]any)
	if outputItem["call_id"] != "call_test0000000000000000000" {
		t.Fatalf("expected call_id preserved, got %v", outputItem["call_id"])
	}
	outputArr, ok := outputItem["output"].([]any)
	if !ok || len(outputArr) != 2 {
		t.Fatalf("expected the output array (2 items) forwarded unflattened, got %v", outputItem["output"])
	}
	if outputArr[0].(map[string]any)["text"] != "Script completed\nWall time 0.1 seconds\nOutput:\n" {
		t.Fatalf("expected the first output content item preserved verbatim, got %v", outputArr[0])
	}
	meta, ok := outputItem["internal_chat_message_metadata_passthrough"].(map[string]any)
	if !ok || meta["turn_id"] != "test-turn-id-0000" {
		t.Fatalf("expected the real item's extra metadata field forwarded upstream, got %v", outputItem["internal_chat_message_metadata_passthrough"])
	}
}
