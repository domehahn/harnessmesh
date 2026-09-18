package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/executil"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

type CodexAgent struct {
	cfg        config.AgentConfig
	switchyard config.SwitchyardConfig
}

func (a *CodexAgent) Name() string { return "codex" }

type codexEvent struct {
	Type     string         `json:"type"`
	ThreadID string         `json:"thread_id"`
	Usage    map[string]any `json:"usage"`
	Item     struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
}

func (a *CodexAgent) Run(parent context.Context, req Request) (protocol.AgentResult, error) {
	timeout := time.Duration(a.cfg.TimeoutMinutes) * time.Minute
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	binary := a.cfg.Command
	if binary == "" {
		binary = "codex"
	}

	tmpDir, err := os.MkdirTemp("", "harnessmesh-codex-*")
	if err != nil {
		return protocol.AgentResult{}, err
	}
	defer os.RemoveAll(tmpDir)

	lastMessagePath := filepath.Join(tmpDir, "last-message.txt")
	args := []string{"exec", "--json", "--skip-git-repo-check", "--color", "never", "-o", lastMessagePath}

	if a.cfg.Model != "" {
		args = append(args, "--model", a.cfg.Model)
	} else if a.cfg.UseSwitchyard && a.switchyard.Enabled {
		route := a.cfg.SwitchyardRouteID
		if route == "" {
			route = a.switchyard.RouteID
		}
		if route != "" {
			args = append(args, "--model", route)
		}
	}

	mode := a.cfg.Mode
	if mode == "" {
		mode = "read-only"
	}
	args = append(args, "--sandbox", mode)

	if req.ReviewMode && req.ReviewSchema != "" {
		schemaPath := filepath.Join(tmpDir, "review-schema.json")
		if err := os.WriteFile(schemaPath, []byte(req.ReviewSchema), 0600); err != nil {
			return protocol.AgentResult{}, err
		}
		args = append(args, "--output-schema", schemaPath)
	}

	args = append(args, a.cfg.ExtraArgs...)
	if req.SessionID != "" {
		args = append(args, "resume", req.SessionID, "-")
	} else {
		args = append(args, "-")
	}

	dir := req.Repo
	if a.cfg.WorkingDir != "" {
		dir = a.cfg.WorkingDir
	}
	run, runErr := executil.Run(ctx, dir, agentEnv(a.cfg, a.switchyard), req.Prompt, binary, args...)

	sessionID, usage, fallbackText := parseCodexJSONL(run.Stdout)
	lastRaw, _ := os.ReadFile(lastMessagePath)
	text := strings.TrimSpace(string(lastRaw))
	if text == "" {
		text = strings.TrimSpace(fallbackText)
	}
	if sessionID == "" {
		sessionID = req.SessionID
	}

	result := protocol.AgentResult{
		AgentName:  req.Name,
		SessionID:  sessionID,
		Text:       text,
		RawOutput:  run.Stdout,
		Usage:      usage,
		DurationMS: run.DurationMS,
	}
	if runErr != nil {
		return result, runErr
	}
	if text == "" {
		return result, fmt.Errorf("Codex completed without a final message")
	}
	return result, nil
}

func parseCodexJSONL(raw string) (string, map[string]any, string) {
	var threadID string
	var usage map[string]any
	var lastText string

	scanner := bufio.NewScanner(strings.NewReader(raw))
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event codexEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		switch event.Type {
		case "thread.started":
			if event.ThreadID != "" {
				threadID = event.ThreadID
			}
		case "turn.completed":
			if event.Usage != nil {
				usage = event.Usage
			}
		case "item.completed":
			if event.Item.Type == "agent_message" && event.Item.Text != "" {
				lastText = event.Item.Text
			}
		}
	}
	if usage == nil {
		usage = map[string]any{}
	}
	return threadID, usage, lastText
}
