package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

func init() {
	factory := func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return NewFakeAdapter(name, cfg), nil
	}
	RegisterAdapter("fake", factory)
	RegisterAdapter("mock", factory)
}

type FakeAdapter struct {
	mu           sync.Mutex
	name         string
	adapterType  string
	cfg          config.AgentConfig
	calls        []InvokeRequest
	healthError  error
	customInvoke func(req InvokeRequest) (InvokeResult, error)
	responses    map[string]string
}

func NewFakeAdapter(name string, cfg config.AgentConfig) *FakeAdapter {
	return &FakeAdapter{
		name:        name,
		adapterType: "fake",
		cfg:         cfg,
		responses:   make(map[string]string),
	}
}

func (a *FakeAdapter) SetInvokeHandler(fn func(req InvokeRequest) (InvokeResult, error)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.customInvoke = fn
}

func (a *FakeAdapter) SetHealthError(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.healthError = err
}

func (a *FakeAdapter) SetResponse(promptSubstring, response string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.responses[promptSubstring] = response
}

func (a *FakeAdapter) Calls() []InvokeRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]InvokeRequest, len(a.calls))
	copy(out, a.calls)
	return out
}

func (a *FakeAdapter) ID() string {
	if a.name != "" {
		return a.name
	}
	return "fake"
}

func (a *FakeAdapter) AdapterType() string {
	if a.adapterType != "" {
		return a.adapterType
	}
	return "fake"
}

func (a *FakeAdapter) SetAdapterType(t string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.adapterType = t
}

func (a *FakeAdapter) Name() string {
	return a.ID()
}

func (a *FakeAdapter) Capabilities() config.AgentCapabilities {
	return a.cfg.Capabilities
}

func (a *FakeAdapter) Health(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.healthError
}

func (a *FakeAdapter) StartSession(ctx context.Context, repo string) (string, error) {
	return fmt.Sprintf("fake_%d", time.Now().UnixNano()), nil
}

func (a *FakeAdapter) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return nil
}

func (a *FakeAdapter) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}

func (a *FakeAdapter) Start(ctx context.Context, repo string) (string, error) {
	return a.StartSession(ctx, repo)
}

func (a *FakeAdapter) Resume(ctx context.Context, sessionID, repo string) error {
	return a.ResumeSession(ctx, sessionID, repo)
}

func (a *FakeAdapter) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	a.mu.Lock()
	a.calls = append(a.calls, req)
	fn := a.customInvoke
	responses := a.responses
	a.mu.Unlock()

	if fn != nil {
		return fn(req)
	}

	for substr, resp := range responses {
		if stringsContains(req.Prompt, substr) {
			return InvokeResult{
				AgentName:  a.Name(),
				SessionID:  req.SessionID,
				Text:       resp,
				RawOutput:  resp,
				DurationMS: 10,
			}, nil
		}
	}

	return InvokeResult{
		AgentName:  a.Name(),
		SessionID:  req.SessionID,
		Text:       "fake response: " + req.Prompt,
		RawOutput:  "fake response: " + req.Prompt,
		DurationMS: 5,
	}, nil
}

func stringsContains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) && (s == substr || len(s) > 0 && len(substr) > 0 && findSubstr(s, substr))
}

func findSubstr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func (a *FakeAdapter) Run(ctx context.Context, req Request) (protocol.AgentResult, error) {
	inv, err := a.Invoke(ctx, InvokeRequest{
		Name:         req.Name,
		Repo:         req.Repo,
		Prompt:       req.Prompt,
		SessionID:    req.SessionID,
		ReviewMode:   req.ReviewMode,
		ReviewSchema: req.ReviewSchema,
	})
	return protocol.AgentResult{
		AgentName:  inv.AgentName,
		SessionID:  inv.SessionID,
		Text:       inv.Text,
		RawOutput:  inv.RawOutput,
		DurationMS: inv.DurationMS,
	}, err
}
