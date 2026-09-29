package provider

import (
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
)

// newRegistryForTest builds a Registry directly from already-constructed
// fakeInferenceBackends, bypassing NewRegistry's config.ProviderBackendConfig
// -driven construction (fakes have no such config to build from). It still
// exercises the same Policy enforcement Resolve/FallbackChain apply.
func newRegistryForTest(backends map[string]InferenceBackend, policy Policy) *Registry {
	reg := &Registry{policy: policy, backends: map[string]InferenceBackend{}, backendCfg: map[string]config.ProviderBackendConfig{}}
	for name, b := range backends {
		reg.backends[name] = b
		reg.backendCfg[name] = config.ProviderBackendConfig{Type: b.Type()}
	}
	return reg
}

func TestRegistry_ResolveDefaultBackend(t *testing.T) {
	local := &fakeInferenceBackend{name: "local", typ: "openai-compatible"}
	reg := newRegistryForTest(map[string]InferenceBackend{"local": local}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})

	b, err := reg.Resolve("")
	if err != nil {
		t.Fatalf("Resolve(\"\"): %v", err)
	}
	if b.Name() != "local" {
		t.Fatalf("expected default backend 'local', got %q", b.Name())
	}
}

func TestRegistry_ResolveUnknownBackend(t *testing.T) {
	reg := newRegistryForTest(map[string]InferenceBackend{}, Policy{ZeroCreditMode: true})
	if _, err := reg.Resolve("does-not-exist"); err == nil {
		t.Fatalf("expected an error resolving an unknown backend")
	} else if _, ok := err.(*BackendUnavailableError); !ok {
		t.Fatalf("expected BackendUnavailableError, got %T", err)
	}
}

func TestRegistry_NoDefaultAndNoNameIsDenied(t *testing.T) {
	reg := newRegistryForTest(map[string]InferenceBackend{}, Policy{ZeroCreditMode: true})
	if _, err := reg.Resolve(""); err == nil {
		t.Fatalf("expected an error when no backend name and no default_backend are configured")
	}
}

func TestRegistry_FallbackChain_SkipsForbiddenEntries(t *testing.T) {
	reg := newRegistryForTest(map[string]InferenceBackend{
		"local":  &fakeInferenceBackend{name: "local", typ: "openai-compatible"},
		"openai": &fakeInferenceBackend{name: "openai", typ: "openai-api"},
	}, Policy{ZeroCreditMode: true, DefaultBackend: "local", FallbackEnabled: true, FallbackOrder: []string{"openai", "local"}})

	chain := reg.FallbackChain()
	for _, name := range chain {
		if name == "openai" {
			t.Fatalf("expected the metered 'openai' backend to be skipped from the fallback chain under zero-credit mode, got chain=%v", chain)
		}
	}
}

func TestRegistry_FallbackDisabled_ReturnsEmptyChain(t *testing.T) {
	reg := newRegistryForTest(map[string]InferenceBackend{
		"local": &fakeInferenceBackend{name: "local", typ: "openai-compatible"},
	}, Policy{ZeroCreditMode: true, DefaultBackend: "local", FallbackEnabled: false, FallbackOrder: []string{"local"}})

	if chain := reg.FallbackChain(); len(chain) != 0 {
		t.Fatalf("expected empty fallback chain when fallback is disabled, got %v", chain)
	}
}

func TestNewRegistry_SelfRecursionDetected(t *testing.T) {
	cfg := config.ProviderGatewayConfig{
		Backends: map[string]config.ProviderBackendConfig{
			"loopy": {Type: "openai-compatible", BaseURL: "http://127.0.0.1:8789/v1"},
		},
	}
	if _, err := NewRegistry(cfg, "127.0.0.1:8789"); err == nil {
		t.Fatalf("expected NewRegistry to reject a backend base_url pointing back at the gateway's own listen port")
	}
}

func TestNewRegistry_SelfRecursionDetected_Localhost(t *testing.T) {
	cfg := config.ProviderGatewayConfig{
		Backends: map[string]config.ProviderBackendConfig{
			"loopy": {Type: "openai-compatible", BaseURL: "http://localhost:8789/v1"},
		},
	}
	if _, err := NewRegistry(cfg, "127.0.0.1:8789"); err == nil {
		t.Fatalf("expected NewRegistry to reject a localhost backend base_url on the gateway's own port")
	}
}

func TestNewRegistry_DifferentPortIsFine(t *testing.T) {
	cfg := config.ProviderGatewayConfig{
		Backends: map[string]config.ProviderBackendConfig{
			"local": {Type: "openai-compatible", BaseURL: "http://127.0.0.1:8000/v1"},
		},
	}
	reg, err := NewRegistry(cfg, "127.0.0.1:8789")
	if err != nil {
		t.Fatalf("expected a backend on a different port to be accepted, got %v", err)
	}
	if reg == nil {
		t.Fatalf("expected a non-nil registry")
	}
}

func TestNewRegistry_RemoteBackendOnSamePortIsFine(t *testing.T) {
	cfg := config.ProviderGatewayConfig{
		Backends: map[string]config.ProviderBackendConfig{
			"remote": {Type: "openai-compatible", BaseURL: "http://example.com:8789/v1"},
		},
	}
	if _, err := NewRegistry(cfg, "127.0.0.1:8789"); err != nil {
		t.Fatalf("expected a genuinely remote host sharing a numeric port to be accepted (not a real loop), got %v", err)
	}
}

func TestPolicy_CheckBackendType(t *testing.T) {
	strict := Policy{ZeroCreditMode: true}
	if err := strict.CheckBackendType("x", "openai-api"); err == nil {
		t.Fatalf("expected openai-api to be denied under zero-credit mode")
	}
	if err := strict.CheckBackendType("x", "codex"); err == nil {
		t.Fatalf("expected codex to be denied under zero-credit mode")
	}
	if err := strict.CheckBackendType("x", "openai-compatible"); err != nil {
		t.Fatalf("expected openai-compatible to be allowed under zero-credit mode, got %v", err)
	}

	off := Policy{ZeroCreditMode: false}
	if err := off.CheckBackendType("x", "openai-api"); err != nil {
		t.Fatalf("expected openai-api to be allowed when zero-credit mode is off, got %v", err)
	}

	denyList := Policy{ZeroCreditMode: false, DeniedBackendTypes: toSet([]string{"bedrock"})}
	if err := denyList.CheckBackendType("x", "bedrock"); err == nil {
		t.Fatalf("expected explicitly denied backend type to be rejected")
	}

	allowList := Policy{ZeroCreditMode: false, AllowedBackendTypes: toSet([]string{"openai-compatible"})}
	if err := allowList.CheckBackendType("x", "bedrock"); err == nil {
		t.Fatalf("expected a non-allowlisted backend type to be rejected when an allowlist is set")
	}
	if err := allowList.CheckBackendType("x", "openai-compatible"); err != nil {
		t.Fatalf("expected the allowlisted type to pass, got %v", err)
	}
}

func TestIsMeteredBackendType(t *testing.T) {
	if !IsMeteredBackendType("openai-api") || !IsMeteredBackendType("OpenAI-API") {
		t.Fatalf("expected openai-api to be metered (case-insensitive)")
	}
	if !IsMeteredBackendType("codex") {
		t.Fatalf("expected codex to be metered")
	}
	if IsMeteredBackendType("openai-compatible") || IsMeteredBackendType("bedrock") {
		t.Fatalf("expected openai-compatible and bedrock to not be metered")
	}
}
