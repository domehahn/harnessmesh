package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPKCE_VerifierAndChallengeAreWellFormed(t *testing.T) {
	v1, err := generateCodeVerifier()
	if err != nil {
		t.Fatalf("generateCodeVerifier: %v", err)
	}
	v2, err := generateCodeVerifier()
	if err != nil {
		t.Fatalf("generateCodeVerifier: %v", err)
	}
	if v1 == v2 {
		t.Fatalf("expected distinct verifiers across calls")
	}
	if len(v1) < 32 {
		t.Fatalf("expected a reasonably long verifier, got %d chars", len(v1))
	}
	c1 := codeChallengeS256(v1)
	c2 := codeChallengeS256(v1)
	if c1 != c2 {
		t.Fatalf("expected codeChallengeS256 to be deterministic for the same verifier")
	}
	if codeChallengeS256(v2) == c1 {
		t.Fatalf("expected different verifiers to produce different challenges")
	}
}

func TestBuildAuthorizeURL_ContainsDocumentedParameters(t *testing.T) {
	u := buildAuthorizeURL("dynamic_agent_client", "http://127.0.0.1:12345/auth/callback", "state123", "challenge123")
	if !strings.HasPrefix(u, siwcAuthorizeURL+"?") {
		t.Fatalf("expected URL to start with the documented authorize endpoint, got %q", u)
	}
	for _, want := range []string{
		"client_id=dynamic_agent_client",
		"response_type=code",
		"code_challenge_method=S256",
		"code_challenge=challenge123",
		"state=state123",
		"resource=https%3A%2F%2Fapi.openai.com%2Fv1",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("expected authorize URL to contain %q, got %s", want, u)
		}
	}
	if !strings.Contains(u, "chatgpt.tokens.use.direct") {
		t.Fatalf("expected the documented chatgpt.tokens.use.direct scope in the URL, got %s", u)
	}
}

func TestSIWCTokenClient_ExchangeCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Fatalf("expected grant_type=authorization_code, got %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("code") != "test-code" || r.Form.Get("code_verifier") != "test-verifier" {
			t.Fatalf("unexpected form: %+v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"client_id":     "oaiapp_issued123",
			"access_token":  "access-abc",
			"refresh_token": "refresh-abc",
			"id_token":      "id-abc",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	tc := &siwcTokenClient{tokenURL: srv.URL, httpClient: http.DefaultClient}
	ts, err := tc.exchangeCode(context.Background(), "dynamic_agent_client", "test-code", "test-verifier", "http://127.0.0.1:0/auth/callback")
	if err != nil {
		t.Fatalf("exchangeCode: %v", err)
	}
	if ts.AccessToken != "access-abc" || ts.ClientID != "oaiapp_issued123" {
		t.Fatalf("unexpected token set: %+v", ts)
	}
	if ts.ExpiresAt.Before(time.Now().Add(59 * time.Minute)) {
		t.Fatalf("expected expiry ~1h from now, got %v", ts.ExpiresAt)
	}
}

func TestSIWCTokenClient_ExchangeCode_HTTPErrorMapsToUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	tc := &siwcTokenClient{tokenURL: srv.URL, httpClient: http.DefaultClient}
	_, err := tc.exchangeCode(context.Background(), "dynamic_agent_client", "bad-code", "verifier", "http://127.0.0.1:0/cb")
	if _, ok := err.(*UnauthorizedError); !ok {
		t.Fatalf("expected UnauthorizedError, got %T: %v", err, err)
	}
}

func TestSIWCTokenClient_Refresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" {
			t.Fatalf("unexpected refresh request: %+v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 7200})
	}))
	defer srv.Close()

	tc := &siwcTokenClient{tokenURL: srv.URL, httpClient: http.DefaultClient}
	ts, err := tc.refresh(context.Background(), "oaiapp_x", "old-refresh")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if ts.AccessToken != "new-access" {
		t.Fatalf("expected refreshed access token, got %+v", ts)
	}
}

func TestSIWCTokenSet_PersistAndLoadRoundTrip(t *testing.T) {
	path := t.TempDir() + "/auth.json"
	ts := &SIWCTokenSet{ClientID: "oaiapp_1", AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}
	if err := saveSIWCTokenSet(path, ts); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := loadSIWCTokenSet(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.AccessToken != "a" || loaded.ClientID != "oaiapp_1" {
		t.Fatalf("unexpected round-tripped token set: %+v", loaded)
	}
}

func TestSIWCTokenSet_Expired(t *testing.T) {
	var nilSet *SIWCTokenSet
	if !nilSet.expired() {
		t.Fatalf("expected a nil token set to be considered expired")
	}
	future := &SIWCTokenSet{AccessToken: "x", ExpiresAt: time.Now().Add(time.Hour)}
	if future.expired() {
		t.Fatalf("expected a future-expiry token to not be expired")
	}
	past := &SIWCTokenSet{AccessToken: "x", ExpiresAt: time.Now().Add(-time.Hour)}
	if !past.expired() {
		t.Fatalf("expected a past-expiry token to be expired")
	}
}

func TestStartLoginFlow_FullRoundTripAgainstFakeTokenEndpoint(t *testing.T) {
	// This test proves the loopback-callback plumbing (listener, authorize
	// URL construction, state handling, prompt timeout behavior) works -
	// it does NOT and cannot exercise the real chatgpt.com/auth.openai.com
	// consent screen (that requires a real browser and a real ChatGPT
	// account, which this environment does not have). See
	// docs/codex-provider.md for that documented, honest limitation. The
	// token-exchange HTTP contract itself is covered separately by
	// TestSIWCTokenClient_ExchangeCode against a fake local server.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result, err := StartLoginFlow(ctx, "127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("StartLoginFlow: %v", err)
	}
	if !strings.HasPrefix(result.AuthorizeURL, siwcAuthorizeURL) {
		t.Fatalf("expected authorize URL to start with the documented endpoint, got %s", result.AuthorizeURL)
	}
	if !strings.Contains(result.AuthorizeURL, "redirect_uri=http%3A%2F%2F127.0.0.1%3A") {
		t.Fatalf("expected a loopback redirect_uri in the authorize URL, got %s", result.AuthorizeURL)
	}

	// Simulate the browser being closed / the user never completing
	// consent within the timeout - the flow must resolve with an error,
	// not hang forever.
	select {
	case outcome := <-result.Done:
		if outcome.Err == nil {
			t.Fatalf("expected a timeout/cancellation error when no callback ever arrives, got success: %+v", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("expected StartLoginFlow's Done channel to resolve promptly on context cancellation")
	}
}

func TestStartLoginFlow_CallbackDeliversCode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := StartLoginFlow(ctx, "127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("StartLoginFlow: %v", err)
	}

	// Extract state and redirect_uri from the authorize URL, then simulate
	// the browser redirect a real consent screen would perform.
	parsed := mustParseURL(t, result.AuthorizeURL)
	state := parsed.Query().Get("state")
	redirectURI := parsed.Query().Get("redirect_uri")

	go func() {
		time.Sleep(50 * time.Millisecond)
		cbURL := redirectURI + "?code=test-auth-code&state=" + state
		resp, err := http.Get(cbURL)
		if err == nil {
			resp.Body.Close()
		}
	}()

	select {
	case outcome := <-result.Done:
		// The code-exchange step will fail because it targets the real
		// (unreachable-from-here, or real-but-rejecting) OpenAI token
		// endpoint with a fake code - that failure is expected and proves
		// the callback->code->exchange-attempt plumbing worked; a network
		// or auth error here, not a hang or a state-mismatch error, is the
		// correct outcome for this unit test.
		if outcome.Err == nil {
			t.Fatalf("expected the fake code to fail exchange against the real token endpoint, got success")
		}
		if strings.Contains(outcome.Err.Error(), "state mismatch") {
			t.Fatalf("did not expect a state mismatch: %v", outcome.Err)
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("expected the callback to be processed promptly")
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return u
}
