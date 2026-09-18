package modelrouting

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestSwitchyardBackend_HealthAndConfigure(t *testing.T) {
	backend := NewSwitchyardBackend("http://switchyard.local", true)
	backend.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/v1/models" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-4o"}]}`)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     make(http.Header),
			}, nil
		}),
	}
	if backend.Type() != "switchyard" {
		t.Fatalf("expected type switchyard, got %s", backend.Type())
	}

	ctx := context.Background()
	if err := backend.Health(ctx); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	// 2. Configure participant
	agentCfg := config.AgentConfig{
		Kind: "codex",
		ModelRouting: &config.AgentModelRoutingConfig{
			Type:  "switchyard",
			Route: "architecture",
		},
	}
	if err := backend.ConfigureParticipant(&agentCfg); err != nil {
		t.Fatalf("ConfigureParticipant failed: %v", err)
	}

	if !agentCfg.UseSwitchyard || agentCfg.SwitchyardRouteID != "architecture" {
		t.Fatalf("unexpected agentCfg after config: %+v", agentCfg)
	}
	if agentCfg.Env["OPENAI_BASE_URL"] != "http://switchyard.local/v1" {
		t.Fatalf("unexpected OPENAI_BASE_URL: %s", agentCfg.Env["OPENAI_BASE_URL"])
	}

	meta := backend.RouteMetadata("architecture")
	if meta["backend"] != "switchyard" || meta["route"] != "architecture" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
}

func TestSwitchyardBackend_Unreachable(t *testing.T) {
	backend := NewSwitchyardBackend("http://switchyard.local", true)
	backend.HTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}),
	}
	ctx := context.Background()
	err := backend.Health(ctx)
	if err == nil {
		t.Fatal("expected error for unreachable Switchyard server, got nil")
	}

	var syErr *protocol.SwitchyardUnavailableError
	if !errors.As(err, &syErr) {
		t.Fatalf("expected SwitchyardUnavailableError, got %T: %v", err, err)
	}
}

func TestBuildRegistry(t *testing.T) {
	cfg := &config.Config{
		Version: 2,
		ModelRoutingBackends: map[string]config.ModelRoutingBackendConfig{
			"sy-local": {
				Type:        "switchyard",
				BaseURL:     "http://127.0.0.1:4000",
				HealthCheck: true,
			},
			"direct": {
				Type: "fixed",
			},
			"copilot-ext": {
				Type: "external",
			},
		},
	}

	registry := BuildRegistry(cfg)
	if len(registry) < 3 {
		t.Fatalf("expected at least 3 backends in registry, got %d", len(registry))
	}

	if registry["sy-local"].Type() != "switchyard" {
		t.Errorf("expected switchyard, got %s", registry["sy-local"].Type())
	}
	if registry["direct"].Type() != "fixed" {
		t.Errorf("expected fixed, got %s", registry["direct"].Type())
	}
	if registry["copilot-ext"].Type() != "external" {
		t.Errorf("expected external, got %s", registry["copilot-ext"].Type())
	}
}
