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
		var unsupported *UnsupportedInputItemTypeError
		if errors.As(err, &unsupported) {
			// A specific, already-typed per-item error is more useful than
			// the generic "expected string or array" wrapper below - the
			// top-level shape WAS a valid array, only one item's type was
			// unrecognized.
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

// UnsupportedInputItemTypeError is returned when an input item's "type" is
// not one of the Responses item types HarnessMesh actively models
// ("message", "function_call", "function_call_output", "reasoning"). An
// unrecognized type is rejected outright with this specific, typed error -
// never silently passed through unexamined (which risks forwarding an item
// HarnessMesh has subtly mis-serialized) and never silently stripped
// (which would lose data without telling the caller).
type UnsupportedInputItemTypeError struct {
	Type string
}

func (e *UnsupportedInputItemTypeError) Error() string {
	return fmt.Sprintf("unsupported input item type %q", e.Type)
}

// InputItem is a discriminated union over the Responses API input item
// "type" field. HarnessMesh models exactly the item types Codex is known
// to send: "message", "function_call", "function_call_output", and
// "reasoning" (required so a prior turn's reasoning item can be echoed
// back as input on the next stateless request - SIWC requires store:false,
// so nothing is retained server-side between turns). Any JSON field
// present on a recognized item that isn't one of the named fields below -
// including fields this package doesn't interpret, like a reasoning item's
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
	ID        string
	CallID    string
	Name      string
	Arguments string
	Output    string
	Status    string

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
	default:
		return &UnsupportedInputItemTypeError{Type: typ}
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
