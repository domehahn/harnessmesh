package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/executil"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

func init() {
	factory := func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return &ClaudeCodeAdapter{name: name, cfg: cfg, switchyard: sy}, nil
	}
	RegisterAdapter("claude", factory)
	RegisterAdapter("claude-code", factory)
}

type ClaudeCodeAdapter struct {
	name       string
	cfg        config.AgentConfig
	switchyard config.SwitchyardConfig
}

type ClaudeAgent = ClaudeCodeAdapter

func (a *ClaudeCodeAdapter) ID() string {
	return a.Name()
}

func (a *ClaudeCodeAdapter) AdapterType() string {
	return "claude-code"
}

func (a *ClaudeCodeAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "claude"
}

func (a *ClaudeCodeAdapter) Capabilities() config.AgentCapabilities {
	return a.cfg.Capabilities
}

func (a *ClaudeCodeAdapter) Health(ctx context.Context) error {
	binary := a.cfg.Binary
	if binary == "" {
		binary = a.cfg.Command
	}
	if binary == "" {
		binary = "claude"
	}
	_, err := executil.Run(ctx, ".", nil, "", binary, "--version")
	return err
}

func (a *ClaudeCodeAdapter) StartSession(ctx context.Context, repo string) (string, error) {
	return a.Start(ctx, repo)
}

func (a *ClaudeCodeAdapter) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return a.Resume(ctx, sessionID, repo)
}

func (a *ClaudeCodeAdapter) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}

func (a *ClaudeCodeAdapter) Start(ctx context.Context, repo string) (string, error) {
	// Claude creates session on first prompt invocation or resume
	return "", nil
}

func (a *ClaudeCodeAdapter) Resume(ctx context.Context, sessionID, repo string) error {
	return nil
}

type claudeJSON struct {
	Type         string         `json:"type"`
	Result       string         `json:"result"`
	SessionID    string         `json:"session_id"`
	TotalCostUSD float64        `json:"total_cost_usd"`
	Usage        map[string]any `json:"usage"`
	IsError      bool           `json:"is_error"`
}

func (a *ClaudeCodeAdapter) Invoke(parent context.Context, req InvokeRequest) (InvokeResult, error) {
	timeout := time.Duration(a.cfg.TimeoutMinutes) * time.Minute
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	binary := a.cfg.Binary
	if binary == "" {
		binary = a.cfg.Command
	}
	if binary == "" {
		binary = "claude"
	}

	args := []string{"-p", "--output-format", "json"}
	if a.cfg.Model != "" {
		args = append(args, "--model", a.cfg.Model)
	}
	if a.cfg.Mode != "" {
		args = append(args, "--permission-mode", a.cfg.Mode)
	}
	if a.cfg.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprint(a.cfg.MaxTurns))
	}
	if a.cfg.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.4f", a.cfg.MaxBudgetUSD))
	}
	if req.ReviewMode {
		args = append(args, "--tools", "")
		if req.ReviewSchema != "" {
			args = append(args, "--json-schema", req.ReviewSchema)
		}
	}
	if req.MCPConfigPath != "" {
		args = append(args, "--mcp-config", req.MCPConfigPath)
	}
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}
	args = append(args, a.cfg.ExtraArgs...)
	args = append(args, req.Prompt)

	dir := req.Repo
	if a.cfg.WorkingDir != "" {
		dir = a.cfg.WorkingDir
	}
	run, err := executil.Run(ctx, dir, agentEnv(a.cfg, a.switchyard), "", binary, args...)
	if err != nil {
		raw := strings.TrimSpace(run.Stdout + "\n" + run.Stderr)
		var out claudeJSON
		if jsonErr := json.Unmarshal([]byte(run.Stdout), &out); jsonErr == nil && out.Result != "" {
			return InvokeResult{
					AgentName:  a.Name(),
					SessionID:  out.SessionID,
					RawOutput:  raw,
					DurationMS: run.DurationMS,
				}, &protocol.HarnessInvocationFailedError{
					Agent:  a.Name(),
					Err:    err,
					Stderr: out.Result,
				}
		}
		diagnostic := strings.TrimSpace(run.Stderr)
		if diagnostic == "" {
			diagnostic = strings.TrimSpace(run.Stdout)
		}
		return InvokeResult{
				AgentName:  a.Name(),
				SessionID:  req.SessionID,
				RawOutput:  raw,
				DurationMS: run.DurationMS,
			}, &protocol.HarnessInvocationFailedError{
				Agent:  a.Name(),
				Err:    err,
				Stderr: tailString(diagnostic, 4000),
			}
	}

	var out claudeJSON
	if err := json.Unmarshal([]byte(run.Stdout), &out); err != nil {
		return InvokeResult{}, &protocol.MalformedPeerResponseError{
			Agent:  a.Name(),
			Reason: fmt.Sprintf("parse Claude JSON output: %v", err),
			Output: tailString(run.Stdout, 3000),
		}
	}
	if out.IsError {
		return InvokeResult{}, fmt.Errorf("Claude reported an error: %s", out.Result)
	}
	sessionID := out.SessionID
	if sessionID == "" {
		sessionID = req.SessionID
	}
	usage := out.Usage
	if usage == nil {
		usage = map[string]any{}
	}
	if out.TotalCostUSD > 0 {
		usage["total_cost_usd"] = out.TotalCostUSD
	}
	return InvokeResult{
		AgentName:  a.Name(),
		SessionID:  sessionID,
		Text:       strings.TrimSpace(out.Result),
		RawOutput:  run.Stdout,
		Usage:      usage,
		DurationMS: run.DurationMS,
	}, nil
}

func (a *ClaudeCodeAdapter) Run(parent context.Context, req Request) (protocol.AgentResult, error) {
	inv, err := a.Invoke(parent, InvokeRequest{
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
		Usage:      inv.Usage,
		DurationMS: inv.DurationMS,
	}, err
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
