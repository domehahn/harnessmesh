package provider

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// codexModelCatalogJSON is the exact Codex-native model catalog response
// for GET /v1/models?client_version=... - byte-for-byte the bundled
// "gpt-5.6-luna" ModelInfo entry from the real OpenAI Codex source
// (github.com/openai/codex, tag rust-v0.155.0-alpha.16.3,
// codex-rs/models-manager/models.json), the exact revision matching the
// real installed Codex VS Code extension's reported cli_version
// (0.155.0-alpha.16.3) - not copied from Codex's current main branch,
// which may have since diverged.
//
// ROOT CAUSE this fixes: Codex's ModelsClient does NOT decode the public
// OpenAI /v1/models list shape. It deserializes the response as
// `ModelsResponse { models: Vec<ModelInfo> }`
// (codex-rs/protocol/src/openai_models.rs), a Codex-specific schema with
// many fields the public OpenAI Model object does not have (slug,
// display_name, supported_reasoning_levels, shell_type, visibility,
// supported_in_api, priority, support_verbosity, default_verbosity,
// apply_patch_tool_type, truncation_policy, experimental_supported_tools,
// input_modalities, and more) - several of which
// (supported_reasoning_levels, shell_type, visibility, supported_in_api,
// priority, support_verbosity, truncation_policy,
// experimental_supported_tools) have NO `#[serde(default)]` and are
// therefore REQUIRED on the wire, not merely "valid JSON". This is served
// verbatim (never re-marshaled through a partial Go struct that could
// accidentally omit a required field) so the exact upstream shape is
// guaranteed byte-faithful. See the handleModels doc comment in server.go
// for how HarnessMesh distinguishes this response from the generic
// OpenAI-compatible one.
//
//go:embed codex_model_catalog.json
var codexModelCatalogJSON []byte

// codexModelCatalogETag is a stable ETag derived from the catalog payload
// itself (Codex's ModelsClient reads the ETag response header for
// caching). An identical catalog always produces an identical ETag; any
// change to the catalog payload changes it. Not required by Codex
// 0.155's decoder - this is optional, additive caching support, computed
// once at package init from the embedded (immutable) payload.
var codexModelCatalogETag = func() string {
	sum := sha256.Sum256(codexModelCatalogJSON)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}()
