// Package provider implements HarnessMesh's Codex-compatible model-provider
// gateway: an HTTP server speaking the subset of the OpenAI Responses API
// wire protocol (wire_api = "responses") that the official Codex VS Code
// extension / Codex CLI require of a custom model_provider. It is a
// separate, bounded plane from HarnessMesh's collaboration engine
// (internal/collaboration) - this package performs no collaboration
// operations, and nothing in internal/collaboration depends on it.
//
// Verified against OpenAI/Codex documentation as of 2026-09-29:
//   - developers.openai.com/codex/config-reference (model_providers table:
//     base_url, name, wire_api, env_key/experimental_bearer_token/auth,
//     http_headers/env_http_headers, query_params, request_max_retries,
//     stream_idle_timeout_ms, stream_max_retries, supports_websockets)
//   - "responses" is the only supported wire_api value (default when
//     omitted); chat/completions support was deprecated.
//   - Convention: base_url ends in "/v1"; Codex POSTs to
//     "{base_url}/responses".
//   - developers.openai.com/api/reference/resources/responses/streaming-events
//     (SSE event names: response.created, response.output_item.added,
//     response.output_text.delta, response.function_call_arguments.delta,
//     response.completed, error, etc.)
package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Request is the subset of the Responses API request body HarnessMesh
// accepts. Fields Codex is not known to require are intentionally omitted
// rather than guessed.
type Request struct {
	Model              string          `json:"model"`
	Input              InputItems      `json:"input"`
	Instructions       string          `json:"instructions,omitempty"`
	Stream             bool            `json:"stream,omitempty"`
	Tools              []Tool          `json:"tools,omitempty"`
	ToolChoice         json.RawMessage `json:"tool_choice,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	MaxOutputTokens    *int            `json:"max_output_tokens,omitempty"`
	ParallelToolCalls  *bool           `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Metadata           map[string]any  `json:"metadata,omitempty"`
}

// InputItems accepts either a plain string (shorthand for a single user
// message) or an array of typed input items, matching the Responses API's
// documented flexible "input" field.
type InputItems []InputItem

func (it *InputItems) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" || trimmed == "" {
		*it = nil
		return nil
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*it = InputItems{{Type: "message", Role: "user", Content: NewPartsContent([]ContentPart{{Type: "input_text", Text: asString}})}}
		return nil
	}
	var asArray []InputItem
	if err := json.Unmarshal(data, &asArray); err != nil {
		// A specific, already-typed per-item compatibility error is more
		// useful than the generic "expected string or array" wrapper below
		// - the top-level shape WAS a valid array, only one item failed a
		// specific, named compatibility check.
		var unsupportedType *UnsupportedResponsesInputItemTypeError
		var unsupportedCapability *UnsupportedSIWCCapabilityError
		var malformed *MalformedInputItemError
		if errors.As(err, &unsupportedType) || errors.As(err, &unsupportedCapability) || errors.As(err, &malformed) {
			return err
		}
		return fmt.Errorf("input: expected string or array of input items: %w", err)
	}
	*it = asArray
	return nil
}

// Content represents the Responses API message "content" field, which the
// documented schema allows to be encoded as EITHER a plain string (the
// "easy message" shorthand OpenAI's own SIWC documentation demonstrates:
// {"role":"user","content":"Say exactly: Hello, world!"}) or an array of
// typed content parts ({"type":"input_text","text":"..."} and friends).
// Content round-trips through whichever encoding it was received in -
// HarnessMesh does not normalize one form into the other when forwarding a
// request upstream, since both are independently valid wire shapes and
// real Codex/Responses examples use both.
type Content struct {
	text   string
	parts  []ContentPart
	isText bool
	set    bool
}

// NewTextContent builds Content in its plain-string form.
func NewTextContent(text string) Content { return Content{text: text, isText: true, set: true} }

// NewPartsContent builds Content in its content-parts-array form.
func NewPartsContent(parts []ContentPart) Content {
	return Content{parts: parts, isText: false, set: true}
}

// IsSet reports whether this Content was actually populated (as opposed to
// a zero-value Content on an item type, like function_call, that has no
// "content" field at all).
func (c Content) IsSet() bool { return c.set }

// IsText reports whether this Content was encoded as a plain string.
func (c Content) IsText() bool { return c.set && c.isText }

// Text returns the plain-string form's value (empty if IsText is false).
func (c Content) Text() string { return c.text }

// Parts returns the content-parts-array form's value (nil if IsText is true).
func (c Content) Parts() []ContentPart { return c.parts }

// PlainText renders this content as flat text, for backends (Bedrock,
// Codex CLI, OpenAI-compatible Chat Completions) that only ever accept a
// single text string: the plain-string form verbatim, or every part's Text
// field concatenated in order.
func (c Content) PlainText() string {
	if !c.set {
		return ""
	}
	if c.isText {
		return c.text
	}
	var b strings.Builder
	for _, p := range c.parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

func (c *Content) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		return fmt.Errorf("content: null is not a valid message content value")
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*c = Content{text: asString, isText: true, set: true}
		return nil
	}
	var asParts []ContentPart
	if err := json.Unmarshal(data, &asParts); err == nil {
		*c = Content{parts: asParts, isText: false, set: true}
		return nil
	}
	return fmt.Errorf("content: expected a string or an array of content parts")
}

func (c Content) MarshalJSON() ([]byte, error) {
	if !c.set {
		return []byte("null"), nil
	}
	if c.isText {
		return json.Marshal(c.text)
	}
	if c.parts == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(c.parts)
}

// This package distinguishes three distinct compatibility-failure modes
// for an input item, rather than collapsing them into one generic error,
// per an explicit real-account audit requirement: fixing one Codex-emitted
// item type per real failure is not sustainable, so each failure mode must
// say precisely which kind of gap it is.
//
//   - UnsupportedResponsesInputItemTypeError: the item's "type" is not
//     found anywhere in the current official Responses API input-item
//     schema this package could verify (developers.openai.com/api/reference/
//     resources/responses, fetched 2026-09-30) - a genuinely unknown or
//     non-schema type.
//   - UnsupportedSIWCCapabilityError: the item's "type" (or, for
//     additional_tools, a tool definition's "type") IS a real, documented
//     Responses item/tool type, but SIWC's documented preview-limitations
//     page (developers.openai.com/siwc/token-sharing-open-source/
//     preview-limitations, fetched 2026-09-30) explicitly lists it as
//     unsupported. That page's exact "Unsupported" list: "Image
//     generation, file search, Code Interpreter, native computer use,
//     hosted MCP/connectors", "Tool search functionality",
//     "programmatic_tool_calling at the top level", and "Apply patch,
//     local shell, and other specialized execution tools" - the last item
//     is a real correction from an earlier pass of this audit, which
//     incorrectly assumed apply_patch/local_shell were SIWC-supported
//     "local Codex execution patterns" merely because they execute
//     client-side; the preview-limitations page names them explicitly as
//     unsupported regardless. additional_tools support is NOT blanket
//     permission to enable every hosted or specialized-execution Responses
//     tool. Note the same page's "Supported" list explicitly includes
//     "Web search (subject to model and account/workspace policy)" -
//     web_search_call is therefore NOT in this category (see
//     siwcSupportedResponsesItemTypes below).
//   - MalformedInputItemError: the item's "type" is both known AND
//     SIWC-supported, but its shape doesn't satisfy that type's documented
//     structural requirements (e.g. additional_tools with a missing/empty
//     tools array, or a non-"developer" role).
type UnsupportedResponsesInputItemTypeError struct {
	Type string
}

func (e *UnsupportedResponsesInputItemTypeError) Error() string {
	return fmt.Sprintf("unsupported input item type %q", e.Type)
}

// UnsupportedSIWCCapabilityError is returned for a real, documented
// Responses API capability (an item type or a tool type) that is outside
// SIWC/ChatGPT-plan-usage's documented preview scope - most commonly a
// hosted tool that requires OpenAI-side execution this locally-hosted
// gateway cannot and does not claim to support.
type UnsupportedSIWCCapabilityError struct {
	Capability string
}

func (e *UnsupportedSIWCCapabilityError) Error() string {
	return fmt.Sprintf("capability %q is not supported under SIWC/ChatGPT-plan usage", e.Capability)
}

// MalformedInputItemError is returned for a known, SIWC-supported item
// type whose shape violates that type's documented structural
// requirements.
type MalformedInputItemError struct {
	Type   string
	Reason string
}

func (e *MalformedInputItemError) Error() string {
	return fmt.Sprintf("malformed %s input item: %s", e.Type, e.Reason)
}

// siwcUnsupportedResponsesItemTypes are real, documented Responses API
// input item types - confirmed via developers.openai.com/api/reference/
// resources/responses/methods/create.md, developers.openai.com/api/docs/
// guides/tools-apply-patch.md, and developers.openai.com/api/docs/guides/
// tools-local-shell.md (all fetched 2026-09-30) - that SIWC's preview-
// limitations page (developers.openai.com/siwc/token-sharing-open-source/
// preview-limitations) explicitly places outside its supported scope.
// Recognizing them by name (rather than falling through to "unknown type")
// lets HarnessMesh give a precise UnsupportedSIWCCapabilityError instead
// of a generic unsupported-type error. shell_call/shell_call_output are
// deliberately NOT included: only local_shell_call/local_shell_call_output
// could be confirmed to exist in the fetched documentation.
var siwcUnsupportedResponsesItemTypes = map[string]bool{
	// "native computer use" - confirmed unsupported.
	"computer_call":        true,
	"computer_call_output": true,
	// "file search" - confirmed unsupported.
	"file_search_call": true,
	// "Tool search functionality" - confirmed unsupported.
	"tool_search_call":   true,
	"tool_search_output": true,
	// "Apply patch, local shell, and other specialized execution tools" -
	// confirmed unsupported, even though they execute client-side.
	"apply_patch_call":        true,
	"apply_patch_call_output": true,
	"local_shell_call":        true,
	"local_shell_call_output": true,
	// "Image generation" / "Code Interpreter" - confirmed unsupported.
	"image_generation_call": true,
	"code_interpreter_call": true,
	// "hosted MCP/connectors" - confirmed unsupported.
	"mcp_call":              true,
	"mcp_list_tools":        true,
	"mcp_approval_request":  true,
	"mcp_approval_response": true,
}

// web_search_call, custom_tool_call, and custom_tool_call_output are real,
// documented Responses API input item types that SIWC's preview-
// limitations page does NOT list as unsupported (either explicitly
// permitted, like web_search_call, or a generic client-executed mechanism
// like custom_tool_call the page's "Supported" list names directly:
// "function/custom tools") - see their own InputItem.UnmarshalJSON case
// blocks below, which model them losslessly via the same Extra-
// preservation pattern as reasoning: only the fields needed for basic
// structure (id/status/call_id/...) are named, everything else -
// including web_search_call's polymorphic "action" payload - passes
// through untouched.

// siwcSupportedToolTypes are the tool "type" values SIWC's
// preview-limitations page describes as supported: plain function tools,
// custom tools ("Supported: ... function/custom tools"), and web search
// ("Supported: Web search, subject to model and account/workspace
// policy"). Any other tool type embedded in additional_tools.tools (e.g. a
// hosted "file_search"/"computer_use_preview"/"code_interpreter"/"mcp"/
// "image_generation" tool, or the specifically-named-unsupported
// "apply_patch"/"local_shell" tool types) is rejected as an unsupported
// SIWC capability - additional_tools support is not blanket permission to
// enable every hosted or specialized-execution Responses tool.
var siwcSupportedToolTypes = map[string]bool{
	"function":   true,
	"custom":     true,
	"web_search": true,
}

// InputItem is a discriminated union over the Responses API input item
// "type" field. HarnessMesh models exactly the item types Codex is known
// to send AND that SIWC's documented preview scope supports: "message",
// "function_call", "function_call_output", "reasoning" (required so a
// prior turn's reasoning item can be echoed back as input on the next
// stateless request - SIWC requires store:false, so nothing is retained
// server-side between turns), and "additional_tools" (function/custom
// tools made available starting at this item's position in input - see
// developers.openai.com/api/reference/resources/responses, fetched
// 2026-09-30: "A list of additional tools made available at this item"
// with role:"developer" and a tools array). Any JSON field present on a
// recognized item that isn't one of the named fields below - including
// fields this package doesn't interpret, like a reasoning item's
// "summary"/"encrypted_content" payload - is preserved losslessly in Extra
// and re-emitted verbatim, rather than silently dropped.
type InputItem struct {
	// Type is never emitted as an empty string: an "easy message" item
	// (role+content with no explicit type - a documented shorthand) is
	// normalized to "message", the canonical wire type OpenAI's Responses
	// endpoint actually requires, during unmarshal (and defensively again
	// on marshal, for a hand-constructed InputItem).
	Type string

	// type == "message"
	Role    string
	Content Content

	// type == "function_call": ID/CallID/Name/Arguments/Status.
	// type == "function_call_output": ID/CallID/Output/Status.
	// type == "reasoning": ID/Status only - its actual reasoning payload
	// (summary, encrypted_content, ...) is intentionally NOT modeled here
	// and lives entirely in Extra, since HarnessMesh never interprets it.
	// type == "web_search_call": ID/Status only - its "action" payload is
	// intentionally left in Extra (polymorphic: search/open_page/
	// find_in_page shapes this package does not need to interpret).
	// type == "custom_tool_call": ID/CallID/Name/Input/Status.
	// type == "custom_tool_call_output": ID/CallID/Output/Status.
	// type == "additional_tools": Role (must be "developer") and Tools.
	ID        string
	CallID    string
	Name      string
	Arguments string
	Input     string
	Output    string
	Status    string

	// Tools holds an additional_tools item's tool definitions, each
	// preserved as raw JSON exactly as received - including nested JSON
	// Schema "parameters" - rather than decoded into a lossy shape. A
	// []json.RawMessage marshals back to a JSON array element-wise, so the
	// original array round-trips byte-for-byte.
	Tools []json.RawMessage

	// Extra preserves every JSON field on this item not already modeled
	// above.
	Extra map[string]json.RawMessage
}

func (it *InputItem) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("input item: expected a JSON object: %w", err)
	}

	typ := ""
	if v, ok := raw["type"]; ok {
		if err := json.Unmarshal(v, &typ); err != nil {
			return fmt.Errorf("input item: \"type\" must be a string: %w", err)
		}
	}
	_, hasRole := raw["role"]
	_, hasContent := raw["content"]
	if typ == "" {
		if hasRole || hasContent {
			// The documented "easy message" shorthand: role+content with
			// no explicit type. Normalize it to the canonical wire type
			// rather than emitting - or forwarding upstream - type:"".
			typ = "message"
		} else {
			return fmt.Errorf("input item: missing required \"type\" field")
		}
	}

	item := InputItem{Type: typ, Extra: make(map[string]json.RawMessage, len(raw))}
	for k, v := range raw {
		item.Extra[k] = v
	}
	delete(item.Extra, "type")

	assignStringField := func(key string, dst *string) error {
		v, ok := raw[key]
		if !ok {
			return nil
		}
		if err := json.Unmarshal(v, dst); err != nil {
			return fmt.Errorf("input item (%s): %q must be a string: %w", typ, key, err)
		}
		delete(item.Extra, key)
		return nil
	}

	switch typ {
	case "message":
		if err := assignStringField("role", &item.Role); err != nil {
			return err
		}
		v, ok := raw["content"]
		if !ok {
			return fmt.Errorf("input item (message): missing required \"content\" field")
		}
		if err := json.Unmarshal(v, &item.Content); err != nil {
			return fmt.Errorf("input item (message): %w", err)
		}
		delete(item.Extra, "content")
	case "function_call":
		for _, f := range []struct {
			key string
			dst *string
		}{{"call_id", &item.CallID}, {"id", &item.ID}, {"name", &item.Name}, {"arguments", &item.Arguments}, {"status", &item.Status}} {
			if err := assignStringField(f.key, f.dst); err != nil {
				return err
			}
		}
	case "function_call_output":
		for _, f := range []struct {
			key string
			dst *string
		}{{"call_id", &item.CallID}, {"id", &item.ID}, {"output", &item.Output}, {"status", &item.Status}} {
			if err := assignStringField(f.key, f.dst); err != nil {
				return err
			}
		}
	case "reasoning":
		for _, f := range []struct {
			key string
			dst *string
		}{{"id", &item.ID}, {"status", &item.Status}} {
			if err := assignStringField(f.key, f.dst); err != nil {
				return err
			}
		}
	case "web_search_call":
		// Confirmed SIWC-supported ("Web search, subject to model and
		// account/workspace policy"); HarnessMesh cannot itself verify that
		// policy, so it forwards the item losslessly (id/status named,
		// everything else - including the polymorphic "action" payload -
		// preserved via Extra) and lets the real upstream API enforce it.
		for _, f := range []struct {
			key string
			dst *string
		}{{"id", &item.ID}, {"status", &item.Status}} {
			if err := assignStringField(f.key, f.dst); err != nil {
				return err
			}
		}
	case "custom_tool_call":
		for _, f := range []struct {
			key string
			dst *string
		}{{"id", &item.ID}, {"call_id", &item.CallID}, {"name", &item.Name}, {"input", &item.Input}, {"status", &item.Status}} {
			if err := assignStringField(f.key, f.dst); err != nil {
				return err
			}
		}
	case "custom_tool_call_output":
		// custom_tool_call's output-item shape was not directly observed
		// in the fetched documentation (only the call item's JSON example
		// was shown); this mirrors the consistent call/call_output field
		// pattern confirmed for function_call_output, apply_patch_call_output,
		// and local_shell_call_output.
		for _, f := range []struct {
			key string
			dst *string
		}{{"call_id", &item.CallID}, {"id", &item.ID}, {"output", &item.Output}, {"status", &item.Status}} {
			if err := assignStringField(f.key, f.dst); err != nil {
				return err
			}
		}
	case "additional_tools":
		if err := assignStringField("role", &item.Role); err != nil {
			return err
		}
		// The documented shape's role is a fixed "developer" - no other
		// value is described anywhere this package could verify, so
		// anything else (including a missing role) is malformed rather
		// than silently accepted or silently defaulted.
		if item.Role == "" {
			return &MalformedInputItemError{Type: typ, Reason: `missing required "role" field (must be "developer")`}
		}
		if item.Role != "developer" {
			return &MalformedInputItemError{Type: typ, Reason: fmt.Sprintf(`"role" must be "developer", got %q`, item.Role)}
		}

		toolsRaw, ok := raw["tools"]
		if !ok {
			return &MalformedInputItemError{Type: typ, Reason: `missing required "tools" field`}
		}
		var tools []json.RawMessage
		if err := json.Unmarshal(toolsRaw, &tools); err != nil {
			return &MalformedInputItemError{Type: typ, Reason: fmt.Sprintf(`"tools" must be a JSON array: %v`, err)}
		}
		if len(tools) == 0 {
			return &MalformedInputItemError{Type: typ, Reason: `"tools" must be non-empty`}
		}
		for i, rawTool := range tools {
			var probe struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(rawTool, &probe); err != nil {
				return &MalformedInputItemError{Type: typ, Reason: fmt.Sprintf("tools[%d] is not a valid tool definition object: %v", i, err)}
			}
			if probe.Type == "" {
				return &MalformedInputItemError{Type: typ, Reason: fmt.Sprintf(`tools[%d] is missing required "type" field`, i)}
			}
			if !siwcSupportedToolTypes[probe.Type] {
				return &UnsupportedSIWCCapabilityError{Capability: fmt.Sprintf("additional_tools tool type %q", probe.Type)}
			}
		}
		item.Tools = tools
		delete(item.Extra, "tools")
	default:
		if siwcUnsupportedResponsesItemTypes[typ] {
			return &UnsupportedSIWCCapabilityError{Capability: fmt.Sprintf("input item type %q", typ)}
		}
		return &UnsupportedResponsesInputItemTypeError{Type: typ}
	}

	*it = item
	return nil
}

func (it InputItem) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(it.Extra)+6)
	for k, v := range it.Extra {
		out[k] = v
	}

	typ := it.Type
	if typ == "" && (it.Role != "" || it.Content.IsSet()) {
		// Defensive normalization for a hand-constructed InputItem that set
		// Role/Content but forgot Type - matches the same normalization
		// UnmarshalJSON applies to the documented "easy message" shorthand.
		typ = "message"
	}
	if typ == "" {
		// There must NEVER be a serialized Responses item containing
		// "type":"" - if normalization above couldn't infer one, fail
		// loudly instead of emitting it.
		return nil, fmt.Errorf("input item: cannot marshal without a type")
	}
	typJSON, err := json.Marshal(typ)
	if err != nil {
		return nil, err
	}
	out["type"] = typJSON

	setStr := func(key, v string) error {
		if v == "" {
			return nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		out[key] = b
		return nil
	}

	switch typ {
	case "message":
		if err := setStr("role", it.Role); err != nil {
			return nil, err
		}
		if it.Content.IsSet() {
			b, err := it.Content.MarshalJSON()
			if err != nil {
				return nil, err
			}
			out["content"] = b
		}
	case "function_call":
		for _, f := range []struct {
			key string
			val string
		}{{"call_id", it.CallID}, {"id", it.ID}, {"name", it.Name}, {"arguments", it.Arguments}, {"status", it.Status}} {
			if err := setStr(f.key, f.val); err != nil {
				return nil, err
			}
		}
	case "function_call_output":
		for _, f := range []struct {
			key string
			val string
		}{{"call_id", it.CallID}, {"id", it.ID}, {"output", it.Output}, {"status", it.Status}} {
			if err := setStr(f.key, f.val); err != nil {
				return nil, err
			}
		}
	case "reasoning":
		for _, f := range []struct {
			key string
			val string
		}{{"id", it.ID}, {"status", it.Status}} {
			if err := setStr(f.key, f.val); err != nil {
				return nil, err
			}
		}
	case "web_search_call":
		for _, f := range []struct {
			key string
			val string
		}{{"id", it.ID}, {"status", it.Status}} {
			if err := setStr(f.key, f.val); err != nil {
				return nil, err
			}
		}
	case "custom_tool_call":
		for _, f := range []struct {
			key string
			val string
		}{{"id", it.ID}, {"call_id", it.CallID}, {"name", it.Name}, {"input", it.Input}, {"status", it.Status}} {
			if err := setStr(f.key, f.val); err != nil {
				return nil, err
			}
		}
	case "custom_tool_call_output":
		for _, f := range []struct {
			key string
			val string
		}{{"call_id", it.CallID}, {"id", it.ID}, {"output", it.Output}, {"status", it.Status}} {
			if err := setStr(f.key, f.val); err != nil {
				return nil, err
			}
		}
	case "additional_tools":
		if err := setStr("role", it.Role); err != nil {
			return nil, err
		}
		toolsJSON, err := json.Marshal(it.Tools)
		if err != nil {
			return nil, err
		}
		out["tools"] = toolsJSON
	}

	return json.Marshal(out)
}

type ContentPart struct {
	Type string `json:"type"` // "input_text", "output_text", "input_image", ...
	Text string `json:"text,omitempty"`
}

// Tool describes a function/tool Codex has made available for the model to
// call. HarnessMesh only needs to pass this through to the backend and
// report it back verbatim - it never interprets or executes tools itself
// (see internal/provider's package doc: agent-runtime logic belongs to
// Codex, not the provider endpoint).
type Tool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// Status values for a Response or an output item.
const (
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusIncomplete = "incomplete"
	StatusFailed     = "failed"
)

// Response is the Responses API response object, returned as the body of a
// non-streaming request and as the payload of the final response.completed
// (or response.failed) streaming event.
type Response struct {
	ID                string          `json:"id"`
	Object            string          `json:"object"` // "response"
	CreatedAt         int64           `json:"created_at"`
	Status            string          `json:"status"`
	Model             string          `json:"model"`
	Output            []OutputItem    `json:"output"`
	Usage             *Usage          `json:"usage,omitempty"`
	Error             *ResponseError  `json:"error,omitempty"`
	IncompleteDetails json.RawMessage `json:"incomplete_details,omitempty"`
	ParallelToolCalls bool            `json:"parallel_tool_calls,omitempty"`
	Metadata          map[string]any  `json:"metadata,omitempty"`
}

// OutputItem is a discriminated union: "message" (assistant text) or
// "function_call" (a tool invocation the caller must execute and answer
// with a function_call_output input item on the next turn).
type OutputItem struct {
	ID     string `json:"id"`
	Type   string `json:"type"` // "message", "function_call", "reasoning"
	Status string `json:"status,omitempty"`

	// type == "message"
	Role    string        `json:"role,omitempty"`
	Content []ContentPart `json:"content,omitempty"`

	// type == "function_call"
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
}
