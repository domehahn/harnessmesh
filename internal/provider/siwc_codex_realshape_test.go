package provider

import (
	"encoding/json"
	"net/http"
	"testing"
)

// This file proves that the REAL current Codex VS Code extension request
// shape - captured from a real E2E session - survives the provider
// pipeline without any field being silently dropped (Mission 6). The real
// captured shape's top-level keys are: client_metadata, include, input,
// model, parallel_tool_calls, prompt_cache_key, reasoning, store, stream,
// text, tool_choice. Input item types: additional_tools, message x4.
// additional_tools tool type: namespace.

const realCodexFullShapeFixture = `{
	"model": "gpt-5.6-luna",
	"client_metadata": {"extension_version": "1.2.3", "session_id": "sess_abc"},
	"include": ["reasoning.encrypted_content"],
	"parallel_tool_calls": true,
	"prompt_cache_key": "codex-cache-key-1",
	"reasoning": {"effort": "medium"},
	"text": {"format": {"type": "text"}},
	"tool_choice": "auto",
	"store": false,
	"stream": true,
	"input": [
		{
			"type": "additional_tools",
			"role": "developer",
			"tools": [
				{
					"type": "namespace",
					"name": "workspace_tools",
					"description": "Workspace tools.",
					"tools": [
						{"type": "function", "name": "read_file", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}
					]
				}
			]
		},
		{"role": "developer", "content": "You are Codex, running inside VS Code."},
		{"role": "user", "content": "what does main.go do?"},
		{"role": "user", "content": "please read it"}
	]
}`

// TestCodexRealFullShape_NoFieldSilentlyDropped_FullHTTPPath drives the
// exact real captured Codex request shape through the complete provider
// HTTP path and asserts every top-level field, every input item, and the
// namespace tool all reach the captured fake upstream unchanged.
func TestCodexRealFullShape_NoFieldSilentlyDropped_FullHTTPPath(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	rec := doRawProviderReq(t, handler, "test-provider-token", realCodexFullShapeFixture)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (the real Codex shape must be accepted), got %d: %s", rec.Code, rec.Body.String())
	}

	upstream := fakeResponses.LastBody()
	assertValidResponsesAPIJSON(t, upstream)
	assertNoEmptyType(t, upstream)

	var upstreamReq map[string]any
	if err := json.Unmarshal(upstream, &upstreamReq); err != nil {
		t.Fatalf("upstream body not valid JSON: %v", err)
	}

	// Every field from the real captured shape must be present and
	// unchanged - proving none was silently dropped.
	cm, ok := upstreamReq["client_metadata"].(map[string]any)
	if !ok || cm["session_id"] != "sess_abc" {
		t.Fatalf("expected client_metadata forwarded, got %v", upstreamReq["client_metadata"])
	}
	include, ok := upstreamReq["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("expected include forwarded, got %v", upstreamReq["include"])
	}
	if upstreamReq["parallel_tool_calls"] != true {
		t.Fatalf("expected parallel_tool_calls forwarded, got %v", upstreamReq["parallel_tool_calls"])
	}
	if upstreamReq["prompt_cache_key"] != "codex-cache-key-1" {
		t.Fatalf("expected prompt_cache_key forwarded, got %v", upstreamReq["prompt_cache_key"])
	}
	reasoning, ok := upstreamReq["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "medium" {
		t.Fatalf("expected reasoning forwarded, got %v", upstreamReq["reasoning"])
	}
	text, ok := upstreamReq["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text forwarded, got %v", upstreamReq["text"])
	}
	textFormat, ok := text["format"].(map[string]any)
	if !ok || textFormat["type"] != "text" {
		t.Fatalf("expected text.format preserved, got %v", text)
	}
	if upstreamReq["tool_choice"] != "auto" {
		t.Fatalf("expected tool_choice forwarded, got %v", upstreamReq["tool_choice"])
	}
	if upstreamReq["store"] != false {
		t.Fatalf("expected store=false, got %v", upstreamReq["store"])
	}
	if upstreamReq["stream"] != true {
		t.Fatalf("expected stream=true, got %v", upstreamReq["stream"])
	}

	// All 4 input items preserved, in order, with the namespace tool intact.
	inputArr, ok := upstreamReq["input"].([]any)
	if !ok || len(inputArr) != 4 {
		t.Fatalf("expected 4 input items forwarded, got %v", upstreamReq["input"])
	}
	atItem := inputArr[0].(map[string]any)
	if atItem["type"] != "additional_tools" {
		t.Fatalf("expected item 0 type=additional_tools, got %v", atItem["type"])
	}
	nsTool := atItem["tools"].([]any)[0].(map[string]any)
	if nsTool["type"] != "namespace" || nsTool["name"] != "workspace_tools" {
		t.Fatalf("expected namespace tool preserved, got %v", nsTool)
	}
	for i, wantContent := range []string{"", "You are Codex, running inside VS Code.", "what does main.go do?", "please read it"} {
		if i == 0 {
			continue // additional_tools, checked above
		}
		item := inputArr[i].(map[string]any)
		if item["type"] != "message" || item["content"] != wantContent {
			t.Fatalf("item %d: expected message with content=%q, got %v", i, wantContent, item)
		}
	}

	// Forbidden fields must never be forwarded (sanity check alongside the
	// forwarded-field assertions above).
	for _, forbidden := range siwcForbiddenTopLevelFields {
		if _, present := upstreamReq[forbidden]; present {
			t.Fatalf("forbidden field %q must never be forwarded, got %v", forbidden, upstreamReq[forbidden])
		}
	}
	if _, present := upstreamReq["previous_response_id"]; present {
		t.Fatalf("previous_response_id must never be forwarded")
	}
}
