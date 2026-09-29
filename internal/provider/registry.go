package provider

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/domehahn/harnessmesh/internal/config"
)

// meteredBackendTypes mirrors internal/config's meteredProviderBackendTypes
// (kept independent to avoid an import cycle - config validates the static
// configuration shape at load time; this package enforces the same rule
// again at registry-build and per-request time, as defense in depth).
var meteredBackendTypes = map[string]bool{
	"openai-api": true,
	"codex":      true,
}

// IsMeteredBackendType reports whether a provider backend type resolves to
// a metered LLM backend (the real OpenAI API, or Codex used as an
// inference engine).
func IsMeteredBackendType(t string) bool {
	return meteredBackendTypes[strings.ToLower(strings.TrimSpace(t))]
}

// Policy is the resolved, effective zero-credit/routing policy for one
// provider gateway instance.
type Policy struct {
	ZeroCreditMode      bool
	DefaultBackend      string
	AllowedBackendTypes map[string]bool // empty = no allowlist restriction beyond zero-credit
	DeniedBackendTypes  map[string]bool
	FallbackEnabled     bool
	FallbackOrder       []string
}

func NewPolicy(cfg config.ProviderGatewayConfig) Policy {
	p := Policy{
		ZeroCreditMode:      cfg.IsZeroCreditMode(),
		DefaultBackend:      cfg.DefaultBackend,
		AllowedBackendTypes: toSet(cfg.AllowedBackendTypes),
		DeniedBackendTypes:  toSet(cfg.DeniedBackendTypes),
		FallbackEnabled:     cfg.Fallback.Enabled,
		FallbackOrder:       cfg.Fallback.Order,
	}
	return p
}

func toSet(ss []string) map[string]bool {
	out := make(map[string]bool, len(ss))
	for _, s := range ss {
		out[strings.ToLower(strings.TrimSpace(s))] = true
	}
	return out
}

// CheckBackendType enforces policy for resolving to a backend of type t,
// named name (for error messages). This is the single choke point every
// request-time backend resolution must pass through - there is no other
// way to reach StreamResponse on a configured backend.
func (p Policy) CheckBackendType(name, t string) error {
	lt := strings.ToLower(strings.TrimSpace(t))
	if p.ZeroCreditMode && meteredBackendTypes[lt] {
		return &MeteredBackendDeniedError{Backend: name, Reason: fmt.Sprintf("backend type %q is metered and zero_credit_mode is enabled", t)}
	}
	if p.DeniedBackendTypes[lt] {
		return &ProviderPolicyDeniedError{Reason: fmt.Sprintf("backend type %q is explicitly denied", t)}
	}
	if len(p.AllowedBackendTypes) > 0 && !p.AllowedBackendTypes[lt] {
		return &ProviderPolicyDeniedError{Reason: fmt.Sprintf("backend type %q is not in allowed_backend_types", t)}
	}
	return nil
}

// Registry holds constructed backends and resolves routing.
type Registry struct {
	policy     Policy
	backends   map[string]InferenceBackend
	backendCfg map[string]config.ProviderBackendConfig
}

// NewRegistry constructs every configured backend eagerly (so misconfiguration
// surfaces at startup, not on the first request) and validates the
// loopback-recursion guard (section 37): no openai-compatible backend may
// point back at this gateway's own listen address.
func NewRegistry(cfg config.ProviderGatewayConfig, listenAddr string) (*Registry, error) {
	policy := NewPolicy(cfg)
	reg := &Registry{policy: policy, backends: map[string]InferenceBackend{}, backendCfg: map[string]config.ProviderBackendConfig{}}

	for name, bCfg := range cfg.Backends {
		// A denied/metered backend is still constructed and registered here
		// (so an explicit, non-default reference to it produces a clear
		// MeteredBackendDenied/ProviderPolicyDenied error at request time
		// via Resolve, rather than "unknown backend") - policy is enforced
		// at Resolve and FallbackChain, never by skipping construction.
		if err := checkSelfRecursion(listenAddr, bCfg.BaseURL); err != nil {
			return nil, fmt.Errorf("backend %q: %w", name, err)
		}
		backend, err := buildBackend(name, bCfg)
		if err != nil {
			return nil, fmt.Errorf("backend %q: %w", name, err)
		}
		reg.backends[name] = backend
		reg.backendCfg[name] = bCfg
	}

	return reg, nil
}

func buildBackend(name string, bCfg config.ProviderBackendConfig) (InferenceBackend, error) {
	switch strings.ToLower(bCfg.Type) {
	case "openai-compatible":
		return NewOpenAICompatibleBackend(name, bCfg), nil
	case "openai-api":
		return NewOpenAIAPIBackend(name, bCfg), nil
	case "codex":
		return NewCodexInferenceBackend(name, bCfg), nil
	case "bedrock":
		return NewBedrockBackend(name, bCfg), nil
	case "chatgpt-subscription":
		return NewSubscriptionBackend(name), nil
	default:
		return nil, fmt.Errorf("unsupported provider backend type %q", bCfg.Type)
	}
}

// checkSelfRecursion detects a configured backend base_url that would send
// requests back into this same gateway process, which would otherwise loop
// forever (Codex -> HarnessMesh -> "backend" -> HarnessMesh -> ...).
func checkSelfRecursion(listenAddr, backendBaseURL string) error {
	if listenAddr == "" || backendBaseURL == "" {
		return nil
	}
	u, err := url.Parse(backendBaseURL)
	if err != nil || u.Host == "" {
		return nil
	}
	_, listenPort, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return nil
	}
	backendHost, backendPort, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil
	}
	if backendPort != listenPort {
		return nil
	}
	if isLoopbackOrWildcard(backendHost) {
		return fmt.Errorf("base_url %q targets this gateway's own listen port %s - this would create an infinite request loop", backendBaseURL, listenPort)
	}
	return nil
}

func isLoopbackOrWildcard(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0", "":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Resolve returns the backend named name, after re-checking policy (defense
// in depth: policy could theoretically differ between startup and request
// time only via ResolveMode's env-var override, which this re-check
// correctly picks up).
func (r *Registry) Resolve(name string) (InferenceBackend, error) {
	if name == "" {
		name = r.policy.DefaultBackend
	}
	if name == "" {
		return nil, &ProviderPolicyDeniedError{Reason: "no backend specified and no default_backend configured"}
	}
	bCfg, exists := r.backendCfg[name]
	if !exists {
		return nil, &BackendUnavailableError{Backend: name, Reason: "not configured"}
	}
	if err := r.policy.CheckBackendType(name, bCfg.Type); err != nil {
		return nil, err
	}
	backend, exists := r.backends[name]
	if !exists {
		return nil, &BackendUnavailableError{Backend: name, Reason: "not registered"}
	}
	return backend, nil
}

// FallbackChain returns the ordered list of backend names to try after the
// primary selection fails, honoring policy (an entry that policy would deny
// is skipped, never silently escalated to bypass zero-credit mode).
func (r *Registry) FallbackChain() []string {
	if !r.policy.FallbackEnabled {
		return nil
	}
	out := make([]string, 0, len(r.policy.FallbackOrder))
	for _, name := range r.policy.FallbackOrder {
		bCfg, exists := r.backendCfg[name]
		if !exists {
			continue
		}
		if err := r.policy.CheckBackendType(name, bCfg.Type); err != nil {
			continue
		}
		out = append(out, name)
	}
	return out
}

func (r *Registry) Policy() Policy { return r.policy }

func (r *Registry) Backends() map[string]InferenceBackend { return r.backends }
