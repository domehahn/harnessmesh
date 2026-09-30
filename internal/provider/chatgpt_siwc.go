package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This file implements OpenAI's officially documented "Sign in with
// ChatGPT" (SIWC) OAuth flow for open-source, locally-hosted apps, so that
// eligible ChatGPT Plus/Pro users can authorize HarnessMesh's provider
// gateway to make Responses API requests billed against their ChatGPT plan
// usage allowance, instead of metered OpenAI API billing.
//
// Verified against official OpenAI documentation as of 2026-09-30:
//   - developers.openai.com/siwc - "Sign in with ChatGPT" overview; three
//     integration types (website sign-in, ChatGPT plugin sign-in, "ChatGPT
//     plan usage in your open-source app"). Quote: "ChatGPT plan usage is
//     available to all open-source partners and selected private clients"
//     (paid/remotely-hosted apps require a separate interest-form process -
//     not what HarnessMesh is; a local, self-hosted, open-source gateway is
//     squarely the documented "open-source and locally hosted apps" case).
//   - developers.openai.com/siwc/token-sharing-open-source - "ChatGPT plan
//     usage is an optional capability within Sign in with ChatGPT. ... your
//     open-source app can request permission to use the user's ChatGPT plan
//     for eligible Responses API requests." Uses the Responses API with
//     store:false, stream:true.
//   - developers.openai.com/siwc/token-sharing-open-source/sign-in -
//     concrete OAuth/OIDC details: authorization endpoint
//     https://auth.openai.com/api/accounts/authorize, token endpoint
//     https://auth.openai.com/api/accounts/oauth/token, PKCE (S256)
//     required, response_type=code, a loopback redirect_uri on 127.0.0.1,
//     scopes "openid profile email offline_access resource.invoke
//     chatgpt.tokens.use.direct", resource=https://api.openai.com/v1,
//     client_id="dynamic_agent_client" for the first registration (the
//     server issues a real client_id, format "oaiapp_...", to persist and
//     reuse for subsequent logins), no client secret (public client).
//   - github.com/openai/sign-in-with-chatgpt-devkit - the official devkit;
//     its @siwc/local package description ("OAuth, credential storage,
//     account profiles, model discovery, and streaming Responses") for
//     "developers building open-source apps that run on a user's own
//     machine" is exactly HarnessMesh's deployment shape.
//
// IMPORTANT, documented caveat this file's docs (docs/codex-provider.md)
// repeat prominently: help.openai.com/en/articles/20001275-chatgpt-work-and-codex
// states that on plans where these features are bundled, "Codex, ChatGPT
// Work, ChatGPT for Excel, and Workspace Agents use a shared allowance and
// credit pool" - i.e. on Plus/Pro, using this backend draws down the SAME
// usage allowance Codex itself draws from. This backend never invokes the
// Codex CLI (internal/creditguard's BackendCodex call counter proves that,
// same as every other backend), and it never touches metered OpenAI API
// billing (a structurally distinct grant/scope from an API key) - but it
// is NOT a "free," separately-metered lane from a plan-usage-allowance
// point of view. HarnessMesh cannot independently verify OpenAI's
// server-side billing routing; this is what the cited documentation states.
const (
	siwcAuthorizeURL = "https://auth.openai.com/api/accounts/authorize"
	siwcTokenURL     = "https://auth.openai.com/api/accounts/oauth/token"
	siwcResponsesURL = "https://api.openai.com/v1/responses"
	// siwcResource is the OAuth "resource" parameter documented for
	// ChatGPT-plan-usage-scoped tokens.
	siwcResource = "https://api.openai.com/v1"
	// siwcBootstrapClientID is used only for the very first authorization;
	// the server returns a real, persistent client_id (format
	// "oaiapp_...") to store and reuse thereafter.
	siwcBootstrapClientID = "dynamic_agent_client"
	siwcPlanUsageScope    = "chatgpt.tokens.use.direct"
	siwcScopes            = "openid profile email offline_access resource.invoke " + siwcPlanUsageScope
	// siwcAgentNameHint identifies HarnessMesh to the consent screen, per
	// the documented agent_name_hint authorize parameter.
	siwcAgentNameHint = "HarnessMesh"
)

// SIWCTokenSet is the persisted OAuth credential set for one signed-in
// ChatGPT account, stored locally (analogous to Codex CLI's own
// ~/.codex/auth.json pattern - never an OpenAI API key).
type SIWCTokenSet struct {
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	// ExtAgentHostID is this host's stable, persistent identifier, sent as
	// the documented ext_agent_host_id authorize-request parameter. It is
	// generated once and reused for every sign-in from this machine - see
	// developers.openai.com/siwc/token-sharing-open-source/sign-in:
	// "Choose and persist this host's ext_agent_host_id before its first
	// sign-in, or reuse its existing value for the same host."
	ExtAgentHostID string `json:"ext_agent_host_id,omitempty"`
	// Subject is the validated ID token "sub" claim - the account identity
	// - populated only after successful ID token verification.
	Subject string `json:"subject,omitempty"`
	Email   string `json:"email,omitempty"`
	// Scopes actually granted, per the token response, so callers can
	// detect a partial-consent case (e.g. the user signed in for identity
	// but declined ChatGPT-plan-usage permission specifically).
	Scopes []string `json:"scopes,omitempty"`
}

// HasScope reports whether scope was actually granted (present in the
// token response's scopes list), not merely requested.
func (t *SIWCTokenSet) HasScope(scope string) bool {
	if t == nil {
		return false
	}
	for _, s := range t.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// HasChatGPTPlanUsageGrant reports whether the user actually approved
// ChatGPT-plan-usage permission during consent (as opposed to signing in
// for identity only and declining that specific permission).
func (t *SIWCTokenSet) HasChatGPTPlanUsageGrant() bool {
	return t.HasScope(siwcPlanUsageScope)
}

func (t *SIWCTokenSet) expired() bool {
	return t == nil || t.AccessToken == "" || time.Now().After(t.ExpiresAt.Add(-30*time.Second))
}

// DefaultSIWCTokenPath returns the default local path SIWC credentials are
// stored at, mirroring Codex CLI's ~/.codex/auth.json convention.
func DefaultSIWCTokenPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".harnessmesh", "chatgpt-siwc-auth.json")
	}
	return filepath.Join(home, ".harnessmesh", "chatgpt-siwc-auth.json")
}

// defaultSIWCHostIDPath returns the default path for this host's stable
// ext_agent_host_id, stored separately from any one account's credentials
// so it survives sign-out/sign-in-as-a-different-account and is shared
// across every SIWCTokenSet this host ever obtains.
func defaultSIWCHostIDPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".harnessmesh", "chatgpt-siwc-host-id")
	}
	return filepath.Join(home, ".harnessmesh", "chatgpt-siwc-host-id")
}

// loadOrCreateExtAgentHostID returns this host's persisted
// ext_agent_host_id, generating and persisting a new one on first use.
func loadOrCreateExtAgentHostID(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	}
	id, err := generateHostID()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := atomicWriteFile(path, []byte(id), 0600); err != nil {
		return "", err
	}
	return id, nil
}

func generateHostID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// urn:uuid:-style formatting, matching the example in OpenAI's own
	// documentation ("ext_agent_host_id": "urn:uuid:...").
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// atomicWriteFile writes data to a temp file in the same directory as path
// and renames it into place, so a crash mid-write never leaves a
// truncated/corrupt credential file - required for the token-lifecycle
// "replacement refresh token persisted atomically" property.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".siwc-tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// SaveSIWCTokenSetTo persists ts to path (0700 dir, 0600 file), for use by
// the CLI's `provider auth chatgpt` login flow.
func SaveSIWCTokenSetTo(path string, ts *SIWCTokenSet) error {
	return saveSIWCTokenSet(path, ts)
}

func loadSIWCTokenSet(path string) (*SIWCTokenSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ts SIWCTokenSet
	if err := json.Unmarshal(data, &ts); err != nil {
		return nil, fmt.Errorf("parse SIWC token file %s: %w", path, err)
	}
	return &ts, nil
}

func saveSIWCTokenSet(path string, ts *SIWCTokenSet) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0600)
}

// newSIWCResponsesRequest builds the HTTP request for a Responses API call
// authenticated with a ChatGPT-plan-scoped OAuth access token (never an API
// key), per developers.openai.com/siwc/token-sharing-open-source's
// documented request shape.
func newSIWCResponsesRequest(ctx context.Context, responsesURL, accessToken string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responsesURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return req, nil
}

func newLoopbackListener(listenAddr string) (net.Listener, error) {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	return net.Listen("tcp", listenAddr)
}

// --- PKCE ---

func generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func codeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func generateState() (string, error) {
	return generateRandomURLSafeToken(16)
}

// generateNonce produces a fresh random OIDC nonce, per the documented
// requirement to "generate a fresh random state, OIDC nonce, and PKCE
// verifier for each attempt" and later verify it against the ID token's
// nonce claim (see chatgpt_idtoken.go).
func generateNonce() (string, error) {
	return generateRandomURLSafeToken(16)
}

func generateRandomURLSafeToken(numBytes int) (string, error) {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// buildAuthorizeURL constructs the documented authorization request,
// including the host-identity and replay-protection parameters
// (ext_agent_host_id, agent_name_hint, nonce) alongside PKCE/state/scope/
// resource - the full documented parameter set:
// client_id, agent_name_hint, ext_agent_host_id, response_type,
// redirect_uri, scope, resource, state, nonce, code_challenge_method,
// code_challenge.
func buildAuthorizeURL(clientID, redirectURI, state, nonce, codeChallenge, extAgentHostID string) string {
	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("agent_name_hint", siwcAgentNameHint)
	v.Set("ext_agent_host_id", extAgentHostID)
	v.Set("redirect_uri", redirectURI)
	v.Set("response_type", "code")
	v.Set("scope", siwcScopes)
	v.Set("state", state)
	v.Set("nonce", nonce)
	v.Set("code_challenge", codeChallenge)
	v.Set("code_challenge_method", "S256")
	v.Set("resource", siwcResource)
	return siwcAuthorizeURL + "?" + v.Encode()
}

// siwcTokenClient makes the token-endpoint HTTP calls. Its base URLs are
// overridable only in tests (against a local fake server), never in
// production configuration - the production endpoints are the documented
// OpenAI ones above, not operator-configurable, since this is a fixed
// OAuth authorization server, not a pluggable one.
type siwcTokenClient struct {
	tokenURL     string
	responsesURL string
	httpClient   *http.Client
}

func newSIWCTokenClient() *siwcTokenClient {
	return &siwcTokenClient{tokenURL: siwcTokenURL, responsesURL: siwcResponsesURL, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (c *siwcTokenClient) exchangeCode(ctx context.Context, clientID, code, verifier, redirectURI string) (*SIWCTokenSet, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	return c.doTokenRequest(ctx, form, clientID)
}

func (c *siwcTokenClient) refresh(ctx context.Context, clientID, refreshToken string) (*SIWCTokenSet, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)
	return c.doTokenRequest(ctx, form, clientID)
}

func (c *siwcTokenClient) doTokenRequest(ctx context.Context, form url.Values, clientID string) (*SIWCTokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &BackendUnavailableError{Backend: "chatgpt-subscription", Reason: fmt.Sprintf("token endpoint unreachable: %v", err)}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, mapTokenEndpointError(resp.StatusCode, body)
	}
	var payload struct {
		ClientID     string   `json:"client_id,omitempty"`
		AccessToken  string   `json:"access_token"`
		RefreshToken string   `json:"refresh_token"`
		IDToken      string   `json:"id_token"`
		ExpiresIn    int      `json:"expires_in"`
		Scope        string   `json:"scope,omitempty"`  // standard OAuth: space-delimited
		Scopes       []string `json:"scopes,omitempty"` // documented persisted-credential shape: array
		Email        string   `json:"email,omitempty"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	issuedClientID := payload.ClientID
	if issuedClientID == "" {
		issuedClientID = clientID
	}
	scopes := payload.Scopes
	if len(scopes) == 0 && payload.Scope != "" {
		scopes = strings.Fields(payload.Scope)
	}
	ts := &SIWCTokenSet{
		ClientID:     issuedClientID,
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		IDToken:      payload.IDToken,
		ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
		Scopes:       scopes,
		Email:        payload.Email,
	}
	if ts.AccessToken == "" {
		return nil, fmt.Errorf("token response contained no access_token")
	}
	return ts, nil
}

// LoginFlowResult carries what an interactive SIWC login needs to hand the
// caller: the URL to open in a browser, and a channel that resolves once
// the loopback callback has been received and the code exchanged.
type LoginFlowResult struct {
	AuthorizeURL string
	Done         <-chan LoginFlowOutcome
}

type LoginFlowOutcome struct {
	Tokens *SIWCTokenSet
	Err    error
}

// StartLoginFlow starts a loopback HTTP listener on 127.0.0.1 (an
// ephemeral port if listenAddr's port is empty/":0"), builds the
// documented authorize URL with a fresh PKCE verifier/challenge and state,
// and returns immediately with that URL plus a channel that resolves once
// the browser redirects back with an authorization code (or the context is
// cancelled). This matches the documented flow: "Your application starts
// an OpenID Connect sign-in with PKCE. The person authenticates and
// consents with OpenAI. OpenAI returns an authorization code to your
// registered callback." The caller is responsible for opening AuthorizeURL
// in a browser (or printing it for the user to open manually) - this
// function never does browser automation itself.
func StartLoginFlow(ctx context.Context, listenAddr, clientID string) (*LoginFlowResult, error) {
	return startLoginFlowWithVerifier(ctx, listenAddr, clientID, newSIWCTokenClient(), newIDTokenVerifier(), defaultSIWCHostIDPath())
}

// startLoginFlowWithVerifier is StartLoginFlow with every network
// dependency (token endpoint, ID-token/JWKS verifier, host-ID storage
// path) injected, so tests can exercise the full flow - including ID
// token validation - against fake local servers instead of OpenAI's real
// infrastructure.
func startLoginFlowWithVerifier(ctx context.Context, listenAddr, clientID string, tc *siwcTokenClient, idv *idTokenVerifier, hostIDPath string) (*LoginFlowResult, error) {
	if clientID == "" {
		clientID = siwcBootstrapClientID
	}
	verifier, err := generateCodeVerifier()
	if err != nil {
		return nil, err
	}
	state, err := generateState()
	if err != nil {
		return nil, err
	}
	nonce, err := generateNonce()
	if err != nil {
		return nil, err
	}
	hostID, err := loadOrCreateExtAgentHostID(hostIDPath)
	if err != nil {
		return nil, fmt.Errorf("load/create ext_agent_host_id: %w", err)
	}
	challenge := codeChallengeS256(verifier)

	listener, err := newLoopbackListener(listenAddr)
	if err != nil {
		return nil, fmt.Errorf("start loopback callback listener: %w", err)
	}
	redirectURI := fmt.Sprintf("http://%s/auth/callback", listener.Addr().String())
	authorizeURL := buildAuthorizeURL(clientID, redirectURI, state, nonce, challenge, hostID)

	done := make(chan LoginFlowOutcome, 1)
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux}
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			done <- LoginFlowOutcome{Err: fmt.Errorf("OAuth state mismatch (possible CSRF)")}
			return
		}
		if errStr := q.Get("error"); errStr != "" {
			http.Error(w, "authorization denied", http.StatusOK)
			done <- LoginFlowOutcome{Err: fmt.Errorf("authorization denied: %s", errStr)}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			done <- LoginFlowOutcome{Err: fmt.Errorf("callback missing authorization code")}
			return
		}
		// New registration echoes the issued client_id in the callback
		// itself; use it for the token exchange and as the ID token's
		// expected audience if present, otherwise fall back to whatever
		// client_id this attempt was started with (a returning-user login).
		exchangeClientID := clientID
		if cb := q.Get("client_id"); cb != "" {
			exchangeClientID = cb
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>Signed in with ChatGPT. You can close this tab and return to HarnessMesh.</body></html>"))

		ts, err := tc.exchangeCode(r.Context(), exchangeClientID, code, verifier, redirectURI)
		if err != nil {
			done <- LoginFlowOutcome{Err: err}
			return
		}
		ts.ExtAgentHostID = hostID
		if ts.IDToken != "" {
			sub, verr := idv.Verify(r.Context(), ts.IDToken, ts.ClientID, nonce)
			if verr != nil {
				done <- LoginFlowOutcome{Err: fmt.Errorf("ID token validation failed: %w", verr)}
				return
			}
			ts.Subject = sub
		}
		if !ts.HasChatGPTPlanUsageGrant() {
			done <- LoginFlowOutcome{Err: fmt.Errorf("signed in, but ChatGPT plan usage permission was declined or not granted (granted scopes: %v) - re-run and approve it on the consent screen", ts.Scopes)}
			return
		}
		done <- LoginFlowOutcome{Tokens: ts}
	})

	go func() { _ = server.Serve(listener) }()
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()

	wrapped := make(chan LoginFlowOutcome, 1)
	go func() {
		select {
		case outcome := <-done:
			wrapped <- outcome
		case <-ctx.Done():
			wrapped <- LoginFlowOutcome{Err: ctx.Err()}
		}
		_ = server.Close()
	}()

	return &LoginFlowResult{AuthorizeURL: authorizeURL, Done: wrapped}, nil
}
