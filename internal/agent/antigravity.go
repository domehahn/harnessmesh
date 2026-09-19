package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/executil"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

func init() {
	factory := func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return &AntigravityAdapter{name: name, cfg: cfg, switchyard: sy}, nil
	}
	RegisterAdapter("antigravity", factory)
	RegisterAdapter("agy", factory)
}

type AntigravityAdapter struct {
	name       string
	cfg        config.AgentConfig
	switchyard config.SwitchyardConfig
}

type AntigravityAgent = AntigravityAdapter

func (a *AntigravityAdapter) ID() string {
	return a.Name()
}

func (a *AntigravityAdapter) AdapterType() string {
	return "antigravity"
}

func (a *AntigravityAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "antigravity"
}

func (a *AntigravityAdapter) Capabilities() config.AgentCapabilities {
	caps := a.cfg.Capabilities
	caps.HardTokenLimitEnforced = false
	return caps
}

func (a *AntigravityAdapter) buildArgs(req InvokeRequest) []string {
	args := []string{"-p", req.Prompt, "--print-timeout", "10m"}
	if a.cfg.Writable {
		args = append(args, "--dangerously-skip-permissions")
	}
	if req.SessionID != "" {
		args = append(args, "--session-id", req.SessionID)
	}
	args = append(args, a.cfg.ExtraArgs...)
	return args
}

func (a *AntigravityAdapter) findBinary() (string, error) {
	candidates := []string{}
	if a.cfg.Binary != "" {
		candidates = append(candidates, a.cfg.Binary)
	}
	if a.cfg.Command != "" {
		candidates = append(candidates, a.cfg.Command)
	}
	candidates = append(candidates, "agy", "antigravity")

	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("antigravity CLI not found (tried: %s)", strings.Join(candidates, ", "))
}

func (a *AntigravityAdapter) Health(ctx context.Context) error {
	bin, err := a.findBinary()
	if err != nil {
		return err
	}
	_, err = executil.Run(ctx, ".", nil, "", bin, "--help")
	return err
}

func (a *AntigravityAdapter) StartSession(ctx context.Context, repo string) (string, error) {
	return fmt.Sprintf("agy_%d", time.Now().UnixNano()), nil
}

func (a *AntigravityAdapter) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return nil
}

func (a *AntigravityAdapter) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}

func (a *AntigravityAdapter) Start(ctx context.Context, repo string) (string, error) {
	return a.StartSession(ctx, repo)
}

func (a *AntigravityAdapter) Resume(ctx context.Context, sessionID, repo string) error {
	return a.ResumeSession(ctx, sessionID, repo)
}

func (a *AntigravityAdapter) Invoke(parent context.Context, req InvokeRequest) (InvokeResult, error) {
	timeout := time.Duration(a.cfg.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 45 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	bin, err := a.findBinary()
	if err != nil {
		return InvokeResult{AgentName: a.Name()}, &protocol.HarnessInvocationFailedError{
			Agent: a.Name(),
			Err:   err,
		}
	}

	args := a.buildArgs(req)

	dir := req.Repo
	if a.cfg.WorkingDir != "" {
		dir = a.cfg.WorkingDir
	}

	env := agentEnv(a.cfg, a.switchyard)
	run, runErr := executil.Run(ctx, dir, env, "", bin, args...)

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("agy_%d", time.Now().UnixNano())
	}

	result := InvokeResult{
		AgentName:  a.Name(),
		SessionID:  sessionID,
		Text:       strings.TrimSpace(run.Stdout),
		RawOutput:  run.Stdout,
		DurationMS: run.DurationMS,
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

	if result.Text == "" {
		return result, &protocol.MalformedPeerResponseError{
			Agent:  a.Name(),
			Reason: "Antigravity completed without output",
			Output: tailString(run.Stdout, 3000),
		}
	}

	return result, nil
}

func (a *AntigravityAdapter) Run(parent context.Context, req Request) (protocol.AgentResult, error) {
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
