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
	"fmt"
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
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*it = InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: asString}}}}
		return nil
	}
	var asArray []InputItem
	if err := json.Unmarshal(data, &asArray); err != nil {
		return fmt.Errorf("input: expected string or array of input items: %w", err)
	}
	*it = asArray
	return nil
}

// InputItem is a discriminated union over the input item "type" field:
// "message", "function_call_output", "function_call", or "reasoning". Not
// every field applies to every type; unused fields are omitted on encode.
type InputItem struct {
	Type string `json:"type"`

	// type == "message"
	Role    string        `json:"role,omitempty"`
	Content []ContentPart `json:"content,omitempty"`

	// type == "function_call" (echoed back from a prior turn) or
	// "function_call_output" (the tool result for one)
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
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
