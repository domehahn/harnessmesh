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

type ClaudeAgent struct {
	cfg        config.AgentConfig
	switchyard config.SwitchyardConfig
}

func (a *ClaudeAgent) Name() string { return "claude" }

type claudeJSON struct {
	Type         string         `json:"type"`
	Result       string         `json:"result"`
	SessionID    string         `json:"session_id"`
	TotalCostUSD float64        `json:"total_cost_usd"`
	Usage        map[string]any `json:"usage"`
	IsError      bool           `json:"is_error"`
}

func (a *ClaudeAgent) Run(parent context.Context, req Request) (protocol.AgentResult, error) {
	timeout := time.Duration(a.cfg.TimeoutMinutes) * time.Minute
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	binary := a.cfg.Command
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
		return protocol.AgentResult{
			AgentName:  req.Name,
			SessionID:  req.SessionID,
			RawOutput:  run.Stdout + "\n" + run.Stderr,
			DurationMS: run.DurationMS,
		}, err
	}

	var out claudeJSON
	if err := json.Unmarshal([]byte(run.Stdout), &out); err != nil {
		return protocol.AgentResult{}, fmt.Errorf("parse Claude JSON output: %w; output=%s", err, tailString(run.Stdout, 3000))
	}
	if out.IsError {
		return protocol.AgentResult{}, fmt.Errorf("Claude reported an error: %s", out.Result)
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
	return protocol.AgentResult{
		AgentName:  req.Name,
		SessionID:  sessionID,
		Text:       strings.TrimSpace(out.Result),
		RawOutput:  run.Stdout,
		Usage:      usage,
		DurationMS: run.DurationMS,
	}, nil
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
