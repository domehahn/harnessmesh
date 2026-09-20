package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

func init() {
	factory := func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (Harness, error) {
		return &OpenAIAdapter{name: name, cfg: cfg, switchyard: sy}, nil
	}
	RegisterAdapter("openai", factory)
	RegisterAdapter("openai-api", factory)
}

type OpenAIAdapter struct {
	name       string
	cfg        config.AgentConfig
	switchyard config.SwitchyardConfig
}

func (a *OpenAIAdapter) ID() string          { return a.Name() }
func (a *OpenAIAdapter) AdapterType() string { return "openai-api" }
func (a *OpenAIAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "openai"
}
func (a *OpenAIAdapter) Capabilities() config.AgentCapabilities                   { return a.cfg.Capabilities }
func (a *OpenAIAdapter) Start(ctx context.Context, repo string) (string, error)   { return "", nil }
func (a *OpenAIAdapter) Resume(ctx context.Context, sessionID, repo string) error { return nil }
func (a *OpenAIAdapter) StartSession(ctx context.Context, repo string) (string, error) {
	return a.Start(ctx, repo)
}
func (a *OpenAIAdapter) ResumeSession(ctx context.Context, sessionID, repo string) error {
	return a.Resume(ctx, sessionID, repo)
}
func (a *OpenAIAdapter) CloseSession(ctx context.Context, sessionID string) error { return nil }

func (a *OpenAIAdapter) credentials() (string, string) {
	key := a.cfg.Env["OPENAI_API_KEY"]
	if key == "" {
		key = os.Getenv("OPENAI_API_KEY")
	}
	base := a.cfg.Env["OPENAI_BASE_URL"]
	if base == "" {
		base = os.Getenv("OPENAI_BASE_URL")
	}
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return key, strings.TrimRight(base, "/")
}

func (a *OpenAIAdapter) Health(ctx context.Context) error {
	key, base := a.credentials()
	if key == "" {
		return &protocol.HarnessAuthenticationRequiredError{Agent: a.Name(), Reason: "OPENAI_API_KEY is not configured"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OpenAI API health check returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *OpenAIAdapter) Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error) {
	key, base := a.credentials()
	if key == "" {
		return InvokeResult{AgentName: a.Name()}, &protocol.HarnessAuthenticationRequiredError{Agent: a.Name(), Reason: "OPENAI_API_KEY is not configured"}
	}
	model := a.cfg.Model
	if model == "" {
		model = os.Getenv("OPENAI_MODEL")
	}
	if model == "" {
		model = "gpt-4.1-mini"
	}
	body := map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": req.Prompt}},
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	raw, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return InvokeResult{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+key)
	httpReq.Header.Set("Content-Type", "application/json")
	started := time.Now()
	timeout := time.Duration(a.cfg.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 45 * time.Minute
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(httpReq)
	if err != nil {
		return InvokeResult{}, err
	}
	defer resp.Body.Close()
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
		Error any            `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return InvokeResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return InvokeResult{}, fmt.Errorf("OpenAI API returned HTTP %d: %v", resp.StatusCode, decoded.Error)
	}
	if len(decoded.Choices) == 0 {
		return InvokeResult{}, &protocol.MalformedPeerResponseError{Agent: a.Name(), Reason: "OpenAI API returned no choices"}
	}
	return InvokeResult{AgentName: a.Name(), SessionID: req.SessionID, Text: strings.TrimSpace(decoded.Choices[0].Message.Content), Usage: decoded.Usage, DurationMS: time.Since(started).Milliseconds()}, nil
}

func (a *OpenAIAdapter) Run(ctx context.Context, req Request) (protocol.AgentResult, error) {
	result, err := a.Invoke(ctx, InvokeRequest{Name: req.Name, Repo: req.Repo, Prompt: req.Prompt, SessionID: req.SessionID, ReviewMode: req.ReviewMode, ReviewSchema: req.ReviewSchema})
	return protocol.AgentResult{AgentName: result.AgentName, SessionID: result.SessionID, Text: result.Text, RawOutput: result.RawOutput, Usage: result.Usage, DurationMS: result.DurationMS}, err
}
