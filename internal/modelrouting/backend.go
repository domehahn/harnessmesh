package modelrouting

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

type ModelRoutingBackend interface {
	Type() string
	Health(ctx context.Context) error
	ConfigureParticipant(cfg *config.AgentConfig) error
	RouteMetadata(route string) map[string]any
	Usage() map[string]any
	Diagnostics() string
}

// SwitchyardBackend integrates with NVIDIA NeMo Switchyard model routing plane.
type SwitchyardBackend struct {
	BaseURL     string
	HealthCheck bool
	HTTPClient  *http.Client
	mu          sync.RWMutex
	stats       map[string]any
}

func NewSwitchyardBackend(baseURL string, healthCheck bool) *SwitchyardBackend {
	return &SwitchyardBackend{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		HealthCheck: healthCheck,
		HTTPClient:  &http.Client{Timeout: 5 * time.Second},
		stats:       make(map[string]any),
	}
}

func (s *SwitchyardBackend) Type() string {
	return "switchyard"
}

func (s *SwitchyardBackend) Health(ctx context.Context) error {
	if s.BaseURL == "" {
		return &protocol.SwitchyardUnavailableError{
			URL:    s.BaseURL,
			Reason: "switchyard base_url is not configured",
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/v1/models", nil)
	if err != nil {
		return &protocol.SwitchyardUnavailableError{
			URL:    s.BaseURL,
			Reason: fmt.Sprintf("create health request: %v", err),
		}
	}

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return &protocol.SwitchyardUnavailableError{
			URL:    s.BaseURL,
			Reason: fmt.Sprintf("unreachable: %v", err),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return &protocol.SwitchyardUnavailableError{
			URL:    s.BaseURL,
			Reason: fmt.Sprintf("unexpected HTTP status %d", resp.StatusCode),
		}
	}

	return nil
}

func (s *SwitchyardBackend) ConfigureParticipant(cfg *config.AgentConfig) error {
	cfg.UseSwitchyard = true
	if cfg.ModelRouting != nil && cfg.ModelRouting.Route != "" {
		cfg.SwitchyardRouteID = cfg.ModelRouting.Route
	}
	if cfg.Env == nil {
		cfg.Env = make(map[string]string)
	}
	cfg.Env["OPENAI_BASE_URL"] = s.BaseURL + "/v1"
	cfg.Env["ANTHROPIC_BASE_URL"] = s.BaseURL
	return nil
}

func (s *SwitchyardBackend) RouteMetadata(route string) map[string]any {
	return map[string]any{
		"backend":  "switchyard",
		"route":    route,
		"base_url": s.BaseURL,
	}
}

func (s *SwitchyardBackend) Usage() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]any, len(s.stats))
	for k, v := range s.stats {
		out[k] = v
	}
	return out
}

func (s *SwitchyardBackend) Diagnostics() string {
	return fmt.Sprintf("NVIDIA NeMo Switchyard at %s (health_check: %t)", s.BaseURL, s.HealthCheck)
}

// FixedBackend represents direct, unproxied model routing.
type FixedBackend struct{}

func (f *FixedBackend) Type() string                                       { return "fixed" }
func (f *FixedBackend) Health(ctx context.Context) error                   { return nil }
func (f *FixedBackend) ConfigureParticipant(cfg *config.AgentConfig) error { return nil }
func (f *FixedBackend) RouteMetadata(route string) map[string]any {
	return map[string]any{"backend": "fixed", "route": route}
}
func (f *FixedBackend) Usage() map[string]any { return map[string]any{} }
func (f *FixedBackend) Diagnostics() string   { return "direct fixed model routing" }

// ExternalBackend represents an externally managed model router (e.g. GitHub Copilot).
type ExternalBackend struct{}

func (e *ExternalBackend) Type() string                                       { return "external" }
func (e *ExternalBackend) Health(ctx context.Context) error                   { return nil }
func (e *ExternalBackend) ConfigureParticipant(cfg *config.AgentConfig) error { return nil }
func (e *ExternalBackend) RouteMetadata(route string) map[string]any {
	return map[string]any{"backend": "external", "route": route}
}
func (e *ExternalBackend) Usage() map[string]any { return map[string]any{} }
func (e *ExternalBackend) Diagnostics() string {
	return "external opaque model routing (e.g. Copilot CLI)"
}

// UnknownBackend represents an unsupported backend.
type UnknownBackend struct {
	BackendName string
}

func (u *UnknownBackend) Type() string { return "unknown" }
func (u *UnknownBackend) Health(ctx context.Context) error {
	return &protocol.ModelRoutingUnavailableError{
		Backend: u.BackendName,
		Reason:  fmt.Sprintf("unknown or unsupported model routing backend %q", u.BackendName),
	}
}
func (u *UnknownBackend) ConfigureParticipant(cfg *config.AgentConfig) error {
	return &protocol.ModelRoutingUnavailableError{
		Backend: u.BackendName,
		Reason:  fmt.Sprintf("unknown or unsupported model routing backend %q", u.BackendName),
	}
}
func (u *UnknownBackend) RouteMetadata(route string) map[string]any {
	return map[string]any{"backend": "unknown", "route": route}
}
func (u *UnknownBackend) Usage() map[string]any { return map[string]any{} }
func (u *UnknownBackend) Diagnostics() string {
	return fmt.Sprintf("unsupported model routing backend: %s", u.BackendName)
}

// BuildRegistry resolves all configured ModelRoutingBackends from a configuration.
func BuildRegistry(cfg *config.Config) map[string]ModelRoutingBackend {
	backends := make(map[string]ModelRoutingBackend)

	// Built-in fixed and external
	backends["fixed"] = &FixedBackend{}
	backends["external"] = &ExternalBackend{}

	// Legacy switchyard config
	if cfg.Switchyard.Enabled && cfg.Switchyard.BaseURL != "" {
		backends["switchyard"] = NewSwitchyardBackend(cfg.Switchyard.BaseURL, true)
	}

	// v2 model_routing_backends map
	for name, bCfg := range cfg.ModelRoutingBackends {
		switch strings.ToLower(bCfg.Type) {
		case "switchyard":
			backends[name] = NewSwitchyardBackend(bCfg.BaseURL, bCfg.HealthCheck)
		case "fixed":
			backends[name] = &FixedBackend{}
		case "external":
			backends[name] = &ExternalBackend{}
		default:
			backends[name] = &UnknownBackend{BackendName: name}
		}
	}

	return backends
}
