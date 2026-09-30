package provider

import (
	"encoding/json"
	"fmt"
)

// This file implements the single, explicit boundary between the general-
// purpose Request (which may continue to represent the complete Responses
// API) and the route-specific request the chatgpt-subscription (SIWC)
// backend is actually permitted to send. One-off field deletions must
// never be scattered through backend_subscription.go; every SIWC-specific
// rule lives here.
//
//	canonical Request
//	        |
//	        v
//	  normalizeForSIWC
//	        |
//	        +-- reject any forbidden top-level field (typed error, never a
//	        |   silent strip - removing a caller's explicit temperature/
//	        |   metadata/... would silently change request semantics)
//	        +-- reject previous_response_id (SIWC has no server-side state;
//	        |   continuation must replay full history via `input`)
//	        +-- enforce store=false / stream=true (the two fields SIWC
//	        |   documents as REQUIRED to specific values, not merely
//	        |   forbidden - an explicit contradicting value is rejected,
//	        |   never silently overridden)
//	        |
//	        v
//	siwcNormalizedRequest -> SubscriptionBackend's upstream JSON
//
// Verified against developers.openai.com/siwc/token-sharing-open-source/
// preview-limitations, fetched 2026-09-30. Its exact "must be omitted"
// list: background, conversation, max_output_tokens, max_tool_calls,
// metadata, moderation, multi_agent, prompt, prompt_cache_retention,
// safety_identifier, temperature, top_logprobs, top_p, truncation, user -
// plus previous_response_id (stateless input replay is required instead,
// per the same page and developers.openai.com/api/docs/guides/
// reasoning.md's store:false replay guidance).

// siwcForbiddenTopLevelFields are Responses API request parameters SIWC's
// preview-limitations page requires be omitted entirely. Presence is
// checked via Request.RawKeys, so this is enforced uniformly whether or
// not the canonical Request struct happens to also model that field with
// a typed Go field.
var siwcForbiddenTopLevelFields = []string{
	"background",
	"conversation",
	"max_output_tokens",
	"max_tool_calls",
	"metadata",
	"moderation",
	"multi_agent",
	"prompt",
	"prompt_cache_retention",
	"safety_identifier",
	"temperature",
	"top_logprobs",
	"top_p",
	"truncation",
	"user",
}

// UnsupportedSIWCRequestParameterError is returned when a request destined
// for the chatgpt-subscription (SIWC) backend includes a top-level
// Responses API parameter the current official SIWC preview documentation
// requires be omitted, or an explicit value for store/stream that
// contradicts SIWC's required value for that field. HarnessMesh never
// silently strips or overrides these - a caller that actually needs
// temperature/metadata/a stateful store:true request has sent a request
// this route cannot honor, and must be told so precisely rather than
// having its semantics quietly changed.
type UnsupportedSIWCRequestParameterError struct {
	Field  string
	Reason string
}

func (e *UnsupportedSIWCRequestParameterError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("field %q is not supported on the chatgpt-subscription (SIWC) route: %s", e.Field, e.Reason)
	}
	return fmt.Sprintf("field %q is not supported on the chatgpt-subscription (SIWC) route and must be omitted", e.Field)
}

// SIWCStatefulRequestError is returned when a request destined for the
// chatgpt-subscription backend includes previous_response_id. SIWC
// requires store=false (no server-side conversation state), so
// continuation must be expressed by replaying the complete prior turn
// history in `input` (per developers.openai.com/api/docs/guides/
// reasoning.md's documented store:false pattern), never by referencing a
// previous response id HarnessMesh/SIWC never persisted.
type SIWCStatefulRequestError struct{}

func (e *SIWCStatefulRequestError) Error() string {
	return "previous_response_id is not supported on the chatgpt-subscription (SIWC) route: SIWC requires store=false (no server-side state is retained) - supply the complete prior turn history via the replayed input array instead"
}

// siwcNormalizedRequest is the SIWC-route-specific request shape - a
// strict subset of the general Request, deliberately kept separate from
// both Request and any other backend's request type so a change to one
// can never accidentally widen or narrow another.
type siwcNormalizedRequest struct {
	Model string     `json:"model"`
	Input InputItems `json:"input"`
	// Instructions is forwarded as its own top-level field - never folded
	// into `input` as a synthesized message. SIWC's preview-limitations
	// page confirms this is the documented mechanism: "Use `instructions`
	// or developer messages; explicit {type: "message", role: "system"}
	// items are rejected." Folding it into input would both duplicate the
	// documented `instructions` mechanism and risk colliding with that
	// same page's rejection of explicit system-role items if a caller
	// later added one.
	Instructions      string          `json:"instructions,omitempty"`
	Stream            bool            `json:"stream"`
	Store             bool            `json:"store"`
	Tools             []Tool          `json:"tools,omitempty"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Reasoning         json.RawMessage `json:"reasoning,omitempty"`
}

// normalizeForSIWC derives a SIWC-contract-compliant request from the
// canonical Request, or returns a typed error instead of silently
// changing request semantics. This is the ONLY place SIWC's field
// allow/deny rules are enforced.
func normalizeForSIWC(req Request) (*siwcNormalizedRequest, error) {
	for _, field := range siwcForbiddenTopLevelFields {
		if _, present := req.RawKeys[field]; present {
			return nil, &UnsupportedSIWCRequestParameterError{Field: field}
		}
	}
	if req.PreviousResponseID != "" {
		return nil, &SIWCStatefulRequestError{}
	}

	// store: missing or explicit false -> emit false. Explicit true is
	// rejected outright - never silently downgraded to a stateless
	// request, since that would change what the caller asked for.
	if raw, present := req.RawKeys["store"]; present {
		var store bool
		if err := json.Unmarshal(raw, &store); err != nil {
			return nil, &UnsupportedSIWCRequestParameterError{Field: "store", Reason: `must be a JSON boolean`}
		}
		if store {
			return nil, &UnsupportedSIWCRequestParameterError{Field: "store", Reason: `SIWC requires store=false (no server-side state); "store":true is not supported`}
		}
	}

	// stream: missing or explicit true -> emit true. Explicit false is
	// rejected outright - never silently upgraded to a streaming request.
	if raw, present := req.RawKeys["stream"]; present {
		var stream bool
		if err := json.Unmarshal(raw, &stream); err != nil {
			return nil, &UnsupportedSIWCRequestParameterError{Field: "stream", Reason: `must be a JSON boolean`}
		}
		if !stream {
			return nil, &UnsupportedSIWCRequestParameterError{Field: "stream", Reason: `SIWC requires stream=true; "stream":false is not supported`}
		}
	}

	return &siwcNormalizedRequest{
		Model:             req.Model,
		Input:             req.Input,
		Instructions:      req.Instructions,
		Stream:            true,
		Store:             false,
		Tools:             req.Tools,
		ToolChoice:        req.ToolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
		Reasoning:         req.Reasoning,
	}, nil
}
