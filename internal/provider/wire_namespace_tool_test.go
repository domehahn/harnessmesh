package provider

import (
	"encoding/json"
	"net/http"
	"testing"
)

// This file reproduces and proves the fix for a real-account SIWC defect:
// a namespace tool (additional_tools.tools[].type == "namespace") wrapping
// a nested function tool was rejected locally with
// UnsupportedSIWCCapabilityError, even though a request containing exactly
// that shape was sent DIRECTLY to the real OpenAI SIWC /v1/responses
// endpoint and was accepted: the namespace and its nested function were
// echoed back in the response's tools, the model returned the requested
// literal text, and the stream reached a terminal response.completed with
// monotonic sequence numbers 0..13. "namespace" is therefore a REAL,
// SIWC-supported tool type - validated here by recursively checking that
// every tool nested inside it is itself SIWC-supported, never by treating
// "namespace" as a hosted capability in its own right.

// realNamespaceFixture is the exact proven-real request from the SIWC
// account test: additional_tools containing one namespace tool
// ("test_namespace") wrapping one nested function tool ("noop"), followed
// by a plain user message.
const realNamespaceFixture = `{
	"model": "gpt-5.6-luna",
	"instructions": "Follow the user instruction exactly.",
	"input": [
		{
			"type": "additional_tools",
			"role": "developer",
			"tools": [
				{
					"type": "namespace",
					"name": "test_namespace",
					"description": "A namespace used only for compatibility testing.",
					"tools": [
						{
							"type": "function",
							"name": "noop",
							"description": "A no-op test function.",
							"parameters": {
								"type": "object",
								"properties": {},
								"additionalProperties": false
							}
						}
					]
				}
			]
		},
		{
			"role": "user",
			"content": "Reply exactly NAMESPACE_SIWC_OK. Do not call any tool."
		}
	],
	"store": false,
	"stream": true
}`

// --- Recursive test matrix ---

// 1. namespace -> function => ACCEPT.
func TestNamespaceTool_1_NamespaceWrappingFunction_Accepted(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[
		{"type":"namespace","name":"ns","tools":[{"type":"function","name":"f"}]}
	]}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("expected namespace wrapping a function to be accepted, got: %v", err)
	}
	if len(it.Tools) != 1 {
		t.Fatalf("expected 1 top-level tool, got %d", len(it.Tools))
	}
}

// 2. namespace -> multiple functions => ACCEPT.
func TestNamespaceTool_2_NamespaceWrappingMultipleFunctions_Accepted(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[
		{"type":"namespace","name":"ns","tools":[
			{"type":"function","name":"f1"},
			{"type":"function","name":"f2"},
			{"type":"function","name":"f3"}
		]}
	]}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("expected namespace wrapping multiple functions to be accepted, got: %v", err)
	}
}

// 3. namespace -> custom => ACCEPT (custom tools are SIWC-supported).
func TestNamespaceTool_3_NamespaceWrappingCustom_Accepted(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[
		{"type":"namespace","name":"ns","tools":[{"type":"custom","name":"code_exec"}]}
	]}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("expected namespace wrapping a custom tool to be accepted, got: %v", err)
	}
}

// 4. namespace -> namespace -> function => ACCEPT (nested namespace
// follows the identical documented shape as the confirmed one level of
// nesting).
func TestNamespaceTool_4_NestedNamespace_Accepted(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[
		{"type":"namespace","name":"outer","tools":[
			{"type":"namespace","name":"inner","tools":[
				{"type":"function","name":"f"}
			]}
		]}
	]}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("expected a namespace nested inside a namespace to be accepted, got: %v", err)
	}
}

// 5. namespace -> unsupported hosted tool => UnsupportedSIWCCapabilityError.
func TestNamespaceTool_5_UnsupportedHostedToolNested_Rejected(t *testing.T) {
	cases := []string{"file_search", "computer_use_preview", "code_interpreter", "mcp", "image_generation", "apply_patch", "local_shell"}
	for _, toolType := range cases {
		t.Run(toolType, func(t *testing.T) {
			src := `{"type":"additional_tools","role":"developer","tools":[
				{"type":"namespace","name":"ns","tools":[{"type":"` + toolType + `","name":"x"}]}
			]}`
			var it InputItem
			err := json.Unmarshal([]byte(src), &it)
			if err == nil {
				t.Fatalf("expected a namespace nesting hosted tool %q to be rejected", toolType)
			}
			if _, ok := err.(*UnsupportedSIWCCapabilityError); !ok {
				t.Fatalf("expected *UnsupportedSIWCCapabilityError for nested %q, got %T: %v", toolType, err, err)
			}
		})
	}
}

// 6. namespace -> unknown tool type => UnsupportedResponsesToolTypeError.
func TestNamespaceTool_6_UnknownToolTypeNested_Rejected(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[
		{"type":"namespace","name":"ns","tools":[{"type":"totally_made_up_tool_type"}]}
	]}`
	var it InputItem
	err := json.Unmarshal([]byte(src), &it)
	if err == nil {
		t.Fatalf("expected a namespace nesting an unknown tool type to be rejected")
	}
	if _, ok := err.(*UnsupportedResponsesToolTypeError); !ok {
		t.Fatalf("expected *UnsupportedResponsesToolTypeError, got %T: %v", err, err)
	}
}

// A genuinely unknown top-level tool type (not nested) must also surface
// as UnsupportedResponsesToolTypeError, distinct from the hosted-capability
// case - this proves the three-way tool-type taxonomy (allowed / hosted-
// unsupported / unknown) at the top level too, not just when nested.
func TestNamespaceTool_UnknownTopLevelToolType_Rejected(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[{"type":"totally_made_up_tool_type"}]}`
	var it InputItem
	err := json.Unmarshal([]byte(src), &it)
	if err == nil {
		t.Fatalf("expected an unknown top-level tool type to be rejected")
	}
	if _, ok := err.(*UnsupportedResponsesToolTypeError); !ok {
		t.Fatalf("expected *UnsupportedResponsesToolTypeError, got %T: %v", err, err)
	}
}

// 7. namespace with nested function containing complex parameters JSON
// Schema => exact lossless preservation.
func TestNamespaceTool_7_ComplexNestedParametersSchema_LosslessRoundTrip(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[
		{"type":"namespace","name":"ns","description":"d","tools":[
			{"type":"function","name":"f","description":"desc","parameters":{
				"type":"object",
				"properties":{
					"city":{"type":"string"},
					"unit":{"type":"string","enum":["c","f"]},
					"nested":{"type":"object","properties":{"deep":{"type":"array","items":{"type":"integer"}}}}
				},
				"required":["city"],
				"additionalProperties":false
			}}
		]}
	]}`
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
	ns := back["tools"].([]any)[0].(map[string]any)
	if ns["name"] != "ns" || ns["description"] != "d" {
		t.Fatalf("expected namespace name/description preserved, got %v", ns)
	}
	fn := ns["tools"].([]any)[0].(map[string]any)
	params := fn["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	nested := props["nested"].(map[string]any)
	nestedProps := nested["properties"].(map[string]any)
	deep := nestedProps["deep"].(map[string]any)
	if deep["type"] != "array" {
		t.Fatalf("expected deeply nested JSON Schema preserved, got %v", deep)
	}
	required := params["required"].([]any)
	if len(required) != 1 || required[0] != "city" {
		t.Fatalf("expected required=[\"city\"] preserved, got %v", params["required"])
	}
	if params["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties=false preserved, got %v", params["additionalProperties"])
	}
}

// 8. additional_tools containing function, namespace, custom => preserve
// original order.
func TestNamespaceTool_8_MixedToolOrder_Preserved(t *testing.T) {
	src := `{"type":"additional_tools","role":"developer","tools":[
		{"type":"function","name":"f1"},
		{"type":"namespace","name":"ns","tools":[{"type":"function","name":"nested_f"}]},
		{"type":"custom","name":"c1"}
	]}`
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
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	wantTypes := []string{"function", "namespace", "custom"}
	for i, want := range wantTypes {
		tool := tools[i].(map[string]any)
		if tool["type"] != want {
			t.Fatalf("tool %d: expected order-preserved type=%q, got %v", i, want, tool["type"])
		}
	}
}

// --- Real regression fixture ---

// TestNamespaceTool_RealFixture_Parses proves the exact proven-real
// request parses successfully with the production parser (it must no
// longer produce UnsupportedSIWCCapabilityError).
func TestNamespaceTool_RealFixture_Parses(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(realNamespaceFixture), &req); err != nil {
		t.Fatalf("expected the real namespace fixture to parse successfully, got: %v", err)
	}
	if len(req.Input) != 2 {
		t.Fatalf("expected 2 input items, got %d", len(req.Input))
	}
	atItem := req.Input[0]
	if atItem.Type != "additional_tools" || len(atItem.Tools) != 1 {
		t.Fatalf("unexpected additional_tools item: %+v", atItem)
	}
}

// TestNamespaceTool_RealFixture_FullHTTPRoundTrip drives the exact
// proven-real request through HTTP /v1/responses -> parser -> SIWC
// capability validation -> normalizer -> SubscriptionBackend -> fake
// upstream, and asserts the captured upstream body contains the namespace
// tool with its nested function intact.
func TestNamespaceTool_RealFixture_FullHTTPRoundTrip(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	rec := doRawProviderReq(t, handler, "test-provider-token", realNamespaceFixture)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (namespace must be accepted), got %d: %s", rec.Code, rec.Body.String())
	}

	upstream := fakeResponses.LastBody()
	assertValidResponsesAPIJSON(t, upstream)
	assertNoEmptyType(t, upstream)

	var upstreamReq map[string]any
	if err := json.Unmarshal(upstream, &upstreamReq); err != nil {
		t.Fatalf("upstream body not valid JSON: %v", err)
	}
	if upstreamReq["instructions"] != "Follow the user instruction exactly." {
		t.Fatalf("expected instructions forwarded, got %v", upstreamReq["instructions"])
	}

	inputArr := upstreamReq["input"].([]any)
	if len(inputArr) != 2 {
		t.Fatalf("expected 2 input items forwarded, got %d", len(inputArr))
	}
	atItem := inputArr[0].(map[string]any)
	if atItem["type"] != "additional_tools" {
		t.Fatalf("expected additional_tools forwarded, got %v", atItem["type"])
	}
	tools := atItem["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool forwarded, got %d", len(tools))
	}
	nsTool := tools[0].(map[string]any)
	if nsTool["type"] != "namespace" {
		t.Fatalf(`expected the tool's type to remain "namespace", got %v`, nsTool["type"])
	}
	if nsTool["name"] != "test_namespace" {
		t.Fatalf("expected namespace name preserved, got %v", nsTool["name"])
	}
	if nsTool["description"] != "A namespace used only for compatibility testing." {
		t.Fatalf("expected namespace description preserved, got %v", nsTool["description"])
	}
	nestedTools := nsTool["tools"].([]any)
	if len(nestedTools) != 1 {
		t.Fatalf("expected 1 nested tool, got %d", len(nestedTools))
	}
	nestedFn := nestedTools[0].(map[string]any)
	if nestedFn["type"] != "function" || nestedFn["name"] != "noop" {
		t.Fatalf("expected the nested function tool preserved, got %v", nestedFn)
	}
}

// --- Codex regression fixture ---

// codexRealShapeWithNamespaceFixture matches the redacted real Codex
// request shape already captured (model/instructions/reasoning/tool_choice/
// stream/store/tools/input; input item types message/reasoning/
// additional_tools; tool types including namespace).
const codexRealShapeWithNamespaceFixture = `{
	"model": "gpt-5.6-luna",
	"instructions": "You are a coding agent operating inside VS Code.",
	"reasoning": {"effort": "medium"},
	"tool_choice": "auto",
	"stream": true,
	"store": false,
	"tools": [{"type": "function", "name": "get_weather"}],
	"input": [
		{"role": "user", "content": "what's the weather in nyc?"},
		{
			"type": "reasoning",
			"id": "rs_1",
			"summary": [{"type": "summary_text", "text": "checking tools"}]
		},
		{
			"type": "additional_tools",
			"role": "developer",
			"tools": [
				{
					"type": "namespace",
					"name": "weather_tools",
					"description": "Weather-related tools.",
					"tools": [
						{"type": "function", "name": "get_forecast", "parameters": {"type": "object", "properties": {}}}
					]
				}
			]
		}
	]
}`

// TestNamespaceTool_CodexRealShape_FullHTTPRoundTrip proves a Codex-
// compatible request combining the already-confirmed real top-level
// shape, input item types, and a namespace tool succeeds end to end.
func TestNamespaceTool_CodexRealShape_FullHTTPRoundTrip(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	rec := doRawProviderReq(t, handler, "test-provider-token", codexRealShapeWithNamespaceFixture)
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
	inputArr := upstreamReq["input"].([]any)
	if len(inputArr) != 3 {
		t.Fatalf("expected 3 input items, got %d", len(inputArr))
	}
	wantOrder := []string{"message", "reasoning", "additional_tools"}
	for i, want := range wantOrder {
		item := inputArr[i].(map[string]any)
		if item["type"] != want {
			t.Fatalf("item %d: expected type=%q, got %v", i, want, item["type"])
		}
	}
	atItem := inputArr[2].(map[string]any)
	nsTool := atItem["tools"].([]any)[0].(map[string]any)
	if nsTool["type"] != "namespace" || nsTool["name"] != "weather_tools" {
		t.Fatalf("expected the namespace tool preserved, got %v", nsTool)
	}
}
