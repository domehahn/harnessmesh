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
		return &CopilotCLIAdapter{name: name, cfg: cfg, switchyard: sy}, nil
	}
	RegisterAdapter("copilot", factory)
	RegisterAdapter("copilot-cli", factory)
	RegisterAdapter("github-copilot", factory)
}

type CopilotCLIAdapter struct {
	name       string
	cfg        config.AgentConfig
	switchyard config.SwitchyardConfig
}

type CopilotCLIAgent = CopilotCLIAdapter

func (a *CopilotCLIAdapter) ID() string {
	return a.Name()
}

func (a *CopilotCLIAdapter) AdapterType() string {
	return "copilot-cli"
}

func (a *CopilotCLIAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "copilot-cli"
}

func (a *CopilotCLIAdapter) Capabilities() config.AgentCapabilities {
	return a.cfg.Capabilities
}

func (a *CopilotCLIAdapter) findBinary() (binPath string, useGh bool, err error) {
	if a.cfg.Binary != "" {
		if path, err := exec.LookPath(a.cfg.Binary); err == nil {
			return path, strings.Contains(filepathBase(a.cfg.Binary), "gh"), nil
		}
	}
	if a.cfg.Command != "" {
		if path, err := exec.LookPath(a.cfg.Command); err == nil {
			return path, strings.Contains(filepathBase(a.cfg.Command), "gh"), nil
		}
	}
	if path, err := exec.LookPath("copilot"); err == nil {
		return path, false, nil
	}
	if path, err := exec.LookPath("gh"); err == nil {
		return path, true, nil
	}
	return "", false, fmt.Errorf("GitHub Copilot CLI not found (tried: copilot, gh)")
}

func filepathBase(p string) string {
	idx := strings.LastIndexAny(p, "/\\")
	if idx >= 0 {
		return p[idx+1:]
	}
	return p
}

func (a *CopilotCLIAdapter) Health(ctx context.Context) error {
	bin, useGh, err := a.findBinary()
	if err != nil {
		return err
	}
	if useGh {
		_, err = executil.Run(ctx, ".", nil, "", bin, "copilot", "--", "--help")
		return err
	}
	_, err = executil.Run(ctx, ".", nil, "", bin, "--help")
	return err
}

func (a *CopilotCLIAdapter) StartSession(ctx context.Context, repo string) (string, error) {
	return fmt.Sprintf("copilot_%d", time.Now().UnixNano()), nil
}

func (a *CopilotCLIAdapter) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return nil
}

func (a *CopilotCLIAdapter) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}

func (a *CopilotCLIAdapter) Start(ctx context.Context, repo string) (string, error) {
	return a.StartSession(ctx, repo)
}

func (a *CopilotCLIAdapter) Resume(ctx context.Context, sessionID, repo string) error {
	return a.ResumeSession(ctx, sessionID, repo)
}

func (a *CopilotCLIAdapter) Invoke(parent context.Context, req InvokeRequest) (InvokeResult, error) {
	timeout := time.Duration(a.cfg.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 45 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	bin, useGh, err := a.findBinary()
	if err != nil {
		return InvokeResult{AgentName: a.Name()}, &protocol.HarnessInvocationFailedError{
			Agent: a.Name(),
			Err:   err,
		}
	}

	var args []string
	if useGh {
		args = append(args, "copilot", "--", "-p", req.Prompt)
		if a.cfg.Writable {
			args = append(args, "--allow-all-tools", "--yes")
		}
	} else {
		args = append(args, "-p", req.Prompt)
		if a.cfg.Writable {
			args = append(args, "--allow-all-tools", "--yes")
		}
	}
	args = append(args, a.cfg.ExtraArgs...)

	dir := req.Repo
	if a.cfg.WorkingDir != "" {
		dir = a.cfg.WorkingDir
	}

	run, runErr := executil.Run(ctx, dir, agentEnv(a.cfg, a.switchyard), "", bin, args...)

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("copilot_%d", time.Now().UnixNano())
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
			strings.Contains(diagLower, "authentication") ||
			strings.Contains(diagLower, "auth") ||
			strings.Contains(diagLower, "login") {
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
			Reason: "Copilot completed without output",
			Output: tailString(run.Stdout, 3000),
		}
	}

	return result, nil
}

func (a *CopilotCLIAdapter) Run(parent context.Context, req Request) (protocol.AgentResult, error) {
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
