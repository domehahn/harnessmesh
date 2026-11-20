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
	RegisterAdapter("opencode", func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return &OSSCLIAdapter{name: name, kind: "opencode", defaultBin: "opencode", cfg: cfg, switchyard: sy}, nil
	})
	RegisterAdapter("opencode-cli", func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return &OSSCLIAdapter{name: name, kind: "opencode", defaultBin: "opencode", cfg: cfg, switchyard: sy}, nil
	})
	RegisterAdapter("aider", func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return &OSSCLIAdapter{name: name, kind: "aider", defaultBin: "aider", cfg: cfg, switchyard: sy}, nil
	})
	RegisterAdapter("aider-cli", func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return &OSSCLIAdapter{name: name, kind: "aider", defaultBin: "aider", cfg: cfg, switchyard: sy}, nil
	})
	RegisterAdapter("gemini", func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return &OSSCLIAdapter{name: name, kind: "gemini-cli", defaultBin: "gemini", cfg: cfg, switchyard: sy}, nil
	})
	RegisterAdapter("gemini-cli", func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return &OSSCLIAdapter{name: name, kind: "gemini-cli", defaultBin: "gemini", cfg: cfg, switchyard: sy}, nil
	})
}

// OSSCLIAdapter provides first-class CLI harness integration for OpenCode,
// Aider and Gemini CLI with sandbox policy enforcement and read-only scoping.
type OSSCLIAdapter struct {
	name       string
	kind       string
	defaultBin string
	cfg        config.AgentConfig
	switchyard config.SwitchyardConfig
}

func (a *OSSCLIAdapter) ID() string {
	return a.Name()
}

func (a *OSSCLIAdapter) AdapterType() string {
	return a.kind
}

func (a *OSSCLIAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return a.kind
}

func (a *OSSCLIAdapter) Capabilities() config.AgentCapabilities {
	caps := a.cfg.Capabilities
	caps.HardTokenLimitEnforced = false
	return caps
}

func (a *OSSCLIAdapter) buildArgs(req InvokeRequest) []string {
	var args []string
	switch a.kind {
	case "opencode":
		args = []string{"run", req.Prompt}
		if a.cfg.Model != "" {
			args = append(args, "--model", a.cfg.Model)
		}
	case "aider":
		args = []string{"--message", req.Prompt, "--yes-always", "--no-auto-commits"}
		if a.cfg.Model != "" {
			args = append(args, "--model", a.cfg.Model)
		}
	case "gemini-cli":
		args = []string{"--prompt", req.Prompt}
		if a.cfg.Model != "" {
			args = append(args, "--model", a.cfg.Model)
		}
	}
	args = append(args, a.cfg.ExtraArgs...)
	return args
}

func (a *OSSCLIAdapter) findBinary() (string, error) {
	for _, candidate := range []string{a.cfg.Binary, a.cfg.Command, a.defaultBin} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s CLI not found (tried: %s)", a.kind, a.defaultBin)
}

func (a *OSSCLIAdapter) Health(ctx context.Context) error {
	bin, err := a.findBinary()
	if err != nil {
		return err
	}
	_, err = executil.Run(ctx, ".", nil, "", bin, "--help")
	return err
}

func (a *OSSCLIAdapter) StartSession(ctx context.Context, repo string) (string, error) {
	return fmt.Sprintf("%s_%d", strings.ReplaceAll(a.kind, "-", "_"), time.Now().UnixNano()), nil
}

func (a *OSSCLIAdapter) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return nil
}

func (a *OSSCLIAdapter) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}

func (a *OSSCLIAdapter) Start(ctx context.Context, repo string) (string, error) {
	return a.StartSession(ctx, repo)
}

func (a *OSSCLIAdapter) Resume(ctx context.Context, sessionID, repo string) error {
	return a.ResumeSession(ctx, sessionID, repo)
}

func (a *OSSCLIAdapter) Invoke(parent context.Context, req InvokeRequest) (InvokeResult, error) {
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

	credScope := "read-write"
	if !a.cfg.Writable {
		credScope = "read-only"
	}

	env := agentEnv(a.cfg, a.switchyard)
	run, runErr := executil.RunWithPolicy(ctx, dir, env, "", executil.SandboxPolicy{
		AllowedCommands: a.cfg.AllowedCommands,
		AllowedPaths:    a.cfg.AllowedPaths,
		DeniedPaths:     a.cfg.DeniedPaths,
		NetworkMode:     a.cfg.NetworkMode,
		CredentialScope: credScope,
	}, bin, args...)

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("%s_%d", strings.ReplaceAll(a.kind, "-", "_"), time.Now().UnixNano())
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
			Reason: fmt.Sprintf("%s completed without output", a.kind),
			Output: tailString(run.Stdout, 3000),
		}
	}

	return result, nil
}

func (a *OSSCLIAdapter) Run(parent context.Context, req Request) (protocol.AgentResult, error) {
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
