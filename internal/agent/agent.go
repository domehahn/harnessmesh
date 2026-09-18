package agent

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

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

var (
	registryMu sync.RWMutex
	registry   = make(map[string]Factory)
)

func RegisterAdapter(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[strings.ToLower(strings.TrimSpace(name))] = f
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

func NewHarness(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
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
