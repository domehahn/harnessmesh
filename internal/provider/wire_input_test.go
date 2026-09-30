package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// This file reproduces and proves the fix for two real-account Responses
// API compatibility defects found immediately after a real, successful
// SIWC inference (HarnessMesh -> chatgpt-subscription -> SIWC OAuth ->
// POST /v1/responses -> gpt-5.6-luna -> SSE streaming -> response.completed):
//
//   1. The documented "easy message" input shape -
//      {"role":"user","content":"Say exactly: Hello, world!"} - was
//      rejected locally because InputItem.Content was modeled solely as
//      []ContentPart, which cannot unmarshal a plain string.
//   2. A structured content-parts message with no explicit "type" field
//      was accepted locally but forwarded upstream with a serialized
//      "type":"" (the Go zero value), which OpenAI rejected outright.

// ---------------------------------------------------------------------
// Content union: string vs content-parts array
// ---------------------------------------------------------------------

func TestContent_UnmarshalString_Accepted(t *testing.T) {
	var c Content
	if err := json.Unmarshal([]byte(`"hello"`), &c); err != nil {
		t.Fatalf("unmarshal string content: %v", err)
	}
	if !c.IsText() {
		t.Fatalf("expected IsText() true for a plain string content")
	}
	if c.Text() != "hello" {
		t.Fatalf("expected Text()=%q, got %q", "hello", c.Text())
	}
	if c.PlainText() != "hello" {
		t.Fatalf("expected PlainText()=%q, got %q", "hello", c.PlainText())
	}
	// Forwarded correctly: re-marshals back to the same string form.
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `"hello"` {
		t.Fatalf("expected content to round-trip as a plain string, got %s", b)
	}
}

func TestContent_UnmarshalContentPartsArray_Accepted(t *testing.T) {
	var c Content
	if err := json.Unmarshal([]byte(`[{"type":"input_text","text":"hello"}]`), &c); err != nil {
		t.Fatalf("unmarshal array content: %v", err)
	}
	if c.IsText() {
		t.Fatalf("expected IsText() false for a content-parts array")
	}
	if len(c.Parts()) != 1 || c.Parts()[0].Text != "hello" {
		t.Fatalf("expected one input_text part with text=hello, got %+v", c.Parts())
	}
	if c.PlainText() != "hello" {
		t.Fatalf("expected PlainText()=%q, got %q", "hello", c.PlainText())
	}
	// Forwarded correctly: re-marshals back to the same array form.
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `[{"type":"input_text","text":"hello"}]` {
		t.Fatalf("expected content to round-trip as a content-parts array, got %s", b)
	}
}

func TestContent_WrongPrimitive_Rejected(t *testing.T) {
	var c Content
	if err := json.Unmarshal([]byte(`123`), &c); err == nil {
		t.Fatalf("expected a numeric content value to be rejected")
	}
}

func TestContent_Null_Rejected(t *testing.T) {
	var c Content
	if err := json.Unmarshal([]byte(`null`), &c); err == nil {
		t.Fatalf("expected a null content value to be rejected")
	}
}

func TestContent_Object_Rejected(t *testing.T) {
	var c Content
	if err := json.Unmarshal([]byte(`{"not":"valid"}`), &c); err == nil {
		t.Fatalf("expected a bare object content value to be rejected")
	}
}

// ---------------------------------------------------------------------
// Type semantics: never emit type:"", correct normalization/preservation
// ---------------------------------------------------------------------

// A) easy message with string content must never become type:"".
func TestInputItem_EasyMessageStringContent_NeverEmitsEmptyType(t *testing.T) {
	var it InputItem
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hello"}`), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.Type != "message" {
		t.Fatalf("expected the easy-message shorthand to normalize to type=message, got %q", it.Type)
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	assertNoEmptyType(t, out)
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if back["type"] != "message" {
		t.Fatalf(`expected serialized "type":"message", got %v`, back["type"])
	}
	if back["content"] != "hello" {
		t.Fatalf(`expected serialized "content":"hello", got %v`, back["content"])
	}
}

// B) easy message with content-parts array must never become type:"" -
// this is the EXACT real-account defect #2.
func TestInputItem_EasyMessagePartsContent_NeverEmitsEmptyType(t *testing.T) {
	var it InputItem
	if err := json.Unmarshal([]byte(`{"role":"user","content":[{"type":"input_text","text":"HARNESSMESH_SIWC_OK"}]}`), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.Type != "message" {
		t.Fatalf("expected normalization to type=message, got %q", it.Type)
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	assertNoEmptyType(t, out)
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if back["type"] != "message" {
		t.Fatalf(`expected serialized "type":"message", got %v`, back["type"])
	}
}

// C) an explicit type:"message" item must preserve it exactly.
func TestInputItem_ExplicitMessageType_Preserved(t *testing.T) {
	var it InputItem
	if err := json.Unmarshal([]byte(`{"type":"message","role":"user","content":"hello"}`), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.Type != "message" {
		t.Fatalf("expected type=message preserved, got %q", it.Type)
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if back["type"] != "message" {
		t.Fatalf(`expected serialized "type":"message", got %v`, back["type"])
	}
}

// D) function_call preserves type/call_id/name/arguments.
func TestInputItem_FunctionCall_PreservesTypeAndFunctionFields(t *testing.T) {
	src := `{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"nyc\"}"}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.Type != "function_call" || it.CallID != "call_1" || it.Name != "get_weather" || it.Arguments != `{"city":"nyc"}` {
		t.Fatalf("unexpected parsed function_call item: %+v", it)
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if back["type"] != "function_call" || back["call_id"] != "call_1" || back["name"] != "get_weather" {
		t.Fatalf("expected function_call fields preserved on round-trip, got %v", back)
	}
}

// E) function_call_output preserves type/call_id/output.
func TestInputItem_FunctionCallOutput_PreservesTypeCallIDOutput(t *testing.T) {
	src := `{"type":"function_call_output","call_id":"call_1","output":"72F and sunny"}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if it.Type != "function_call_output" || it.CallID != "call_1" || it.Output != "72F and sunny" {
		t.Fatalf("unexpected parsed function_call_output item: %+v", it)
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if back["type"] != "function_call_output" || back["call_id"] != "call_1" || back["output"] != "72F and sunny" {
		t.Fatalf("expected function_call_output fields preserved on round-trip, got %v", back)
	}
}

// F) an unknown/unsupported item type fails with a typed compatibility
// error, not silent field loss.
func TestInputItem_UnknownType_RejectedWithTypedError(t *testing.T) {
	var it InputItem
	err := json.Unmarshal([]byte(`{"type":"computer_call","action":{"type":"click"}}`), &it)
	if err == nil {
		t.Fatalf("expected an unknown input item type to be rejected")
	}
	unsupported, ok := err.(*UnsupportedInputItemTypeError)
	if !ok {
		t.Fatalf("expected *UnsupportedInputItemTypeError, got %T: %v", err, err)
	}
	if unsupported.Type != "computer_call" {
		t.Fatalf("expected the error to name the unsupported type, got %q", unsupported.Type)
	}
}

// ---------------------------------------------------------------------
// Unknown/future field preservation within a known type
// ---------------------------------------------------------------------

func TestInputItem_PreservesUnknownFieldsOnKnownType(t *testing.T) {
	src := `{"type":"message","role":"user","content":"hi","some_future_field":{"nested":true}}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := it.Extra["some_future_field"]; !ok {
		t.Fatalf("expected an unrecognized field on a known type to be preserved in Extra, got %+v", it.Extra)
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	future, ok := back["some_future_field"].(map[string]any)
	if !ok || future["nested"] != true {
		t.Fatalf("expected some_future_field to survive the round-trip verbatim, got %v", back["some_future_field"])
	}
}

// reasoning items: recognized as a supported type (required for stateless
// reasoning continuation under store:false), with their actual payload
// (summary, encrypted_content) preserved opaquely rather than modeled
// field-by-field or rejected.
func TestInputItem_Reasoning_AcceptedAndPayloadPreserved(t *testing.T) {
	src := `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thinking..."}],"encrypted_content":"opaque-blob"}`
	var it InputItem
	if err := json.Unmarshal([]byte(src), &it); err != nil {
		t.Fatalf("unmarshal reasoning item: %v", err)
	}
	if it.Type != "reasoning" || it.ID != "rs_1" {
		t.Fatalf("unexpected parsed reasoning item: %+v", it)
	}
	if _, ok := it.Extra["summary"]; !ok {
		t.Fatalf("expected the reasoning summary payload to be preserved in Extra")
	}
	if _, ok := it.Extra["encrypted_content"]; !ok {
		t.Fatalf("expected encrypted_content to be preserved in Extra")
	}
	out, err := json.Marshal(it)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if back["type"] != "reasoning" || back["id"] != "rs_1" || back["encrypted_content"] != "opaque-blob" {
		t.Fatalf("expected reasoning item fields preserved on round-trip, got %v", back)
	}
}

// ---------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------

func assertNoEmptyType(t *testing.T, data []byte) {
	t.Helper()
	if strings.Contains(string(data), `"type":""`) {
		t.Fatalf(`serialized input item must NEVER contain "type":"", got: %s`, data)
	}
}
