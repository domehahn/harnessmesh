package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/domehahn/harnessmesh/internal/creditguard"
)

// SubscriptionBackend implements InferenceBackend using OpenAI's officially
// documented "Sign in with ChatGPT" mechanism (see chatgpt_siwc.go's
// package-level doc comment for full citations). It is a real, working
// backend - not a stub - as of the research in this mission
// (2026-09-30), because that research found a genuinely official,
// synchronous, externally-triggerable Responses API path backed by a
// user's ChatGPT plan entitlement.
//
// Read this before enabling it: it does NOT touch metered OpenAI API
// billing and NEVER invokes the Codex CLI - both are proven by dedicated
// tests - but on ChatGPT plans where Codex is bundled, OpenAI's own
// documentation states this draws from the SAME usage allowance Codex
// itself draws from (help.openai.com "ChatGPT Work and Codex"). See
// docs/codex-provider.md's "ChatGPT-subscription backend" section for the
// full, honest accounting before relying on this in a workflow that
// assumes it is free or independent of Codex usage.
type SubscriptionBackend struct {
	name      string
	tokenPath string
	client    *siwcTokenClient
	// requestDiagnostics is nil (a complete no-op) unless explicitly opted
	// into via HARNESSMESH_SIWC_DEBUG=1 or injected by a test.
	requestDiagnostics RequestShapeDiagnosticsSink

	mu     sync.Mutex
	tokens *SIWCTokenSet
}

func NewSubscriptionBackend(name string) *SubscriptionBackend {
	return &SubscriptionBackend{name: name, tokenPath: DefaultSIWCTokenPath(), client: newSIWCTokenClient(), requestDiagnostics: requestShapeDiagnosticsSinkFromEnv()}
}

// NewSubscriptionBackendWithTokenPath is used by tests (and can be used by
// operators via config) to point at a non-default credential file.
func NewSubscriptionBackendWithTokenPath(name, tokenPath string) *SubscriptionBackend {
	return &SubscriptionBackend{name: name, tokenPath: tokenPath, client: newSIWCTokenClient(), requestDiagnostics: requestShapeDiagnosticsSinkFromEnv()}
}

func (b *SubscriptionBackend) Name() string { return b.name }
func (b *SubscriptionBackend) Type() string { return "chatgpt-subscription" }

func (b *SubscriptionBackend) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Tools: true, Reasoning: true}
}

// ensureValidToken loads the persisted token set (from a prior `harnessmesh
// provider auth chatgpt` login), refreshing it if expired. It never
// initiates an interactive browser login itself - StartLoginFlow (in
// chatgpt_siwc.go) is a separate, explicit, user-initiated action, exactly
// like Codex CLI's own `codex login` is separate from running a task.
func (b *SubscriptionBackend) ensureValidToken(ctx context.Context) (*SIWCTokenSet, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.tokens == nil {
		ts, err := loadSIWCTokenSet(b.tokenPath)
		if err != nil {
			return nil, &UnauthorizedError{}
		}
		b.tokens = ts
	}
	if !b.tokens.expired() {
		return b.tokens, nil
	}
	if b.tokens.RefreshToken == "" {
		return nil, &UnauthorizedError{}
	}
	refreshed, err := b.client.refresh(ctx, b.tokens.ClientID, b.tokens.RefreshToken)
	if err != nil {
		return nil, err
	}
	if err := saveSIWCTokenSet(b.tokenPath, refreshed); err != nil {
		return nil, fmt.Errorf("persist refreshed SIWC token: %w", err)
	}
	b.tokens = refreshed
	return b.tokens, nil
}

// SIWCModel is one entry from the documented model-discovery endpoint
// (developers.openai.com/siwc/token-sharing-open-source/models-and-inference:
// "Show display_name in your UI and pass the selected slug as model in the
// next step").
type SIWCModel struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Visibility  string `json:"visibility"`
}

// ListModels queries the authenticated account's actual model catalog via
// the documented GET https://api.openai.com/v1/models endpoint, filtered
// to visibility:"list" entries - so callers (CLI, doctor, a future picker
// UI) verify model availability against the real, signed-in account
// instead of hardcoding a model id that may not be entitled or may not
// exist under this name for this account.
func (b *SubscriptionBackend) ListModels(ctx context.Context) ([]SIWCModel, error) {
	tokens, err := b.ensureValidToken(ctx)
	if err != nil {
		return nil, err
	}
	modelsURL := strings.TrimSuffix(b.client.responsesURL, "/responses") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	resp, err := b.client.httpClient.Do(req)
	if err != nil {
		return nil, &BackendUnavailableError{Backend: b.name, Reason: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, mapResponsesAPIError(resp.StatusCode, body)
	}
	var payload struct {
		Models []SIWCModel `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("parse models response: %w", err)
	}
	out := make([]SIWCModel, 0, len(payload.Models))
	for _, m := range payload.Models {
		if m.Visibility == "list" {
			out = append(out, m)
		}
	}
	return out, nil
}

func (b *SubscriptionBackend) Health(ctx context.Context) error {
	// A network round trip against the Responses endpoint isn't
	// documented as safe to do "for free" as a bare health probe (unlike
	// e.g. an OpenAI-compatible server's GET /models), so Health here is
	// deliberately a structural check only: does a valid, non-expired (or
	// refreshable) credential exist locally. This intentionally can't
	// prove the account's plan is currently eligible/entitled server-side
	// - only a real request can.
	_, err := b.ensureValidToken(ctx)
	return err
}

// StreamResponse sends req to https://api.openai.com/v1/responses,
// authenticated with the user's ChatGPT-plan-scoped OAuth access token
// (never an API key), and re-emits the server's own Responses-API SSE
// stream directly as StreamEvents - the wire shape already matches, so
// this is parsing, not translation.
func (b *SubscriptionBackend) StreamResponse(ctx context.Context, req Request, sink Sink) error {
	// Recorded unconditionally and first, before any network activity, so
	// tests can prove this line - and only this line, never
	// creditguard.BackendOpenAIAPI or BackendCodex - is what increments
	// when this backend is used.
	creditguard.RecordCall(creditguard.BackendChatGPTPlanUsage)

	tokens, err := b.ensureValidToken(ctx)
	if err != nil {
		return err
	}

	normalized, err := normalizeForSIWC(req)
	if b.requestDiagnostics != nil {
		b.requestDiagnostics.RecordRequestShape(buildRequestShapeDiagnostic(req, normalized, err))
	}
	if err != nil {
		return err
	}
	body, err := json.Marshal(normalized)
	if err != nil {
		return err
	}

	httpReq, err := newSIWCResponsesRequest(ctx, b.client.responsesURL, tokens.AccessToken, body)
	if err != nil {
		return err
	}

	resp, err := b.client.httpClient.Do(httpReq)
	if err != nil {
		return &BackendUnavailableError{Backend: b.name, Reason: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Errors (including the documented subscription_sharing_* and
		// chatpass_v2_scope_not_authorized codes) arrive as an ordinary
		// JSON body, not a stream, on a non-2xx response - read it whole
		// and map it per errors-and-recovery.md's documented table
		// (chatgpt_errors.go), rather than treating every 4xx/5xx alike.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return mapResponsesAPIError(resp.StatusCode, errBody)
	}

	return relaySIWCStream(ctx, resp.Body, sink)
}

// relaySIWCStream reads the server's own Responses-API SSE stream and
// forwards each event to sink, dropping only truly malformed lines (with a
// stream error) rather than silently swallowing them.
func relaySIWCStream(ctx context.Context, body io.Reader, sink Sink) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var pendingType string
	for scanner.Scan() {
		select {
		case <-sink.Done():
			return &StreamInterruptedError{Reason: "client disconnected"}
		case <-ctx.Done():
			return &StreamInterruptedError{Reason: "request cancelled"}
		default:
		}
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			pendingType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" {
				continue
			}
			var ev StreamEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				_ = sink.Send(StreamEvent{Type: "error", Error: &ResponseError{Code: "malformed_backend_event", Message: "backend produced a malformed stream event", Type: "backend_error"}})
				return &StreamInterruptedError{Reason: "malformed backend event"}
			}
			if ev.Type == "" {
				ev.Type = pendingType
			}
			if err := sink.Send(ev); err != nil {
				return err
			}
			if ev.Type == "response.completed" || ev.Type == "response.failed" {
				return nil
			}
		}
	}
	if err := scannerErr(scanner); err != nil {
		return &StreamInterruptedError{Reason: err.Error()}
	}
	return nil
}

func scannerErr(s *bufio.Scanner) error { return s.Err() }
