package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/executil"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

func init() {
	factory := func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return &CodexAdapter{name: name, cfg: cfg, switchyard: sy}, nil
	}
	RegisterAdapter("codex", factory)
}

type CodexAdapter struct {
	name       string
	cfg        config.AgentConfig
	switchyard config.SwitchyardConfig
}

type CodexAgent = CodexAdapter

func (a *CodexAdapter) ID() string {
	return a.Name()
}

func (a *CodexAdapter) AdapterType() string {
	return "codex"
}

func (a *CodexAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "codex"
}

func (a *CodexAdapter) Capabilities() config.AgentCapabilities {
	return a.cfg.Capabilities
}

func (a *CodexAdapter) Health(ctx context.Context) error {
	binary := a.cfg.Binary
	if binary == "" {
		binary = a.cfg.Command
	}
	if binary == "" {
		binary = "codex"
	}
	binPath, err := exec.LookPath(binary)
	if err != nil {
		return &protocol.PeerUnavailableError{Peer: a.Name(), Reason: fmt.Sprintf("binary %q not found in PATH", binary)}
	}
	_, err = executil.Run(ctx, ".", nil, "", binPath, "--version")
	return err
}

func (a *CodexAdapter) StartSession(ctx context.Context, repo string) (string, error) {
	return a.Start(ctx, repo)
}

func (a *CodexAdapter) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return a.Resume(ctx, sessionID, repo)
}

func (a *CodexAdapter) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}

func (a *CodexAdapter) Start(ctx context.Context, repo string) (string, error) {
	return "", nil
}

func (a *CodexAdapter) Resume(ctx context.Context, sessionID, repo string) error {
	return nil
}

type codexEvent struct {
	Type     string         `json:"type"`
	ThreadID string         `json:"thread_id"`
	Usage    map[string]any `json:"usage"`
	Item     struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
}

func (a *CodexAdapter) Invoke(parent context.Context, req InvokeRequest) (InvokeResult, error) {
	timeout := time.Duration(a.cfg.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 45 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	binary := a.cfg.Binary
	if binary == "" {
		binary = a.cfg.Command
	}
	if binary == "" {
		binary = "codex"
	}
	binPath, err := exec.LookPath(binary)
	if err != nil {
		return InvokeResult{AgentName: a.Name()}, &protocol.PeerUnavailableError{
			Peer:   a.Name(),
			Reason: fmt.Sprintf("codex binary %q not found in PATH. Run 'harnessmesh doctor'", binary),
		}
	}

	tmpDir, err := os.MkdirTemp("", "harnessmesh-codex-*")
	if err != nil {
		return InvokeResult{}, err
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
			return InvokeResult{}, err
		}
		args = append(args, "--output-schema", schemaPath)
	}

	if req.MaxTokens > 0 {
		args = append(args, "-c", fmt.Sprintf("model_options.max_tokens=%d", req.MaxTokens))
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
	env := agentEnv(a.cfg, a.switchyard)
	if req.MaxTokens > 0 {
		env["OPENAI_MAX_TOKENS"] = fmt.Sprint(req.MaxTokens)
	}
	run, runErr := executil.Run(ctx, dir, env, req.Prompt, binPath, args...)

	sessionID, usage, fallbackText := parseCodexJSONL(run.Stdout)
	lastRaw, _ := os.ReadFile(lastMessagePath)
	text := strings.TrimSpace(string(lastRaw))
	if text == "" {
		text = strings.TrimSpace(fallbackText)
	}
	if sessionID == "" {
		sessionID = req.SessionID
	}

	result := InvokeResult{
		AgentName:  a.Name(),
		SessionID:  sessionID,
		Text:       text,
		RawOutput:  run.Stdout,
		Usage:      usage,
		DurationMS: run.DurationMS,
	}

	if ctx.Err() == context.DeadlineExceeded || parent.Err() == context.DeadlineExceeded {
		return result, &protocol.PeerTimeoutError{
			Peer:    a.Name(),
			Timeout: timeout,
		}
	}

	if runErr != nil {
		diagnostic := strings.TrimSpace(run.Stderr)
		if diagnostic == "" {
			diagnostic = strings.TrimSpace(run.Stdout)
		}
		diagLower := strings.ToLower(diagnostic)
		if strings.Contains(diagLower, "not logged in") ||
			strings.Contains(diagLower, "auth") ||
			strings.Contains(diagLower, "login") ||
			strings.Contains(diagLower, "api key") ||
			strings.Contains(diagLower, "credentials") {
			return result, &protocol.HarnessAuthenticationRequiredError{
				Agent:  a.Name(),
				Reason: tailString(diagnostic, 4000),
			}
		}
		return result, &protocol.HarnessInvocationFailedError{
			Agent:  a.Name(),
			Err:    runErr,
			Stderr: tailString(diagnostic, 4000),
		}
	}
	if text == "" {
		return result, &protocol.MalformedPeerResponseError{
			Agent:  a.Name(),
			Reason: "Codex completed without a final message",
			Output: tailString(run.Stdout, 3000),
		}
	}
	return result, nil
}

func (a *CodexAdapter) Run(parent context.Context, req Request) (protocol.AgentResult, error) {
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
