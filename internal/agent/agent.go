package agent

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

type Agent interface {
	Run(context.Context, Request) (protocol.AgentResult, error)
	Name() string
}

func New(cfg config.AgentConfig, sy config.SwitchyardConfig) (Agent, error) {
	switch cfg.Kind {
	case "claude":
		return &ClaudeAgent{cfg: cfg, switchyard: sy}, nil
	case "codex":
		return &CodexAgent{cfg: cfg, switchyard: sy}, nil
	default:
		return nil, fmt.Errorf("unsupported agent kind %q", cfg.Kind)
	}
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
