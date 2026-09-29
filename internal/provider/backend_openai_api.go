package provider

import (
	"context"
	"os"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/creditguard"
)

// OpenAIAPIBackend routes through the real, metered OpenAI API. It exists
// so an operator who has explicitly disabled zero-credit mode can still use
// it as a normal, named backend - the provider gateway never selects it
// implicitly, and Registry.Resolve/CheckBackendType refuse to reach it at
// all while zero-credit mode is on (internal/provider/registry.go).
//
// It reuses OpenAICompatibleBackend's Chat Completions translation pointed
// at api.openai.com (or OPENAI_BASE_URL if set), since that's the wire
// OpenAI's own API also speaks - but is a distinct type from
// OpenAICompatibleBackend so Type() correctly reports "openai-api" (a
// metered type) rather than "openai-compatible" (not metered), which is
// exactly the distinction the credit-isolation policy keys off.
type OpenAIAPIBackend struct {
	inner *OpenAICompatibleBackend
	name  string
}

func NewOpenAIAPIBackend(name string, cfg config.ProviderBackendConfig) *OpenAIAPIBackend {
	base := cfg.BaseURL
	if base == "" {
		base = os.Getenv("OPENAI_BASE_URL")
	}
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	keyEnv := cfg.APIKeyEnv
	if keyEnv == "" {
		keyEnv = "OPENAI_API_KEY"
	}
	inner := NewOpenAICompatibleBackend(name, config.ProviderBackendConfig{
		BaseURL:      base,
		APIKeyEnv:    keyEnv,
		Model:        cfg.Model,
		TimeoutSec:   cfg.TimeoutSec,
		ExtraHeaders: cfg.ExtraHeaders,
	})
	return &OpenAIAPIBackend{inner: inner, name: name}
}

func (b *OpenAIAPIBackend) Name() string { return b.name }
func (b *OpenAIAPIBackend) Type() string { return "openai-api" }

func (b *OpenAIAPIBackend) Capabilities() Capabilities {
	c := b.inner.Capabilities()
	c.Reasoning = true
	return c
}

func (b *OpenAIAPIBackend) Health(ctx context.Context) error {
	creditguard.RecordCall(creditguard.BackendOpenAIAPI)
	return b.inner.Health(ctx)
}

func (b *OpenAIAPIBackend) StreamResponse(ctx context.Context, req Request, sink Sink) error {
	// Recorded unconditionally and first, before any network activity, so
	// tests can prove this line is unreachable in zero-credit mode
	// regardless of what happens afterward.
	creditguard.RecordCall(creditguard.BackendOpenAIAPI)
	return b.inner.StreamResponse(ctx, req, sink)
}
