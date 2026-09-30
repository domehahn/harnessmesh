package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/creditguard"
)

// fakeResponsesServer mimics the real https://api.openai.com/v1/responses
// endpoint's SSE shape closely enough to test SubscriptionBackend's
// request-building and stream-relay logic without any real network call to
// OpenAI's infrastructure or any real ChatGPT account.
type fakeResponsesServer struct {
	*httptest.Server
	lastAuth string
	mode     string // "normal", "unauthorized", "malformed"
}

func newFakeResponsesServer(mode string) *fakeResponsesServer {
	f := &fakeResponsesServer{mode: mode}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeResponsesServer) handle(w http.ResponseWriter, r *http.Request) {
	f.lastAuth = r.Header.Get("Authorization")

	if f.mode == "unauthorized" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	write := func(ev StreamEvent) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
		if flusher != nil {
			flusher.Flush()
		}
	}

	switch f.mode {
	case "malformed":
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {not valid json\n\n")
	default:
		oi, ci := 0, 0
		write(StreamEvent{Type: "response.created", Response: &Response{ID: "resp_fake", Object: "response", Status: StatusInProgress, Output: []OutputItem{}}})
		write(StreamEvent{Type: "response.output_item.added", OutputIndex: &oi, Item: &OutputItem{ID: "resp_fake_msg_0", Type: "message", Status: StatusInProgress, Role: "assistant"}})
		write(StreamEvent{Type: "response.output_text.delta", ItemID: "resp_fake_msg_0", OutputIndex: &oi, ContentIndex: &ci, Delta: "hello from ChatGPT plan"})
		write(StreamEvent{Type: "response.completed", Response: &Response{
			ID: "resp_fake", Object: "response", Status: StatusCompleted,
			Output: []OutputItem{{ID: "resp_fake_msg_0", Type: "message", Status: StatusCompleted, Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: "hello from ChatGPT plan"}}}},
			Usage:  &Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7},
		}})
	}
}

func newTestSubscriptionBackend(t *testing.T, srv *fakeResponsesServer, tokens *SIWCTokenSet) *SubscriptionBackend {
	t.Helper()
	tokenPath := t.TempDir() + "/auth.json"
	if tokens == nil {
		tokens = &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "test-access-token", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(time.Hour)}
	}
	if err := saveSIWCTokenSet(tokenPath, tokens); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: srv.URL, responsesURL: srv.URL, httpClient: http.DefaultClient}
	return b
}

func TestSubscriptionBackend_NormalStream_UsesChatGPTPlanCounterOnly(t *testing.T) {
	creditguard.ResetForTest()
	srv := newFakeResponsesServer("normal")
	defer srv.Close()
	b := newTestSubscriptionBackend(t, srv, nil)

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "gpt-5-chatgpt", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink)
	if err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	if got := creditguard.Calls(creditguard.BackendChatGPTPlanUsage); got != 1 {
		t.Fatalf("expected exactly 1 chatgpt-plan-usage call recorded, got %d", got)
	}
	if got := creditguard.Calls(creditguard.BackendOpenAIAPI); got != 0 {
		t.Fatalf("expected 0 metered OpenAI API calls (this is a distinct, non-metered path), got %d", got)
	}
	if got := creditguard.Calls(creditguard.BackendCodex); got != 0 {
		t.Fatalf("expected 0 Codex invocations, got %d", got)
	}

	if srv.lastAuth != "Bearer test-access-token" {
		t.Fatalf("expected the OAuth access token as a bearer token, got %q", srv.lastAuth)
	}

	var sawText, sawCompleted bool
	for _, ev := range sink.events {
		if ev.Type == "response.output_text.delta" && ev.Delta == "hello from ChatGPT plan" {
			sawText = true
		}
		if ev.Type == "response.completed" {
			sawCompleted = true
		}
	}
	if !sawText || !sawCompleted {
		t.Fatalf("expected to relay the server's own text delta and completion events, got: %+v", sink.events)
	}
}

func TestSubscriptionBackend_Unauthorized_NoStoredToken(t *testing.T) {
	creditguard.ResetForTest()
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", t.TempDir()+"/nonexistent.json")

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink)
	if _, ok := err.(*UnauthorizedError); !ok {
		t.Fatalf("expected UnauthorizedError when no token has ever been stored (no login performed), got %T: %v", err, err)
	}
	// Even an unauthorized/not-logged-in attempt should not touch the
	// metered API or Codex paths.
	if creditguard.Calls(creditguard.BackendOpenAIAPI) != 0 || creditguard.Calls(creditguard.BackendCodex) != 0 {
		t.Fatalf("expected zero metered/Codex calls even when not logged in")
	}
}

func TestSubscriptionBackend_ServerRejectsToken(t *testing.T) {
	creditguard.ResetForTest()
	srv := newFakeResponsesServer("unauthorized")
	defer srv.Close()
	b := newTestSubscriptionBackend(t, srv, nil)

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink)
	if _, ok := err.(*UnauthorizedError); !ok {
		t.Fatalf("expected UnauthorizedError when the server rejects the token, got %T: %v", err, err)
	}
}

func TestSubscriptionBackend_MalformedStreamEvent(t *testing.T) {
	creditguard.ResetForTest()
	srv := newFakeResponsesServer("malformed")
	defer srv.Close()
	b := newTestSubscriptionBackend(t, srv, nil)

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink)
	if _, ok := err.(*StreamInterruptedError); !ok {
		t.Fatalf("expected StreamInterruptedError for a malformed backend event, got %T: %v", err, err)
	}
}

func TestSubscriptionBackend_ExpiredTokenIsRefreshed(t *testing.T) {
	creditguard.ResetForTest()
	responsesSrv := newFakeResponsesServer("normal")
	defer responsesSrv.Close()

	var refreshCalled bool
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			refreshCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed-access", "refresh_token": "refreshed-refresh", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	tokenPath := t.TempDir() + "/auth.json"
	expired := &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Hour)}
	if err := saveSIWCTokenSet(tokenPath, expired); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: tokenSrv.URL, responsesURL: responsesSrv.URL, httpClient: http.DefaultClient}

	sink := newCollectingSink()
	if err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	if !refreshCalled {
		t.Fatalf("expected an expired token to trigger a refresh_token grant")
	}
	if responsesSrv.lastAuth != "Bearer refreshed-access" {
		t.Fatalf("expected the request to use the refreshed access token, got %q", responsesSrv.lastAuth)
	}

	persisted, err := loadSIWCTokenSet(tokenPath)
	if err != nil {
		t.Fatalf("reload persisted token: %v", err)
	}
	if persisted.AccessToken != "refreshed-access" {
		t.Fatalf("expected the refreshed token to be persisted, got %+v", persisted)
	}
}

func TestSubscriptionBackend_Health(t *testing.T) {
	tokenPath := t.TempDir() + "/auth.json"
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	if err := b.Health(context.Background()); err == nil {
		t.Fatalf("expected Health to fail with no stored credentials")
	}

	valid := &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}
	if err := saveSIWCTokenSet(tokenPath, valid); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	b2 := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	if err := b2.Health(context.Background()); err != nil {
		t.Fatalf("expected Health to succeed with a valid stored credential, got %v", err)
	}
}

func TestSubscriptionBackend_Type(t *testing.T) {
	b := NewSubscriptionBackend("chatgpt-subscription")
	if b.Type() != "chatgpt-subscription" {
		t.Fatalf("expected Type() chatgpt-subscription, got %q", b.Type())
	}
	caps := b.Capabilities()
	if !caps.Streaming || !caps.Tools {
		t.Fatalf("expected streaming and tools capabilities to be advertised, got %+v", caps)
	}
}
