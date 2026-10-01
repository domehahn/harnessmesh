package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/domehahn/harnessmesh/internal/creditguard"
)

func TestParseV1Config(t *testing.T) {
	raw := []byte(`{
		"schema_version": 1,
		"workflow": {
			"executor": "claude",
			"reviewer": "codex",
			"max_rounds": 4
		},
		"agents": {
			"claude": {
				"kind": "claude"
			},
			"codex": {
				"kind": "codex"
			}
		}
	}`)

	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if cfg.Version != 1 {
		t.Fatalf("expected version 1, got %d", cfg.Version)
	}
	if cfg.Collaboration.MaxPeerRounds != 4 {
		t.Fatalf("expected max_peer_rounds 4, got %d", cfg.Collaboration.MaxPeerRounds)
	}
	if cfg.Collaboration.MaxPeerDepth != 2 {
		t.Fatalf("expected max_peer_depth 2, got %d", cfg.Collaboration.MaxPeerDepth)
	}
	if !cfg.Agents["claude"].Writable {
		t.Fatal("expected claude executor to default to writable")
	}
	if cfg.Agents["codex"].Writable {
		t.Fatal("expected codex reviewer to be non-writable")
	}
}

func TestParseV2Config(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"collaboration": {
			"max_peer_rounds": 5,
			"max_peer_depth": 3,
			"max_peer_calls": 12,
			"session_timeout": "30m"
		},
		"context": {
			"max_context_chars": 80000,
			"allowed_paths": ["internal/**"]
		},
		"agents": {
			"claude": {
				"adapter": "claude-code",
				"role": "executor",
				"writable": true
			},
			"codex": {
				"adapter": "codex",
				"role": "reviewer",
				"writable": false
			}
		}
	}`)

	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if cfg.Version != 2 {
		t.Fatalf("expected version 2, got %d", cfg.Version)
	}
	if cfg.Collaboration.MaxPeerDepth != 3 {
		t.Fatalf("expected max depth 3, got %d", cfg.Collaboration.MaxPeerDepth)
	}
	if cfg.Collaboration.SessionTimeoutDuration().Minutes() != 30 {
		t.Fatalf("expected 30m timeout, got %v", cfg.Collaboration.SessionTimeoutDuration())
	}
}

func TestSingleWriterConstraint(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {
			"claude": {
				"adapter": "claude-code",
				"writable": true
			},
			"codex": {
				"adapter": "codex",
				"writable": true
			}
		}
	}`)

	_, err := Parse(raw)
	if err == nil {
		t.Fatal("expected single-writer invariant violation error, got nil")
	}
	if !strings.Contains(err.Error(), "single-writer invariant violated") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCreditIsolation_RejectsMeteredAdapterOnExternalAgent(t *testing.T) {
	os.Unsetenv(creditguard.EnvVar)
	raw := []byte(`{
		"version": 2,
		"agents": {
			"claude-executor": {
				"adapter": "claude-code",
				"writable": true
			},
			"chatgpt-browser": {
				"adapter": "openai-api",
				"execution_mode": "external",
				"writable": false
			}
		}
	}`)

	_, err := Parse(raw)
	if err == nil {
		t.Fatal("expected credit isolation violation, got nil")
	}
	if !strings.Contains(err.Error(), "ChatGPTCreditIsolationViolation") {
		t.Fatalf("expected ChatGPTCreditIsolationViolation, got: %v", err)
	}
}

func TestCreditIsolation_AllowsPassiveAdapterOnExternalAgent(t *testing.T) {
	os.Unsetenv(creditguard.EnvVar)
	raw := []byte(`{
		"version": 2,
		"agents": {
			"claude-executor": {
				"adapter": "claude-code",
				"writable": true
			},
			"chatgpt-browser": {
				"adapter": "mcp-remote",
				"execution_mode": "external",
				"writable": false
			}
		}
	}`)

	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("expected mcp-remote external agent to pass validation, got: %v", err)
	}
	if cfg.Agents["chatgpt-browser"].IsExternal() != true {
		t.Fatalf("expected chatgpt-browser to remain external")
	}
}

func TestMigrateV1ToV2(t *testing.T) {
	v1Raw := []byte(`{
		"schema_version": 1,
		"workflow": {
			"executor": "claude",
			"reviewer": "codex"
		},
		"agents": {
			"claude": {
				"kind": "claude",
				"role": "executor"
			},
			"codex": {
				"kind": "codex",
				"role": "reviewer"
			}
		}
	}`)

	v2Bytes, err := MigrateV1ToV2(v1Raw)
	if err != nil {
		t.Fatalf("MigrateV1ToV2 failed: %v", err)
	}

	cfg, err := Parse(v2Bytes)
	if err != nil {
		t.Fatalf("failed to parse migrated config: %v", err)
	}

	if cfg.Version != 2 {
		t.Errorf("expected version 2, got %d", cfg.Version)
	}
	if !cfg.Workspace.SingleWriter {
		t.Errorf("expected single writer true")
	}
	if !cfg.Agents["codex"].HasRole("reviewer") {
		t.Errorf("expected codex to have reviewer role")
	}
}

func TestConfigV2RolesAndRouting(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {
			"main": {
				"adapter": "antigravity",
				"roles": ["executor"],
				"writable": true
			},
			"sec": {
				"adapter": "copilot-cli",
				"roles": ["security", "dependencies"],
				"writable": false
			}
		},
		"capability_routing": {
			"security": ["sec"]
		}
	}`)

	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if !cfg.Agents["sec"].HasRole("security") {
		t.Errorf("expected sec to have security role")
	}
	if !cfg.Agents["sec"].HasRole("dependencies") {
		t.Errorf("expected sec to have dependencies role")
	}
	if len(cfg.CapabilityRouting["security"]) != 1 || cfg.CapabilityRouting["security"][0] != "sec" {
		t.Errorf("expected capability_routing security to map to [sec]")
	}
}

func TestProviderGateway_ZeroCreditMode_RejectsMeteredDefaultBackend(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {"claude": {"adapter": "claude-code", "writable": true}},
		"provider": {
			"enabled": true,
			"default_backend": "openai",
			"backends": {"openai": {"type": "openai-api"}}
		}
	}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected zero-credit mode (the default) to reject a default_backend resolving to a metered type")
	} else if !strings.Contains(err.Error(), "zero_credit_mode forbids") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestProviderGateway_ZeroCreditMode_RejectsMeteredFallback(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {"claude": {"adapter": "claude-code", "writable": true}},
		"provider": {
			"enabled": true,
			"default_backend": "local",
			"backends": {
				"local": {"type": "openai-compatible", "base_url": "http://127.0.0.1:8000/v1"},
				"codex": {"type": "codex"}
			},
			"fallback": {"enabled": true, "order": ["codex"]}
		}
	}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected zero-credit mode to reject a fallback order entry resolving to a metered type")
	}
}

func TestProviderGateway_ZeroCreditMode_ExplicitlyDisabled_AllowsMeteredBackend(t *testing.T) {
	falseVal := false
	raw, err := json.Marshal(map[string]any{
		"version": 2,
		"agents":  map[string]any{"claude": map[string]any{"adapter": "claude-code", "writable": true}},
		"provider": map[string]any{
			"enabled":          true,
			"zero_credit_mode": falseVal,
			"default_backend":  "openai",
			"backends":         map[string]any{"openai": map[string]any{"type": "openai-api"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("expected explicit zero_credit_mode=false to allow a metered default backend, got: %v", err)
	}
	if cfg.Provider.IsZeroCreditMode() {
		t.Fatalf("expected IsZeroCreditMode() to report false")
	}
}

func TestProviderGateway_OpenAICompatibleRequiresBaseURL(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {"claude": {"adapter": "claude-code", "writable": true}},
		"provider": {
			"enabled": true,
			"default_backend": "local",
			"backends": {"local": {"type": "openai-compatible"}}
		}
	}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected an openai-compatible backend with no base_url to be rejected")
	}
}

func TestProviderGateway_DefaultBackendMustExist(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {"claude": {"adapter": "claude-code", "writable": true}},
		"provider": {
			"enabled": true,
			"default_backend": "does-not-exist",
			"backends": {"local": {"type": "openai-compatible", "base_url": "http://127.0.0.1:8000/v1"}}
		}
	}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected a default_backend referencing an undefined backend to be rejected")
	}
}

func TestProviderGateway_ValidZeroCreditConfig_Passes(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {"claude": {"adapter": "claude-code", "writable": true}},
		"provider": {
			"enabled": true,
			"token": "test-token",
			"default_backend": "local",
			"backends": {
				"local": {"type": "openai-compatible", "base_url": "http://127.0.0.1:8000/v1"},
				"openai": {"type": "openai-api"},
				"codex": {"type": "codex"}
			},
			"denied_backend_types": ["openai-api", "codex"]
		}
	}`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("expected a well-formed zero-credit config to pass validation, got: %v", err)
	}
	if !cfg.Provider.IsZeroCreditMode() {
		t.Fatalf("expected zero-credit mode to default to true")
	}
}

func TestProviderGateway_ZeroAPIBillingMode_IsCanonicalAndLegacyAliasWorks(t *testing.T) {
	raw := []byte(`{"version":2,"agents":{"placeholder":{"kind":"fake","role":"executor","writable":true}},"provider":{"enabled":true,"zero_api_billing_mode":true,"default_backend":"local","backends":{"local":{"type":"openai-compatible","base_url":"http://127.0.0.1:8000/v1"}}}}`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("canonical mode should parse: %v", err)
	}
	if !cfg.Provider.IsZeroAPIBillingMode() || !cfg.Provider.IsZeroCreditMode() {
		t.Fatal("expected canonical and compatibility accessors to report enabled")
	}
	legacy := []byte(`{"version":2,"agents":{"placeholder":{"kind":"fake","role":"executor","writable":true}},"provider":{"enabled":true,"zero_credit_mode":false,"default_backend":"local","backends":{"local":{"type":"openai-compatible","base_url":"http://127.0.0.1:8000/v1"}}}}`)
	cfg, err = Parse(legacy)
	if err != nil || cfg.Provider.IsZeroAPIBillingMode() {
		t.Fatalf("legacy alias should remain compatible, cfg=%v err=%v", cfg, err)
	}
}

func TestProviderGateway_UnsafeListenAndUnknownBackendFailFast(t *testing.T) {
	raw := []byte(`{"version":2,"agents":{"placeholder":{"kind":"fake","role":"executor","writable":true}},"provider":{"enabled":true,"listen":"0.0.0.0:8789","default_backend":"local","backends":{"local":{"type":"future-backend","base_url":"http://127.0.0.1:8000/v1"}}}}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected unsafe listen and unknown backend configuration to fail fast")
	}
}

// TestProviderGateway_StillRequiresAtLeastOneAgent proves the "at least one
// agent" requirement is deliberately NOT relaxed for provider-enabled
// configs (self-review caught a real gap here: relaxing it would let a
// zero-agent, provider.enabled=true config also be silently accepted by
// `mcp serve`/`bridge serve` if reused there, under which
// MeshCommitCoordinator.requireWritableExecutor treats "no agents
// registered" as permissive-by-design and would fail open for every
// participant name). A provider-only deployment must define at least one
// placeholder agent - see configs/codex-provider.example.json.
func TestProviderGateway_StillRequiresAtLeastOneAgent(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"provider": {
			"enabled": true,
			"token": "test-token",
			"default_backend": "local",
			"backends": {"local": {"type": "openai-compatible", "base_url": "http://127.0.0.1:8000/v1"}}
		}
	}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected a zero-agent config to still be rejected even with provider.enabled=true")
	}
}

func TestProviderGateway_OnlyProviderEnabled_WithPlaceholderAgent(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {"placeholder": {"kind": "fake", "role": "executor", "writable": true}},
		"provider": {
			"enabled": true,
			"token": "test-token",
			"default_backend": "local",
			"backends": {"local": {"type": "openai-compatible", "base_url": "http://127.0.0.1:8000/v1"}}
		}
	}`)
	if _, err := Parse(raw); err != nil {
		t.Fatalf("expected a provider-only config with a placeholder agent to be valid, got: %v", err)
	}
}

func TestProviderGateway_RequiresDefaultBackend(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {"placeholder": {"kind": "fake", "role": "executor", "writable": true}},
		"provider": {"enabled": true, "token": "test-token"}
	}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected provider.enabled=true with no default_backend to be rejected")
	}
}

func TestProviderGateway_RequiresAtLeastOneBackend(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {"placeholder": {"kind": "fake", "role": "executor", "writable": true}},
		"provider": {"enabled": true, "token": "test-token", "default_backend": "local"}
	}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected provider.enabled=true with no backends defined to be rejected")
	}
}

func TestProviderGateway_DefaultBackendCannotBeExplicitlyDenied(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {"placeholder": {"kind": "fake", "role": "executor", "writable": true}},
		"provider": {
			"enabled": true,
			"token": "test-token",
			"default_backend": "local",
			"backends": {"local": {"type": "openai-compatible", "base_url": "http://127.0.0.1:8000/v1"}},
			"denied_backend_types": ["openai-compatible"]
		}
	}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected a default_backend whose type is in denied_backend_types to be rejected at config-load time")
	}
}

func TestProviderGateway_DefaultBackendMustBeInAllowedTypes(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {"placeholder": {"kind": "fake", "role": "executor", "writable": true}},
		"provider": {
			"enabled": true,
			"token": "test-token",
			"default_backend": "local",
			"backends": {"local": {"type": "bedrock", "base_url": "us-east-1"}},
			"allowed_backend_types": ["openai-compatible"]
		}
	}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected a default_backend whose type is not in allowed_backend_types to be rejected at config-load time")
	}
}

func TestProviderGateway_OpenAICompatibleTypeCheckIsCaseInsensitive(t *testing.T) {
	raw := []byte(`{
		"version": 2,
		"agents": {"placeholder": {"kind": "fake", "role": "executor", "writable": true}},
		"provider": {
			"enabled": true,
			"token": "test-token",
			"default_backend": "local",
			"backends": {"local": {"type": "OpenAI-Compatible"}}
		}
	}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected a mixed-case \"OpenAI-Compatible\" type with no base_url to still be rejected (base_url requirement must be case-insensitive)")
	}
}
