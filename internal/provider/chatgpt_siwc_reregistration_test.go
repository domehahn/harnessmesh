package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file reproduces and proves the fix for a real first-registration
// lifecycle bug: a dynamic-registration callback's issued client_id was
// being discarded whenever the accompanying authorization-code exchange
// returned invalid_grant, causing every subsequent attempt to re-send
// client_id=dynamic_agent_client (and agent_name_hint) instead of the
// already-issued, persisted client_id. Per the documented SIWC OSS
// lifecycle, the issued client_id must be retained independently of
// whether that first exchange succeeds.

// fakeRegistrationTokenServer plays the token endpoint across a two-attempt
// login lifecycle: the first authorization_code exchange fails with
// invalid_grant, the second succeeds. It records every call so tests can
// assert exactly which client_id and redirect_uri each request used.
type fakeRegistrationTokenServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls []url.Values
	t     *testing.T
}

func newFakeRegistrationTokenServer(t *testing.T) *fakeRegistrationTokenServer {
	f := &fakeRegistrationTokenServer{t: t}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeRegistrationTokenServer) handle(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.calls = append(f.calls, r.Form)
	callNum := len(f.calls)
	f.mu.Unlock()

	// Requirement 2: dynamic_agent_client must NEVER reach the token
	// endpoint - only the very first authorize request may use it; every
	// token exchange must use a real client_id.
	if r.Form.Get("client_id") == siwcBootstrapClientID {
		f.t.Errorf("dynamic_agent_client must never be sent to oauth/token, but call #%d did (form=%+v)", callNum, r.Form)
	}

	w.Header().Set("Content-Type", "application/json")
	if callNum == 1 {
		// First attempt's exchange fails with the documented invalid_grant
		// error - this is the exact failure mode being reproduced.
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":             "invalid_grant",
			"error_description": "authorization code is invalid or has already been used",
		})
		return
	}
	// Second attempt (the retry) succeeds.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  "retry-access-token",
		"refresh_token": "retry-refresh-token",
		"expires_in":    3600,
		"scopes":        []string{"openid", "profile", "email", "offline_access", "resource.invoke", siwcPlanUsageScope},
	})
}

func (f *fakeRegistrationTokenServer) Calls() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]url.Values, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestSIWCFirstRegistration_InvalidGrant_RetryLifecycle(t *testing.T) {
	dir := t.TempDir()
	hostIDPath := dir + "/host-id"
	clientIDPath := dir + "/client-id"
	const issuedClientID = "oaiapp_issued_abc123"

	tokenSrv := newFakeRegistrationTokenServer(t)
	defer tokenSrv.Close()
	tc := &siwcTokenClient{tokenURL: tokenSrv.URL, responsesURL: tokenSrv.URL, httpClient: http.DefaultClient}
	idv := newIDTokenVerifier() // unused: neither fake token response includes an id_token

	// ================= FIRST ATTEMPT =================
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()
	result1, err := startLoginFlowWithVerifier(ctx1, "127.0.0.1:0", "", tc, idv, hostIDPath, clientIDPath)
	if err != nil {
		t.Fatalf("first startLoginFlowWithVerifier: %v", err)
	}
	u1 := mustParseURL(t, result1.AuthorizeURL)

	if got := u1.Query().Get("client_id"); got != siwcBootstrapClientID {
		t.Fatalf("expected the first attempt to authorize with client_id=%s, got %q", siwcBootstrapClientID, got)
	}
	if got := u1.Query().Get("agent_name_hint"); got != siwcAgentNameHint {
		t.Fatalf("expected the first attempt to include agent_name_hint=%s, got %q", siwcAgentNameHint, got)
	}
	state1 := u1.Query().Get("state")
	nonce1 := u1.Query().Get("nonce")
	challenge1 := u1.Query().Get("code_challenge")
	redirectURI1 := u1.Query().Get("redirect_uri")
	hostID1 := u1.Query().Get("ext_agent_host_id")
	if hostID1 == "" || !strings.HasPrefix(hostID1, "urn:uuid:") {
		t.Fatalf("expected a urn:uuid: ext_agent_host_id, got %q", hostID1)
	}

	// Simulate the browser redirect: a successful dynamic-registration
	// callback echoes the issued client_id alongside code/state, per the
	// documented callback shape.
	go func() {
		time.Sleep(30 * time.Millisecond)
		cbURL := redirectURI1 + "?code=first-attempt-code&state=" + state1 + "&client_id=" + issuedClientID
		resp, err := http.Get(cbURL)
		if err == nil {
			resp.Body.Close()
		}
	}()

	outcome1 := <-result1.Done
	if outcome1.Err == nil {
		t.Fatalf("expected the first attempt's token exchange to fail with invalid_grant, got success: %+v", outcome1.Tokens)
	}
	if _, ok := outcome1.Err.(*SubscriptionReauthRequiredError); !ok {
		t.Fatalf("expected invalid_grant to map to SubscriptionReauthRequiredError, got %T: %v", outcome1.Err, outcome1.Err)
	}

	// Requirement 3: the issued client_id registration must be retained
	// even though the exchange failed.
	registered := loadRegisteredClientID(clientIDPath)
	if registered != issuedClientID {
		t.Fatalf("expected the issued client_id to survive invalid_grant, got %q want %q", registered, issuedClientID)
	}
	// Requirement: do NOT retain unusable access/refresh tokens - nothing
	// in this flow ever wrote a full credential file (SaveSIWCTokenSetTo is
	// only ever called by the caller on success), so there is nothing to
	// assert away here beyond outcome1.Tokens being nil.
	if outcome1.Tokens != nil {
		t.Fatalf("expected no tokens on a failed exchange, got %+v", outcome1.Tokens)
	}

	// ================= SECOND ATTEMPT (retry) =================
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	result2, err := startLoginFlowWithVerifier(ctx2, "127.0.0.1:0", "", tc, idv, hostIDPath, clientIDPath)
	if err != nil {
		t.Fatalf("second startLoginFlowWithVerifier: %v", err)
	}
	u2 := mustParseURL(t, result2.AuthorizeURL)

	// Requirement 4: retry must use the issued client_id, never the
	// bootstrap one again.
	if got := u2.Query().Get("client_id"); got != issuedClientID {
		t.Fatalf("expected the retry to authorize with the issued client_id %q, got %q", issuedClientID, got)
	}
	// Requirement 5: agent_name_hint must be omitted once a client_id has
	// been issued and persisted.
	if got := u2.Query().Get("agent_name_hint"); got != "" {
		t.Fatalf("expected agent_name_hint to be omitted on retry, got %q", got)
	}
	// Requirement 6: fresh state/nonce/PKCE for the retry.
	state2 := u2.Query().Get("state")
	nonce2 := u2.Query().Get("nonce")
	challenge2 := u2.Query().Get("code_challenge")
	if state2 == state1 {
		t.Fatalf("expected a fresh state on retry, got the same value %q", state2)
	}
	if nonce2 == nonce1 {
		t.Fatalf("expected a fresh nonce on retry, got the same value %q", nonce2)
	}
	if challenge2 == challenge1 {
		t.Fatalf("expected a fresh PKCE code_challenge on retry, got the same value %q", challenge2)
	}
	// Requirement 7: the stable ext_agent_host_id must survive the retry.
	if got := u2.Query().Get("ext_agent_host_id"); got != hostID1 {
		t.Fatalf("expected the same ext_agent_host_id across attempts, got %q want %q", got, hostID1)
	}
	redirectURI2 := u2.Query().Get("redirect_uri")

	// This retry's callback is a returning-registration login: OpenAI does
	// not need to echo client_id again since it's already known.
	go func() {
		time.Sleep(30 * time.Millisecond)
		cbURL := redirectURI2 + "?code=retry-code&state=" + state2
		resp, err := http.Get(cbURL)
		if err == nil {
			resp.Body.Close()
		}
	}()

	outcome2 := <-result2.Done
	if outcome2.Err != nil {
		t.Fatalf("expected the retry to succeed, got error: %v", outcome2.Err)
	}
	// Requirement 8: successful retry persists full credentials, and the
	// persisted client_id is the issued one, not the bootstrap value.
	if outcome2.Tokens.ClientID != issuedClientID {
		t.Fatalf("expected the retried token set's client_id to be %q, got %q", issuedClientID, outcome2.Tokens.ClientID)
	}
	if outcome2.Tokens.AccessToken != "retry-access-token" || outcome2.Tokens.RefreshToken != "retry-refresh-token" {
		t.Fatalf("expected the retry's real access/refresh tokens to be captured, got %+v", outcome2.Tokens)
	}
	if outcome2.Tokens.ExtAgentHostID != hostID1 {
		t.Fatalf("expected the persisted token set to carry the same stable ext_agent_host_id, got %q want %q", outcome2.Tokens.ExtAgentHostID, hostID1)
	}
	tokenPath := dir + "/auth.json"
	if err := SaveSIWCTokenSetTo(tokenPath, outcome2.Tokens); err != nil {
		t.Fatalf("SaveSIWCTokenSetTo: %v", err)
	}
	reloaded, err := loadSIWCTokenSet(tokenPath)
	if err != nil {
		t.Fatalf("reload persisted credential: %v", err)
	}
	if reloaded.ClientID != issuedClientID || reloaded.AccessToken != "retry-access-token" {
		t.Fatalf("expected the full credential to round-trip through persistence, got %+v", reloaded)
	}

	// Requirement 1 & "redirect_uri byte-for-byte identical": verify what
	// was actually sent to the token endpoint on both attempts.
	calls := tokenSrv.Calls()
	if len(calls) != 2 {
		t.Fatalf("expected exactly 2 token-endpoint calls (one per attempt), got %d", len(calls))
	}
	if calls[0].Get("client_id") != issuedClientID {
		t.Fatalf("expected the FIRST attempt's token exchange to already use the issued client_id (from the callback's client_id param), got %q", calls[0].Get("client_id"))
	}
	if calls[1].Get("client_id") != issuedClientID {
		t.Fatalf("expected the retry's token exchange to use the issued client_id, got %q", calls[1].Get("client_id"))
	}
	if calls[0].Get("redirect_uri") != redirectURI1 {
		t.Fatalf("expected the first exchange's redirect_uri to byte-for-byte match its authorize request's redirect_uri;\n exchange=%q\n authorize=%q", calls[0].Get("redirect_uri"), redirectURI1)
	}
	if calls[1].Get("redirect_uri") != redirectURI2 {
		t.Fatalf("expected the retry's exchange redirect_uri to byte-for-byte match its authorize request's redirect_uri;\n exchange=%q\n authorize=%q", calls[1].Get("redirect_uri"), redirectURI2)
	}

	// Requirement 10 (proxy check): the persisted client-id registration
	// file contains only the bare client_id - no access/refresh tokens or
	// other secrets ever get written to it.
	raw, err := os.ReadFile(clientIDPath)
	if err != nil {
		t.Fatalf("read clientIDPath: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != issuedClientID {
		t.Fatalf("expected the client-id registration file to contain exactly the issued client_id and nothing else, got %q", got)
	}
}

// Requirement 9: a stored credential's refresh MUST use the issued
// client_id (never dynamic_agent_client), since by the time a refresh
// happens registration has always already completed.
func TestSIWCRefresh_UsesIssuedClientID_NeverBootstrap(t *testing.T) {
	const issuedClientID = "oaiapp_refresh_test_xyz"
	var gotClientID string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotClientID = r.Form.Get("client_id")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	responsesSrv := newFakeResponsesServer("normal")
	defer responsesSrv.Close()

	tokenPath := t.TempDir() + "/auth.json"
	if err := saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: issuedClientID, AccessToken: "old", RefreshToken: "old-r", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: tokenSrv.URL, responsesURL: responsesSrv.URL, httpClient: http.DefaultClient}

	sink := newCollectingSink()
	if err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	if gotClientID != issuedClientID {
		t.Fatalf("expected refresh to use the issued client_id %q, got %q", issuedClientID, gotClientID)
	}
	if gotClientID == siwcBootstrapClientID {
		t.Fatalf("refresh must never use the bootstrap client_id")
	}
}

// TestBuildAuthorizeURL_IssuedClientID_OmitsAgentNameHint is a focused unit
// test isolating requirement 5 from the full lifecycle test above.
func TestBuildAuthorizeURL_IssuedClientID_OmitsAgentNameHint(t *testing.T) {
	u := buildAuthorizeURL("oaiapp_already_issued", "http://127.0.0.1:0/cb", "s", "n", "c", "urn:uuid:host-1")
	if strings.Contains(u, "agent_name_hint") {
		t.Fatalf("expected agent_name_hint to be entirely absent once a real client_id is used, got %s", u)
	}
}

func TestBuildAuthorizeURL_BootstrapClientID_IncludesAgentNameHint(t *testing.T) {
	u := buildAuthorizeURL(siwcBootstrapClientID, "http://127.0.0.1:0/cb", "s", "n", "c", "urn:uuid:host-1")
	if !strings.Contains(u, "agent_name_hint=HarnessMesh") {
		t.Fatalf("expected agent_name_hint on the first, bootstrap-client_id attempt, got %s", u)
	}
}
