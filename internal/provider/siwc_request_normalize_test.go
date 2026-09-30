package provider

import (
	"encoding/json"
	"testing"
)

// This file proves the SIWC request-sanitization boundary
// (siwc_request_normalize.go) added after auditing the actual outgoing
// SubscriptionBackend request and finding it forwarded temperature,
// max_output_tokens, and other fields SIWC's preview-limitations page
// documents as required to be omitted - a real correctness defect.

func mustRequestFromJSON(t *testing.T, body string) Request {
	t.Helper()
	var req Request
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	return req
}

// Table-driven test for every SIWC-forbidden top-level field: presence
// must produce UnsupportedSIWCRequestParameterError, never a silently
// stripped/forwarded request.
func TestSIWCNormalize_ForbiddenTopLevelFields_Rejected(t *testing.T) {
	forbiddenValues := map[string]string{
		"background":             `"low"`,
		"conversation":           `"conv_123"`,
		"max_output_tokens":      `256`,
		"max_tool_calls":         `4`,
		"metadata":               `{"k":"v"}`,
		"moderation":             `"auto"`,
		"multi_agent":            `{"enabled":true}`,
		"prompt":                 `{"id":"pmpt_1"}`,
		"prompt_cache_retention": `"24h"`,
		"safety_identifier":      `"user-123"`,
		"temperature":            `0.7`,
		"top_logprobs":           `3`,
		"top_p":                  `0.9`,
		"truncation":             `"auto"`,
		"user":                   `"user-123"`,
	}
	for field, value := range forbiddenValues {
		t.Run(field, func(t *testing.T) {
			body := `{"model":"x","input":"hi",` + `"` + field + `":` + value + `}`
			req := mustRequestFromJSON(t, body)

			normalized, err := normalizeForSIWC(req)
			// 1. it never reaches the SIWC upstream request.
			if normalized != nil {
				t.Fatalf("expected normalization to fail for forbidden field %q, got a normalized request instead", field)
			}
			// 2. the caller receives the documented typed error.
			typed, ok := err.(*UnsupportedSIWCRequestParameterError)
			if !ok {
				t.Fatalf("expected *UnsupportedSIWCRequestParameterError for field %q, got %T: %v", field, err, err)
			}
			if typed.Field != field {
				t.Fatalf("expected the error to name field %q, got %q", field, typed.Field)
			}
		})
	}
}

// previous_response_id is a separate, distinctly-worded rejection
// (SIWCStatefulRequestError) since it signals a different kind of mistake
// (assuming server-side state) than a merely-unsupported parameter.
func TestSIWCNormalize_PreviousResponseID_Rejected(t *testing.T) {
	req := mustRequestFromJSON(t, `{"model":"x","input":"hi","previous_response_id":"resp_abc"}`)
	normalized, err := normalizeForSIWC(req)
	if normalized != nil {
		t.Fatalf("expected normalization to fail, got a normalized request")
	}
	if _, ok := err.(*SIWCStatefulRequestError); !ok {
		t.Fatalf("expected *SIWCStatefulRequestError, got %T: %v", err, err)
	}
}

// 3. no silent behavior change: an ordinary request with none of the
// forbidden fields normalizes successfully and carries none of them.
func TestSIWCNormalize_OrdinaryRequest_NormalizesCleanly(t *testing.T) {
	req := mustRequestFromJSON(t, `{"model":"x","input":"hi","tools":[{"type":"function","name":"f"}]}`)
	normalized, err := normalizeForSIWC(req)
	if err != nil {
		t.Fatalf("expected a clean request to normalize successfully, got: %v", err)
	}
	if normalized.Model != "x" || !normalized.Stream || normalized.Store {
		t.Fatalf("unexpected normalized request: %+v", normalized)
	}
	out, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal normalized request: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	for _, forbidden := range siwcForbiddenTopLevelFields {
		if _, present := back[forbidden]; present {
			t.Fatalf("normalized request must never carry forbidden field %q, got %v", forbidden, back)
		}
	}
	if _, present := back["previous_response_id"]; present {
		t.Fatalf("normalized request must never carry previous_response_id")
	}
}

// --- store ---

func TestSIWCNormalize_StoreMissing_EmitsFalse(t *testing.T) {
	req := mustRequestFromJSON(t, `{"model":"x","input":"hi"}`)
	normalized, err := normalizeForSIWC(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if normalized.Store != false {
		t.Fatalf("expected store=false when missing, got %v", normalized.Store)
	}
}

func TestSIWCNormalize_StoreFalse_Accepted(t *testing.T) {
	req := mustRequestFromJSON(t, `{"model":"x","input":"hi","store":false}`)
	normalized, err := normalizeForSIWC(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if normalized.Store != false {
		t.Fatalf("expected store=false, got %v", normalized.Store)
	}
}

func TestSIWCNormalize_StoreTrue_Rejected(t *testing.T) {
	req := mustRequestFromJSON(t, `{"model":"x","input":"hi","store":true}`)
	normalized, err := normalizeForSIWC(req)
	if normalized != nil {
		t.Fatalf("expected store:true to be rejected, got a normalized request")
	}
	typed, ok := err.(*UnsupportedSIWCRequestParameterError)
	if !ok {
		t.Fatalf("expected *UnsupportedSIWCRequestParameterError, got %T: %v", err, err)
	}
	if typed.Field != "store" {
		t.Fatalf("expected Field=store, got %q", typed.Field)
	}
}

// --- stream ---

func TestSIWCNormalize_StreamMissing_EmitsTrue(t *testing.T) {
	req := mustRequestFromJSON(t, `{"model":"x","input":"hi"}`)
	normalized, err := normalizeForSIWC(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if normalized.Stream != true {
		t.Fatalf("expected stream=true when missing, got %v", normalized.Stream)
	}
}

func TestSIWCNormalize_StreamTrue_Accepted(t *testing.T) {
	req := mustRequestFromJSON(t, `{"model":"x","input":"hi","stream":true}`)
	normalized, err := normalizeForSIWC(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if normalized.Stream != true {
		t.Fatalf("expected stream=true, got %v", normalized.Stream)
	}
}

// reasoning is not on the SIWC-forbidden list and must be forwarded, not
// silently dropped merely because siwcNormalizedRequest is a strict subset
// of Request.
func TestSIWCNormalize_ReasoningField_Forwarded(t *testing.T) {
	req := mustRequestFromJSON(t, `{"model":"x","input":"hi","reasoning":{"effort":"medium"}}`)
	normalized, err := normalizeForSIWC(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(normalized.Reasoning) != `{"effort":"medium"}` {
		t.Fatalf("expected reasoning forwarded verbatim, got %s", normalized.Reasoning)
	}
}

func TestSIWCNormalize_StreamFalse_Rejected(t *testing.T) {
	req := mustRequestFromJSON(t, `{"model":"x","input":"hi","stream":false}`)
	normalized, err := normalizeForSIWC(req)
	if normalized != nil {
		t.Fatalf("expected stream:false to be rejected, got a normalized request")
	}
	typed, ok := err.(*UnsupportedSIWCRequestParameterError)
	if !ok {
		t.Fatalf("expected *UnsupportedSIWCRequestParameterError, got %T: %v", err, err)
	}
	if typed.Field != "stream" {
		t.Fatalf("expected Field=stream, got %q", typed.Field)
	}
}
