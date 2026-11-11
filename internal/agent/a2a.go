package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/a2a"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

func init() {
	factory := func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return &A2AAdapter{name: name, cfg: cfg, switchyard: sy}, nil
	}
	RegisterAdapter("a2a", factory)
	RegisterAdapter("a2a-remote", factory)
}

// A2AAdapter delegates peer invocations over the Agent2Agent (A2A) protocol to
// remote agent clusters (e.g. CrewAI, LangGraph, AutoGen or Agno deployments)
// while enforcing tenant isolation and secret redaction.
type A2AAdapter struct {
	name       string
	cfg        config.AgentConfig
	switchyard config.SwitchyardConfig
}

func (a *A2AAdapter) ID() string {
	return a.Name()
}

func (a *A2AAdapter) AdapterType() string {
	return "a2a"
}

func (a *A2AAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "a2a"
}

func (a *A2AAdapter) Capabilities() config.AgentCapabilities {
	return a.cfg.Capabilities
}

func (a *A2AAdapter) client() a2a.Client {
	endpoint := strings.TrimSpace(a.cfg.Env["HARNESSMESH_A2A_ENDPOINT"])
	if endpoint == "" && a.cfg.ModelRouting != nil {
		endpoint = strings.TrimSpace(a.cfg.ModelRouting.Backend)
	}
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("HARNESSMESH_A2A_ENDPOINT"))
	}
	token := strings.TrimSpace(a.cfg.Env["HARNESSMESH_A2A_TOKEN"])
	if token == "" {
		token = strings.TrimSpace(os.Getenv("HARNESSMESH_A2A_TOKEN"))
	}
	return a2a.Client{
		Endpoint: endpoint,
		Token:    token,
	}
}

func (a *A2AAdapter) tenant() string {
	if t := strings.TrimSpace(a.cfg.Env["HARNESSMESH_TENANT"]); t != "" {
		return t
	}
	if t := strings.TrimSpace(os.Getenv("HARNESSMESH_TENANT")); t != "" {
		return t
	}
	return "default"
}

func (a *A2AAdapter) Health(ctx context.Context) error {
	_, err := a.client().Discover(ctx)
	return err
}

func (a *A2AAdapter) StartSession(ctx context.Context, repo string) (string, error) {
	return fmt.Sprintf("a2a_%d", time.Now().UnixNano()), nil
}

func (a *A2AAdapter) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return nil
}

func (a *A2AAdapter) CloseSession(ctx context.Context, sessionID string) error {
	return nil
}

func (a *A2AAdapter) Start(ctx context.Context, repo string) (string, error) {
	return a.StartSession(ctx, repo)
}

func (a *A2AAdapter) Resume(ctx context.Context, sessionID, repo string) error {
	return a.ResumeSession(ctx, sessionID, repo)
}

func (a *A2AAdapter) Invoke(parent context.Context, req InvokeRequest) (InvokeResult, error) {
	start := time.Now()
	timeout := time.Duration(a.cfg.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("a2a_%d", time.Now().UnixNano())
	}

	task, err := a.client().SubmitTask(ctx, a2a.TaskRequest{
		ID:     sessionID,
		Tenant: a.tenant(),
		Input:  req.Prompt,
		Metadata: map[string]any{
			"agent":       a.Name(),
			"session_id":  sessionID,
			"review_mode": req.ReviewMode,
		},
	})
	durationMS := time.Since(start).Milliseconds()
	if err != nil {
		return InvokeResult{AgentName: a.Name(), SessionID: sessionID, DurationMS: durationMS}, &protocol.HarnessInvocationFailedError{
			Agent: a.Name(),
			Err:   err,
		}
	}
	out := strings.TrimSpace(task.Output)
	if out == "" {
		return InvokeResult{AgentName: a.Name(), SessionID: sessionID, DurationMS: durationMS}, &protocol.MalformedPeerResponseError{
			Agent:  a.Name(),
			Reason: "A2A peer returned empty task output",
		}
	}
	return InvokeResult{
		AgentName:  a.Name(),
		SessionID:  sessionID,
		Text:       out,
		RawOutput:  out,
		DurationMS: durationMS,
	}, nil
}

func (a *A2AAdapter) Run(parent context.Context, req Request) (protocol.AgentResult, error) {
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
