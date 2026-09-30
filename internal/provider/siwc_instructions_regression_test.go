package provider

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestSIWCNormalize_RealCodexShape_InstructionsPreservedEndToEnd is the
// required full-path regression test: the exact real redacted Codex
// request shape - model, instructions, reasoning, tool_choice, stream,
// store, tools, input - driven through HTTP handler -> canonical request
// -> SIWC normalizer -> SubscriptionBackend -> captured fake upstream
// body, proving no semantic field silently disappears.
func TestSIWCNormalize_RealCodexShape_InstructionsPreservedEndToEnd(t *testing.T) {
	handler, fakeResponses := newSIWCTestServer(t)

	const instructionsText = "You are a coding agent operating inside VS Code."
	body := `{
		"model": "gpt-5.6-luna",
		"instructions": "` + instructionsText + `",
		"reasoning": {"effort": "medium"},
		"tool_choice": "auto",
		"stream": true,
		"store": false,
		"tools": [{"type": "function", "name": "get_weather", "description": "Get the weather"}],
		"input": [
			{"role": "user", "content": "what's the weather in nyc?"}
		]
	}`

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

	// 1 & 2. upstream contains instructions, exact value preserved.
	if upstreamReq["instructions"] != instructionsText {
		t.Fatalf("expected instructions=%q forwarded verbatim, got %v", instructionsText, upstreamReq["instructions"])
	}

	// 3. input is unchanged (still exactly the one message the caller
	// sent - no synthesized developer message prepended for instructions).
	inputArr, ok := upstreamReq["input"].([]any)
	if !ok || len(inputArr) != 1 {
		t.Fatalf("expected input unchanged (1 item), got %v", upstreamReq["input"])
	}
	item := inputArr[0].(map[string]any)
	if item["type"] != "message" || item["role"] != "user" || item["content"] != "what's the weather in nyc?" {
		t.Fatalf("expected the original input item unchanged, got %v", item)
	}

	// 4. reasoning is unchanged.
	reasoning, ok := upstreamReq["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "medium" {
		t.Fatalf("expected reasoning unchanged, got %v", upstreamReq["reasoning"])
	}

	// 5. tools are unchanged.
	tools, ok := upstreamReq["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected tools unchanged (1 tool), got %v", upstreamReq["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "get_weather" {
		t.Fatalf("expected tool unchanged, got %v", tool)
	}

	// 6. tool_choice is unchanged.
	if upstreamReq["tool_choice"] != "auto" {
		t.Fatalf("expected tool_choice=auto unchanged, got %v", upstreamReq["tool_choice"])
	}

	// 7 & 8. store=false, stream=true.
	if upstreamReq["store"] != false {
		t.Fatalf("expected store=false, got %v", upstreamReq["store"])
	}
	if upstreamReq["stream"] != true {
		t.Fatalf("expected stream=true, got %v", upstreamReq["stream"])
	}
}

// --- Normalizer design rule: every currently modeled top-level Request
// field must be provably forwarded, rejected, or deliberately normalized -
// never a fourth "accepted by parser and silently discarded" category. ---

// siwcTopLevelFieldDisposition documents, for one JSON key in the
// canonical Request, which of the three allowed categories it falls into.
type siwcTopLevelFieldDisposition struct {
	key  string
	kind string // "forwarded", "rejected", "normalized"
}

// siwcTopLevelFieldDispositions is the exhaustive disposition table for
// every top-level key the canonical Request currently models with a typed
// Go field (see wire.go's Request struct). model/input are load-bearing
// and asserted separately by every other test in this file; they are
// omitted here only to avoid duplicate bookkeeping, not because they lack
// a disposition (both are "forwarded").
var siwcTopLevelFieldDispositions = []siwcTopLevelFieldDisposition{
	{"instructions", "forwarded"},
	{"stream", "normalized"},
	{"tools", "forwarded"},
	{"tool_choice", "forwarded"},
	{"temperature", "rejected"},
	{"max_output_tokens", "rejected"},
	{"parallel_tool_calls", "forwarded"},
	{"previous_response_id", "rejected"},
	{"metadata", "rejected"},
	{"reasoning", "forwarded"},
	{"store", "normalized"},
}

// TestSIWCNormalize_NoFieldSilentlyDiscarded proves, for every currently
// modeled top-level Request field, that normalizeForSIWC either forwards
// it, rejects it with a typed error, or deliberately normalizes it - never
// silently discards it. A field accidentally left out of
// siwcTopLevelFieldDispositions (e.g. a newly added Request field) will
// cause forwarded/normalized checks below to have nothing to assert,
// which is why every case also has a corresponding assertion elsewhere in
// this file or in siwc_request_normalize_test.go - this test specifically
// re-drives each one here so the invariant is checked in one place.
func TestSIWCNormalize_NoFieldSilentlyDiscarded(t *testing.T) {
	values := map[string]string{
		"instructions":         `"hi"`,
		"stream":               `true`,
		"tools":                `[{"type":"function","name":"f"}]`,
		"tool_choice":          `"auto"`,
		"temperature":          `0.5`,
		"max_output_tokens":    `100`,
		"parallel_tool_calls":  `true`,
		"previous_response_id": `"resp_1"`,
		"metadata":             `{"k":"v"}`,
		"reasoning":            `{"effort":"low"}`,
		"store":                `false`,
	}

	for _, d := range siwcTopLevelFieldDispositions {
		t.Run(d.key, func(t *testing.T) {
			v, ok := values[d.key]
			if !ok {
				t.Fatalf("test bug: no sample value registered for field %q", d.key)
			}
			body := `{"model":"x","input":"hi","` + d.key + `":` + v + `}`
			req := mustRequestFromJSON(t, body)
			normalized, err := normalizeForSIWC(req)

			switch d.kind {
			case "rejected":
				if err == nil {
					t.Fatalf("field %q is classified \"rejected\" but normalizeForSIWC accepted it", d.key)
				}
			case "forwarded":
				if err != nil {
					t.Fatalf("field %q is classified \"forwarded\" but normalizeForSIWC rejected it: %v", d.key, err)
				}
				out, marshalErr := json.Marshal(normalized)
				if marshalErr != nil {
					t.Fatalf("marshal: %v", marshalErr)
				}
				var back map[string]any
				if unmarshalErr := json.Unmarshal(out, &back); unmarshalErr != nil {
					t.Fatalf("unmarshal round-trip: %v", unmarshalErr)
				}
				if _, present := back[d.key]; !present {
					t.Fatalf("field %q is classified \"forwarded\" but is absent from the normalized/marshaled upstream request - this is exactly the silent-discard defect this test exists to catch", d.key)
				}
			case "normalized":
				if err != nil {
					t.Fatalf("field %q is classified \"normalized\" but normalizeForSIWC rejected the documented-allowed value: %v", d.key, err)
				}
				out, marshalErr := json.Marshal(normalized)
				if marshalErr != nil {
					t.Fatalf("marshal: %v", marshalErr)
				}
				var back map[string]any
				if unmarshalErr := json.Unmarshal(out, &back); unmarshalErr != nil {
					t.Fatalf("unmarshal round-trip: %v", unmarshalErr)
				}
				if _, present := back[d.key]; !present {
					t.Fatalf("field %q is classified \"normalized\" but is absent from the normalized/marshaled upstream request", d.key)
				}
			default:
				t.Fatalf("test bug: unknown disposition kind %q for field %q", d.kind, d.key)
			}
		})
	}
}
