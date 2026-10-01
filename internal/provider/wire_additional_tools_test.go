package provider

import (
	"encoding/json"
	"net/http"
	"testing"
)

// This file reproduces and proves the fix for the next real Codex
// interoperability defect: a real Codex VS Code request failed with
// `unsupported input item type "additional_tools"`. additional_tools is a
// real, documented Responses API input item
// (developers.openai.com/api/reference/resources/responses, fetched
// 2026-09-30: "A list of additional tools made available at this item")
// with the shape {"type":"additional_tools","role":"developer","tools":[...]}
// - an INPUT item, not a top-level tools replacement, whose position in
// the input array is semantically significant (the tools become available
// only after that item appears). SIWC's token-sharing-open-source docs
// describe function/custom tools as supported; nothing in the fetched SIWC
// documentation describes hosted tool support, so this package treats
// hosted tool types (inside additional_tools.tools, or as their own
// hosted item types like computer_call/web_search_call/file_search_call/
// tool_search_call/tool_search_output) as an explicit, distinct
// UnsupportedSIWCCapabilityError rather than silently allowing them
// through additional_tools's door.

const realCodexAdditionalToolsFixture = `{
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
			"type": "additional_tools",
			"role": "developer",
			"tools": [
				{
					"type": "function",
					"name": "get_weather",
					"description": "Get the weather for a city",
					"parameters": {
						"type": "object",
						"properties": {
							"city": {"type": "string"}
						},
						"required": ["city"],
						"additionalProperties": false
					}
				}
			]
		},
		{
			"type": "message",
			"role": "user",
			"content": [{"type": "input_text", "text": "go ahead"}]
		}
	]
}`

// A) valid additional_tools with one function.
func TestAdditionalTools_A_SingleFunction_Accepted(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"get_weather"}]}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.Type != "additional_tools" || it.Role != "developer" {
		t.Fatalf("unexpected parsed item: %+v", it)
	}
	if len(it.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(it.Tools))
	}
}

// B) multiple tools.
func TestAdditionalTools_B_MultipleTools_Accepted(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[
		{"type":"function","name":"get_weather"},
		{"type":"function","name":"get_time"},
		{"type":"custom","name":"run_shell"}
	]}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(it.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(it.Tools))
	}
}

// C) nested JSON Schema parameters preserved losslessly.
func TestAdditionalTools_C_NestedJSONSchemaParameters_Preserved(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[{
		"type":"function","name":"get_weather","description":"Get the weather for a city",
		"parameters":{"type":"object","properties":{"city":{"type":"string"},"unit":{"type":"string","enum":["c","f"]}},"required":["city"],"additionalProperties":false}
	}]}`
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
	tools := back["tools"].([]any)
	tool := tools[0].(map[string]any)
	params := tool["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	if _, ok := props["city"]; !ok {
		t.Fatalf("expected nested parameters.properties.city to survive the round-trip, got %v", params)
	}
	required := params["required"].([]any)
	if len(required) != 1 || required[0] != "city" {
		t.Fatalf("expected required=[\"city\"] to survive the round-trip, got %v", params["required"])
	}
	if params["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties=false to survive the round-trip, got %v", params["additionalProperties"])
	}
}

// D) unknown extra fields (both on the item itself and inside a tool
// definition) round-trip losslessly.
func TestAdditionalTools_D_UnknownExtraFields_RoundTrip(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"f","some_future_tool_field":42}],"some_future_item_field":"x"}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := it.Extra["some_future_item_field"]; !ok {
		t.Fatalf("expected an unrecognized item-level field to be preserved in Extra, got %+v", it.Extra)
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if back["some_future_item_field"] != "x" {
		t.Fatalf("expected item-level unknown field to survive, got %v", back["some_future_item_field"])
	}
	tool := back["tools"].([]any)[0].(map[string]any)
	if tool["some_future_tool_field"] != float64(42) {
		t.Fatalf("expected tool-level unknown field to survive, got %v", tool["some_future_tool_field"])
	}
}

// E) correct ordering: message A, additional_tools, message B must be
// forwarded in exactly that order - position is semantically significant
// (the tools become available only after the additional_tools item).
func TestAdditionalTools_E_OrderingAmongMessageItems_Preserved(t *testing.T) {
	src := `[
		{"type":"message","role":"user","content":"message A"},
		{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"f"}]},
		{"type":"message","role":"user","content":"message B"}
	]`
	var items []InputItem
	if err := json.Unmarshal([]byte(src), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].Type != "message" || items[0].Content.PlainText() != "message A" {
		t.Fatalf("item 0: expected message A first, got %+v", items[0])
	}
	if items[1].Type != "additional_tools" {
		t.Fatalf("item 1: expected additional_tools second, got %+v", items[1])
	}
	if items[2].Type != "message" || items[2].Content.PlainText() != "message B" {
		t.Fatalf("item 2: expected message B third, got %+v", items[2])
	}

	out, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back []map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	wantOrder := []string{"message", "additional_tools", "message"}
	for i, want := range wantOrder {
		if back[i]["type"] != want {
			t.Fatalf("item %d: expected type=%q after round-trip, got %v", i, want, back[i]["type"])
		}
	}
}

// F) missing tools is malformed.
func TestAdditionalTools_F_MissingTools_Malformed(t *testing.T) {
	var it InputItem
	err := json.Unmarshal([]byte(`{"type":"additional_tools","role":"developer"}`), &it)
	if err == nil {
		t.Fatalf("expected missing tools to be rejected")
	}
	m, ok := err.(*MalformedInputItemError)
	if !ok {
		t.Fatalf("expected *MalformedInputItemError, got %T: %v", err, err)
	}
	if m.Type != "additional_tools" {
		t.Fatalf("expected Type=additional_tools, got %q", m.Type)
	}
}

// G) malformed tools: not an array, empty array, and a tool definition
// missing its own "type".
func TestAdditionalTools_G_MalformedTools_Rejected(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"tools_not_an_array", `{"type":"additional_tools","role":"developer","tools":"not-an-array"}`},
		{"tools_empty", `{"type":"additional_tools","role":"developer","tools":[]}`},
		{"tool_missing_type", `{"type":"additional_tools","role":"developer","tools":[{"name":"f"}]}`},
		{"tool_not_an_object", `{"type":"additional_tools","role":"developer","tools":[123]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var it InputItem
			err := json.Unmarshal([]byte(tc.src), &it)
			if err == nil {
				t.Fatalf("expected malformed tools to be rejected")
			}
			if _, ok := err.(*MalformedInputItemError); !ok {
				t.Fatalf("expected *MalformedInputItemError, got %T: %v", err, err)
			}
		})
	}
}

// H) wrong role is malformed (the documented shape's role is fixed to
// "developer"; nothing in the fetched documentation permits another
// value).
func TestAdditionalTools_H_WrongRole_Malformed(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"wrong_role", `{"type":"additional_tools","role":"user","tools":[{"type":"function","name":"f"}]}`},
		{"missing_role", `{"type":"additional_tools","tools":[{"type":"function","name":"f"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var it InputItem
			err := json.Unmarshal([]byte(tc.src), &it)
			if err == nil {
				t.Fatalf("expected a non-developer role to be rejected")
			}
			if _, ok := err.(*MalformedInputItemError); !ok {
				t.Fatalf("expected *MalformedInputItemError, got %T: %v", err, err)
			}
		})
	}
}

// I) an unsupported (hosted) SIWC tool type embedded in additional_tools
// is rejected specifically, not silently allowed through additional_tools'
// door.
func TestAdditionalTools_I_UnsupportedSIWCHostedToolType_Rejected(t *testing.T) {
	// web_search is deliberately NOT in this list: SIWC's preview-
	// limitations page explicitly lists "Web search" under Supported
	// (subject to model/account policy) - see TestAdditionalTools_WebSearchTool_Allowed.
	cases := []string{"apply_patch", "local_shell", "computer_use_preview", "file_search", "code_interpreter", "mcp"}
	for _, toolType := range cases {
		t.Run(toolType, func(t *testing.T) {
			src := `{"type":"additional_tools","role":"developer","tools":[{"type":"` + toolType + `","name":"x"}]}`
			var it InputItem
			err := json.Unmarshal([]byte(src), &it)
			if err == nil {
				t.Fatalf("expected hosted tool type %q inside additional_tools to be rejected", toolType)
			}
			if _, ok := err.(*UnsupportedSIWCCapabilityError); !ok {
				t.Fatalf("expected *UnsupportedSIWCCapabilityError, got %T: %v", err, err)
			}
		})
	}
}

// web_search is confirmed SIWC-supported ("subject to model and
// account/workspace policy") per the preview-limitations page, so it must
// be ALLOWED through additional_tools, unlike the hosted/specialized-
// execution tool types above.
func TestAdditionalTools_WebSearchTool_Allowed(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[{"type":"web_search","name":"web_search"}]}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("expected web_search tool type to be allowed inside additional_tools, got: %v", err)
	}
	if len(it.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(it.Tools))
	}
}

// J) exact real Codex fixture that triggered the original defect: parses
// successfully with the production parser.
func TestAdditionalTools_J_RealCodexFixture_Parses(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(realCodexAdditionalToolsFixture), &req); err != nil {
		t.Fatalf("unmarshal real Codex fixture: %v", err)
	}
	if len(req.Input) != 3 {
		t.Fatalf("expected 3 input items, got %d", len(req.Input))
	}
	if req.Input[0].Type != "message" || req.Input[1].Type != "additional_tools" || req.Input[2].Type != "message" {
		t.Fatalf("unexpected item types: %q, %q, %q", req.Input[0].Type, req.Input[1].Type, req.Input[2].Type)
	}
	if req.Input[1].Role != "developer" {
		t.Fatalf("expected additional_tools role=developer, got %q", req.Input[1].Role)
	}
	if len(req.Input[1].Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(req.Input[1].Tools))
	}
}

// L) full HTTP -> backend -> captured-upstream round trip, asserting all
// 10 required properties from the real Codex fixture.
func TestAdditionalTools_L_FullHTTPRoundTrip(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	rec := doRawProviderReq(t, handler, "test-provider-token", realCodexAdditionalToolsFixture)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (additional_tools must be accepted), got %d: %s", rec.Code, rec.Body.String())
	}

	upstream := fakeResponses.LastBody()
	assertValidResponsesAPIJSON(t, upstream)
	assertNoEmptyType(t, upstream) // 7. no field becomes type:""

	var upstreamReq map[string]any
	if err := json.Unmarshal(upstream, &upstreamReq); err != nil {
		t.Fatalf("upstream body is not valid JSON: %v", err)
	}
	inputArr, ok := upstreamReq["input"].([]any)
	if !ok || len(inputArr) != 3 { // 8. no item silently dropped
		t.Fatalf("expected all 3 input items forwarded upstream, got %v", upstreamReq["input"])
	}

	// 6. input ordering preserved.
	wantOrder := []string{"message", "additional_tools", "message"}
	for i, want := range wantOrder {
		item := inputArr[i].(map[string]any)
		if item["type"] != want {
			t.Fatalf("upstream item %d: expected type=%q, got %v", i, want, item["type"])
		}
	}

	atItem := inputArr[1].(map[string]any)
	// 2. type remains exactly "additional_tools".
	if atItem["type"] != "additional_tools" {
		t.Fatalf(`expected upstream item 1 type="additional_tools", got %v`, atItem["type"])
	}
	// 3. role is preserved.
	if atItem["role"] != "developer" {
		t.Fatalf(`expected upstream additional_tools role="developer", got %v`, atItem["role"])
	}
	// 4. all tools preserved.
	tools, ok := atItem["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool forwarded upstream, got %v", atItem["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "get_weather" {
		t.Fatalf("expected the tool's name to be forwarded, got %v", tool["name"])
	}
	// 5. nested parameters JSON preserved.
	params, ok := tool["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested parameters to be forwarded, got %v", tool["parameters"])
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		if !ok {
			t.Fatalf("expected parameters.properties to be forwarded, got %v", params)
		}
	}
	if _, ok := props["city"]; !ok {
		t.Fatalf("expected parameters.properties.city to be forwarded, got %v", props)
	}
	// 9. additional_tools must never be moved to top-level request.tools.
	if _, ok := upstreamReq["tools"]; ok {
		t.Fatalf("additional_tools must never be hoisted to top-level request.tools, but upstream request.tools=%v", upstreamReq["tools"])
	}
	// 1. additional_tools is accepted (implied by rec.Code==200 above) and
	// 10. the outgoing request remains valid for SIWC: store=false,
	// stream=true, matching what SubscriptionBackend always sends.
	if upstreamReq["store"] != false {
		t.Fatalf("expected store=false, got %v", upstreamReq["store"])
	}
	if upstreamReq["stream"] != true {
		t.Fatalf("expected stream=true, got %v", upstreamReq["stream"])
	}
}
