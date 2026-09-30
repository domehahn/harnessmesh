package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// This file reproduces and proves the fix for the next real Codex VS Code
// model-catalog decode failure:
//
//	failed to refresh available models: stream disconnected before
//	completion: failed to decode models response: Data at line 1 column
//	144 (body: 145 bytes)
//
// ROOT CAUSE (confirmed from the real OpenAI Codex source,
// github.com/openai/codex, tag rust-v0.155.0-alpha.16.3 - the exact tag
// matching the real installed extension's reported cli_version
// 0.155.0-alpha.16.3): Codex's ModelsClient never decodes the public
// OpenAI /v1/models list shape at all. It requests
// "{base}/models?client_version=<version>"
// (codex-rs/model-provider/src/models_endpoint.rs) and deserializes the
// response as `ModelsResponse { models: Vec<ModelInfo> }`
// (codex-rs/protocol/src/openai_models.rs) - a structurally different,
// Codex-specific schema. A prior fix made the OpenAI-shaped response
// valid per the public Model object (id/object/created/owned_by), but
// that was the wrong schema entirely for Codex, which explains why the
// live decode error persisted despite that fix. handleModels now branches
// on the presence of the "client_version" query parameter (Mission 1) and
// serves the embedded, byte-faithful real Codex catalog payload
// (codex_model_catalog.go) for that branch.

// codexRequiredModelInfoFields are the ModelInfo fields confirmed to have
// NO `#[serde(default)]` in codex-rs/protocol/src/openai_models.rs at tag
// rust-v0.155.0-alpha.16.3 - i.e. required keys on the wire (some are
// nullable Option<T> without a default attribute, which still requires
// the JSON *key* to be present, just permits a null value).
var codexRequiredModelInfoFields = []string{
	"slug",
	"display_name",
	"description", // Option<String>, no default -> key required, value may be null
	"supported_reasoning_levels",
	"shell_type",
	"visibility",
	"supported_in_api",
	"priority",
	"availability_nux", // Option<...>, no default -> key required, value may be null
	"upgrade",          // Option<...>, no default -> key required, value may be null
	"support_verbosity",
	"default_verbosity",     // Option<Verbosity>, no default -> key required, value may be null
	"apply_patch_tool_type", // Option<ApplyPatchToolType>, no default -> key required, value may be null
	"truncation_policy",
	"experimental_supported_tools",
}

// codexModelInfoCompat is a Go compatibility struct for the fields this
// package explicitly verifies the VALUE SHAPE of (beyond mere key
// presence, checked separately) - mirroring the real Codex 0.155
// ModelInfo contract's documented enum/struct types exactly.
type codexModelInfoCompat struct {
	Slug                       string                             `json:"slug"`
	DisplayName                string                             `json:"display_name"`
	SupportedReasoningLevels   []codexReasoningEffortPresetCompat `json:"supported_reasoning_levels"`
	ShellType                  string                             `json:"shell_type"` // "unified_exec" | "disabled"
	Visibility                 string                             `json:"visibility"` // "list" | "hide" | "none"
	SupportedInAPI             bool                               `json:"supported_in_api"`
	Priority                   int                                `json:"priority"`
	SupportVerbosity           bool                               `json:"support_verbosity"`
	TruncationPolicy           codexTruncationPolicyCompat        `json:"truncation_policy"`
	ExperimentalSupportedTools []string                           `json:"experimental_supported_tools"`
}

type codexReasoningEffortPresetCompat struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type codexTruncationPolicyCompat struct {
	Mode  string `json:"mode"` // "bytes" | "tokens"
	Limit int64  `json:"limit"`
}

type codexModelsResponseCompat struct {
	Models []codexModelInfoCompat `json:"models"`
}

// TEST A: generic OpenAI-compatible client - no client_version param.
func TestModels_GenericClient_OpenAICompatibleShape(t *testing.T) {
	backend := &fakeInferenceBackend{name: "chatgpt", typ: "chatgpt-subscription", events: simpleTextEvents("resp_1", "ok")}
	reg := newTestRegistry(t, map[string]InferenceBackend{"chatgpt": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "chatgpt"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var back map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &back); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if back["object"] != "list" {
		t.Fatalf(`expected .object == "list", got %v`, back["object"])
	}
	data, ok := back["data"].([]any)
	if !ok {
		t.Fatalf(".data is not an array: %v", back["data"])
	}
	if len(data) == 0 {
		t.Fatalf("expected at least one model")
	}
	// The Codex-dialect key must never leak into the generic response.
	if _, present := back["models"]; present {
		t.Fatalf(`generic OpenAI-compatible response must not carry a "models" key`)
	}
}

// TEST B: Codex client - GET /v1/models?client_version=0.155.0-alpha.16.3.
func TestModels_CodexClient_ModelsResponseSchema(t *testing.T) {
	backend := &fakeInferenceBackend{name: "chatgpt", typ: "chatgpt-subscription", events: simpleTextEvents("resp_1", "ok")}
	reg := newTestRegistry(t, map[string]InferenceBackend{"chatgpt": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "chatgpt"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	r := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.155.0-alpha.16.3", nil)
	r.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatalf("expected an ETag header on the Codex catalog response")
	}

	// .models is an array (top-level structural check).
	var top map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &top); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	modelsAny, ok := top["models"].([]any)
	if !ok {
		t.Fatalf(".models is not an array: %v", top["models"])
	}
	if len(modelsAny) == 0 {
		t.Fatalf("expected at least one model in the Codex catalog")
	}
	// The generic-dialect keys must never leak into the Codex response.
	if _, present := top["object"]; present {
		t.Fatalf(`Codex-dialect response must not carry an "object" key`)
	}
	if _, present := top["data"]; present {
		t.Fatalf(`Codex-dialect response must not carry a "data" key`)
	}

	// Required-field presence check, per entry, against the raw JSON keys
	// (not merely "valid JSON" - proves every field Codex 0.155's
	// ModelInfo requires is actually present).
	var rawModels []map[string]json.RawMessage
	rawTop := map[string]json.RawMessage{}
	if err := json.Unmarshal(rec.Body.Bytes(), &rawTop); err != nil {
		t.Fatalf("decode raw top-level: %v", err)
	}
	if err := json.Unmarshal(rawTop["models"], &rawModels); err != nil {
		t.Fatalf("decode raw models array: %v", err)
	}
	for i, rawModel := range rawModels {
		for _, field := range codexRequiredModelInfoFields {
			if _, present := rawModel[field]; !present {
				t.Fatalf("model %d: missing required field %q (Codex 0.155's ModelInfo has no #[serde(default)] for this field)", i, field)
			}
		}
	}

	// Full decode against the Go compatibility struct matching the
	// required Codex 0.155 ModelInfo JSON contract.
	var decoded codexModelsResponseCompat
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response does not match the Codex 0.155 ModelsResponse contract: %v", err)
	}
	if len(decoded.Models) == 0 {
		t.Fatalf("expected at least one decoded model")
	}
	found := false
	for _, m := range decoded.Models {
		if m.Slug != "gpt-5.6-luna" {
			continue
		}
		found = true
		if m.DisplayName == "" {
			t.Fatalf("expected a non-empty display_name")
		}
		if m.ShellType != "unified_exec" && m.ShellType != "disabled" {
			t.Fatalf("unexpected shell_type: %q", m.ShellType)
		}
		if m.Visibility != "list" && m.Visibility != "hide" && m.Visibility != "none" {
			t.Fatalf("unexpected visibility: %q", m.Visibility)
		}
		if !m.SupportedInAPI {
			t.Fatalf("expected supported_in_api=true for the configured model")
		}
		if len(m.SupportedReasoningLevels) == 0 {
			t.Fatalf("expected at least one supported reasoning level")
		}
		if m.TruncationPolicy.Mode != "bytes" && m.TruncationPolicy.Mode != "tokens" {
			t.Fatalf("unexpected truncation_policy.mode: %q", m.TruncationPolicy.Mode)
		}
	}
	if !found {
		t.Fatalf("expected the configured model gpt-5.6-luna in the Codex catalog, got %+v", decoded.Models)
	}
}

// TestCodexModelCatalog_IdenticalPayload_IdenticalETag proves ETag
// stability: an unchanged catalog always produces the same ETag.
func TestCodexModelCatalog_IdenticalPayload_IdenticalETag(t *testing.T) {
	backend := &fakeInferenceBackend{name: "chatgpt", typ: "chatgpt-subscription", events: simpleTextEvents("resp_1", "ok")}
	reg := newTestRegistry(t, map[string]InferenceBackend{"chatgpt": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "chatgpt"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	var etags []string
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.155.0-alpha.16.3", nil)
		r.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		etags = append(etags, rec.Header().Get("ETag"))
	}
	for i := 1; i < len(etags); i++ {
		if etags[i] != etags[0] {
			t.Fatalf("expected identical ETag across requests for an unchanged catalog, got %v", etags)
		}
	}
}

// TestCodexModelCatalog_EmbeddedJSON_IsWellFormed is a sanity check on the
// embedded catalog file itself, independent of the HTTP handler.
func TestCodexModelCatalog_EmbeddedJSON_IsWellFormed(t *testing.T) {
	var decoded codexModelsResponseCompat
	if err := json.Unmarshal(codexModelCatalogJSON, &decoded); err != nil {
		t.Fatalf("embedded codex_model_catalog.json does not match the Codex 0.155 ModelsResponse contract: %v", err)
	}
	if len(decoded.Models) == 0 {
		t.Fatalf("expected at least one embedded model")
	}
}
