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
		return &OpenHandsAdapter{name: name, cfg: cfg, switchyard: sy}, nil
	}
	RegisterAdapter("openhands", factory)
	RegisterAdapter("openhands-cli", factory)
	RegisterAdapter("all-hands", factory)
}

type OpenHandsAdapter struct {
	name       string
	cfg        config.AgentConfig
	switchyard config.SwitchyardConfig
}

type OpenHandsAgent = OpenHandsAdapter

func (a *OpenHandsAdapter) ID() string {
	return a.Name()
}

func (a *OpenHandsAdapter) AdapterType() string {
	return "openhands"
}

func (a *OpenHandsAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "openhands"
}

func (a *OpenHandsAdapter) Capabilities() config.AgentCapabilities {
	caps := a.cfg.Capabilities
	caps.HardTokenLimitEnforced = false
	return caps
}

func (a *OpenHandsAdapter) buildArgs(req InvokeRequest) []string {
	args := []string{"--task", req.Prompt, "--headless"}
	if !a.cfg.Writable {
		args = append(args, "--read-only")
	}
	if a.cfg.Model != "" {
		args = append(args, "--model", a.cfg.Model)
	}
	args = append(args, a.cfg.ExtraArgs...)
	return args
}

func (a *OpenHandsAdapter) findBinary() (string, error) {
	if a.cfg.Binary != "" {
		if path, err := exec.LookPath(a.cfg.Binary); err == nil {
			return path, nil
		}
	}
	if a.cfg.Command != "" {
		if path, err := exec.LookPath(a.cfg.Command); err == nil {
			return path, nil
		}
	}
	if path, err := exec.LookPath("openhands"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("OpenHands CLI not found (tried: openhands)")
}

func (a *OpenHandsAdapter) Health(ctx context.Context) error {
	bin, err := a.findBinary()
	if err != nil {
		return err
	}
	_, err = executil.Run(ctx, ".", nil, "", bin, "--help")
	return err
}

func (a *OpenHandsAdapter) StartSession(ctx context.Context, repo string) (string, error) {
	return fmt.Sprintf("openhands_%d", time.Now().UnixNano()), nil
}

func (a *OpenHandsAdapter) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return nil
}

func (a *OpenHandsAdapter) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}

func (a *OpenHandsAdapter) Start(ctx context.Context, repo string) (string, error) {
	return a.StartSession(ctx, repo)
}

func (a *OpenHandsAdapter) Resume(ctx context.Context, sessionID, repo string) error {
	return a.ResumeSession(ctx, sessionID, repo)
}

func (a *OpenHandsAdapter) Invoke(parent context.Context, req InvokeRequest) (InvokeResult, error) {
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
	run, runErr := executil.RunWithPolicy(ctx, dir, env, "", executil.SandboxPolicy{
		AllowedCommands: a.cfg.AllowedCommands,
		AllowedPaths:    a.cfg.AllowedPaths,
		DeniedPaths:     a.cfg.DeniedPaths,
		NetworkMode:     a.cfg.NetworkMode,
	}, bin, args...)

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("openhands_%d", time.Now().UnixNano())
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
		return result, &protocol.HarnessInvocationFailedError{
			Agent:  a.Name(),
			Err:    runErr,
			Stderr: tailString(diagnostic, 4000),
		}
	}

	if result.Text == "" {
		return result, &protocol.MalformedPeerResponseError{
			Agent:  a.Name(),
			Reason: "OpenHands completed without output",
			Output: tailString(run.Stdout, 3000),
		}
	}

	return result, nil
}

func (a *OpenHandsAdapter) Run(parent context.Context, req Request) (protocol.AgentResult, error) {
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
