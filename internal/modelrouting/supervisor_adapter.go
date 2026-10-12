package modelrouting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// defaultRequestTimeout bounds every Complete() call regardless of what
// timeout (if any) r.Backend.HTTPClient carries, and regardless of
// whether the caller's ctx has a deadline at all. Closure review found
// that falling back to http.DefaultClient (no timeout) when
// Backend.HTTPClient is nil would let an unresponsive or malicious
// Switchyard endpoint hang a Plan/Replan/Synthesize call indefinitely.
const defaultRequestTimeout = 60 * time.Second

// errRedirectsNotFollowed is returned by the http.Client's CheckRedirect
// hook to refuse any 3xx response. A chat-completions endpoint has no
// legitimate reason to redirect; following one would risk sending the
// prompt (including goal/task context) to a different, possibly
// attacker-controlled, host - a lightweight SSRF-adjacent hardening even
// though BaseURL itself is operator-configured, trusted input.
var errRedirectsNotFollowed = errors.New("switchyard inference transport does not follow redirects")

// SwitchyardInferenceRouter adapts a *SwitchyardBackend to
// supervisor.ModelRouter and supervisor.InferenceTransport, so the
// Supervisor (internal/supervisor, which has zero provider-SDK
// dependencies) can obtain structured completions without ever
// constructing an OpenAI/Anthropic client or naming a concrete model.
//
// The dependency direction mirrors Phase 2A's
// collaboration.SupervisorAgentDirectory: the routing-aware package
// (modelrouting) depends on supervisor's interfaces to build an adapter;
// supervisor depends on nothing provider-specific.
//
// Route() never selects a concrete model - it resolves a logical ROUTE
// ALIAS per Stage (configurable via Routes, defaulting to "supervisor"
// for every stage). Switchyard itself resolves that alias to whatever
// concrete provider/model its own configuration maps it to; this code
// never sees or chooses that mapping. Complete() calls Switchyard's
// OpenAI-compatible chat-completions endpoint with a plain net/http POST
// (no OpenAI/Anthropic SDK), passing the route alias as the "model"
// field exactly as ConfigureParticipant already does for harness
// subprocesses via OPENAI_BASE_URL/ANTHROPIC_BASE_URL.
type SwitchyardInferenceRouter struct {
	Backend *SwitchyardBackend
	// Routes maps a Stage ("plan"/"replan"/"synthesize") to the route
	// alias to request. A missing entry falls back to DefaultRoute.
	Routes map[string]string
	// DefaultRoute is used when Routes has no entry for the requested
	// stage. Defaults to "supervisor" if empty.
	DefaultRoute string
}

// NewSwitchyardInferenceRouter constructs the adapter over a live
// SwitchyardBackend.
func NewSwitchyardInferenceRouter(backend *SwitchyardBackend) *SwitchyardInferenceRouter {
	return &SwitchyardInferenceRouter{Backend: backend}
}

func (r *SwitchyardInferenceRouter) routeFor(stage string) string {
	if route, ok := r.Routes[stage]; ok && route != "" {
		return route
	}
	if r.DefaultRoute != "" {
		return r.DefaultRoute
	}
	return "supervisor"
}

func (r *SwitchyardInferenceRouter) Route(ctx context.Context, req supervisor.ModelRoutingRequest) (supervisor.ModelRoutingDecision, error) {
	if r.Backend == nil || r.Backend.BaseURL == "" {
		return supervisor.ModelRoutingDecision{}, fmt.Errorf("switchyard base_url is not configured")
	}
	route := r.routeFor(req.Stage)
	return supervisor.ModelRoutingDecision{
		Provider: "switchyard",
		Model:    route,
		RouteID:  route,
		Reason:   fmt.Sprintf("switchyard route %q for stage %q", route, req.Stage),
	}, nil
}

var _ supervisor.ModelRouter = (*SwitchyardInferenceRouter)(nil)

// httpClient returns a client that shares r.Backend.HTTPClient's
// Transport (connection pooling) and base Timeout, but never follows
// redirects (see errRedirectsNotFollowed). It builds a new *http.Client
// value per call rather than mutating r.Backend.HTTPClient in place,
// since that field is shared with SwitchyardBackend.Health and must not
// be reconfigured by this adapter as a side effect.
func (r *SwitchyardInferenceRouter) httpClient() *http.Client {
	base := r.Backend.HTTPClient
	if base == nil {
		return &http.Client{
			CheckRedirect: denyRedirects,
		}
	}
	return &http.Client{
		Transport:     base.Transport,
		Timeout:       base.Timeout,
		CheckRedirect: denyRedirects,
	}
}

func denyRedirects(req *http.Request, via []*http.Request) error {
	return errRedirectsNotFollowed
}

// chatCompletionRequest/-Response mirror only the subset of the OpenAI
// chat-completions wire format this adapter needs - no SDK, just the
// documented JSON shape, sent to Switchyard's own OpenAI-compatible
// surface (the same surface ConfigureParticipant already points harness
// subprocesses at).
type chatCompletionRequest struct {
	Model    string                  `json:"model"`
	Messages []chatCompletionMessage `json:"messages"`
}

type chatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message chatCompletionMessage `json:"message"`
	} `json:"choices"`
}

func (r *SwitchyardInferenceRouter) Complete(ctx context.Context, decision supervisor.ModelRoutingDecision, systemPrompt, userPrompt string, maxResponseBytes int) (supervisor.InferenceResult, error) {
	if r.Backend == nil || r.Backend.BaseURL == "" {
		return supervisor.InferenceResult{}, fmt.Errorf("switchyard base_url is not configured")
	}
	if decision.Model == "" {
		return supervisor.InferenceResult{}, fmt.Errorf("model routing decision carries no route alias")
	}

	reqBody, err := json.Marshal(chatCompletionRequest{
		Model: decision.Model,
		Messages: []chatCompletionMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
	})
	if err != nil {
		return supervisor.InferenceResult{}, fmt.Errorf("encode chat completion request: %w", err)
	}

	// Bound every call by defaultRequestTimeout regardless of the
	// caller's own ctx deadline (if any) and regardless of whatever
	// Timeout r.Backend.HTTPClient's *http.Client carries (including
	// zero, i.e. none) - see defaultRequestTimeout's doc comment.
	timeoutCtx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(timeoutCtx, http.MethodPost, r.Backend.BaseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return supervisor.InferenceResult{}, fmt.Errorf("create chat completion request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient().Do(httpReq)
	if err != nil {
		return supervisor.InferenceResult{}, fmt.Errorf("switchyard chat completion request failed: %w", err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, int64(maxResponseBytes)+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return supervisor.InferenceResult{}, fmt.Errorf("read switchyard chat completion response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return supervisor.InferenceResult{}, fmt.Errorf("switchyard chat completion response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return supervisor.InferenceResult{}, fmt.Errorf("switchyard chat completion returned HTTP %d", resp.StatusCode)
	}

	var parsed chatCompletionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return supervisor.InferenceResult{}, fmt.Errorf("decode switchyard chat completion response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return supervisor.InferenceResult{}, fmt.Errorf("switchyard chat completion response has no choices")
	}

	return supervisor.InferenceResult{
		Content:        parsed.Choices[0].Message.Content,
		ServedModel:    parsed.Model,
		ServedProvider: "switchyard",
	}, nil
}

var _ supervisor.InferenceTransport = (*SwitchyardInferenceRouter)(nil)
