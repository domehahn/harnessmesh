package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

var ErrExternalParticipantCannotBeInvoked = errors.New("external participant cannot be invoked directly by HarnessMesh; conversations must be initiated by the external participant")

type ExternalHarness struct {
	id           string
	adapter      string
	capabilities config.AgentCapabilities
}

func NewExternalHarness(id string, cfg config.AgentConfig) *ExternalHarness {
	return &ExternalHarness{
		id:           id,
		adapter:      cfg.Kind,
		capabilities: cfg.Capabilities,
	}
}

func (h *ExternalHarness) ID() string                             { return h.id }
func (h *ExternalHarness) Name() string                           { return h.id }
func (h *ExternalHarness) AdapterType() string                    { return h.adapter }
func (h *ExternalHarness) Capabilities() config.AgentCapabilities { return h.capabilities }
func (h *ExternalHarness) Health(ctx context.Context) error       { return nil }
func (h *ExternalHarness) StartSession(ctx context.Context, repo string) (string, error) {
	return "", ErrExternalParticipantCannotBeInvoked
}
func (h *ExternalHarness) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return ErrExternalParticipantCannotBeInvoked
}
func (h *ExternalHarness) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}
func (h *ExternalHarness) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	return InvokeResult{}, ErrExternalParticipantCannotBeInvoked
}
func (h *ExternalHarness) Start(ctx context.Context, repo string) (string, error) {
	return "", ErrExternalParticipantCannotBeInvoked
}
func (h *ExternalHarness) Resume(ctx context.Context, sessionID, repo string) error {
	return ErrExternalParticipantCannotBeInvoked
}
func (h *ExternalHarness) Run(ctx context.Context, req Request) (protocol.AgentResult, error) {
	return protocol.AgentResult{AgentName: h.id, Text: ErrExternalParticipantCannotBeInvoked.Error()}, ErrExternalParticipantCannotBeInvoked
}

type Request struct {
	Name         string
	Repo         string
	Prompt       string
	SessionID    string
	ReviewMode   bool
	ReviewSchema string
}

type InvokeRequest struct {
	Name          string
	Repo          string
	Prompt        string
	SessionID     string
	ReviewMode    bool
	ReviewSchema  string
	MCPConfigPath string
	MaxTokens     int64
	MaxCostUSD    float64
	ApprovalID    string
}

type InvokeResult struct {
	AgentName  string         `json:"agent_name"`
	SessionID  string         `json:"session_id"`
	Text       string         `json:"text"`
	RawOutput  string         `json:"-"`
	Usage      map[string]any `json:"usage,omitempty"`
	DurationMS int64          `json:"duration_ms"`
}

type Agent interface {
	Run(context.Context, Request) (protocol.AgentResult, error)
	Name() string
}

type Harness interface {
	Agent
	ID() string
	AdapterType() string
	Capabilities() config.AgentCapabilities
	Health(ctx context.Context) error
	StartSession(ctx context.Context, repo string) (string, error)
	ResumeSession(ctx context.Context, sessionID, repo string) error
	CloseSession(ctx context.Context, sessionID string) error
	Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error)

	// Backward compatibility methods
	Start(ctx context.Context, repo string) (string, error)
	Resume(ctx context.Context, sessionID, repo string) error
}

type Factory func(id string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error)

// AdapterDescriptor is the stable metadata contract used by control-plane
// catalogs, adapter installers and future external adapter SDKs. Factories
// remain the execution seam; descriptors never contain credentials.
type AdapterDescriptor struct {
	Name         string   `json:"name"`
	Aliases      []string `json:"aliases,omitempty"`
	Version      string   `json:"version,omitempty"`
	Managed      bool     `json:"managed"`
	External     bool     `json:"external"`
	Capabilities []string `json:"capabilities,omitempty"`
}

var (
	registryMu  sync.RWMutex
	registry    = make(map[string]Factory)
	descriptors = make(map[string]AdapterDescriptor)
)

func RegisterAdapter(name string, f Factory) {
	RegisterAdapterWithDescriptor(name, f, AdapterDescriptor{Name: strings.ToLower(strings.TrimSpace(name)), Version: "builtin", Managed: true})
}

func RegisterAdapterWithDescriptor(name string, f Factory, descriptor AdapterDescriptor) {
	registryMu.Lock()
	defer registryMu.Unlock()
	key := strings.ToLower(strings.TrimSpace(name))
	registry[key] = f
	if descriptor.Name == "" {
		descriptor.Name = key
	}
	descriptors[key] = descriptor
}

func RegisteredAdapters() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var out []string
	for k := range registry {
		out = append(out, k)
	}
	return out
}

func RegisteredAdapterDescriptors() []AdapterDescriptor {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]AdapterDescriptor, 0, len(descriptors))
	for _, descriptor := range descriptors {
		out = append(out, descriptor)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func NewHarness(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
	if cfg.IsExternal() {
		return NewExternalHarness(name, cfg), nil
	}
	kind := cfg.Kind
	if kind == "" {
		kind = cfg.Adapter
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	registryMu.RLock()
	f, ok := registry[kind]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unsupported harness adapter %q", kind)
	}
	return f(name, cfg, sy)
}

func New(cfg config.AgentConfig, sy config.SwitchyardConfig) (Agent, error) {
	return NewHarness(cfg.Kind, cfg, sy)
}

func CheckSwitchyard(ctx context.Context, cfg config.SwitchyardConfig) error {
	if !cfg.Enabled {
		return nil
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		return fmt.Errorf("switchyard enabled but base_url is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, req.URL)
	}
	return nil
}

func CheckBinary(binary string) (string, error) {
	return exec.LookPath(binary)
}
