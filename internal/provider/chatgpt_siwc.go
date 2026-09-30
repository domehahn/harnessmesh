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
	siwcScopes            = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
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
	return os.WriteFile(path, data, 0600)
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
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// buildAuthorizeURL constructs the documented authorization request.
func buildAuthorizeURL(clientID, redirectURI, state, codeChallenge string) string {
	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("response_type", "code")
	v.Set("scope", siwcScopes)
	v.Set("state", state)
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
		return nil, &UnauthorizedError{}
	}
	var payload struct {
		ClientID     string `json:"client_id,omitempty"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	issuedClientID := payload.ClientID
	if issuedClientID == "" {
		issuedClientID = clientID
	}
	ts := &SIWCTokenSet{
		ClientID:     issuedClientID,
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		IDToken:      payload.IDToken,
		ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second),
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
	challenge := codeChallengeS256(verifier)

	listener, err := newLoopbackListener(listenAddr)
	if err != nil {
		return nil, fmt.Errorf("start loopback callback listener: %w", err)
	}
	redirectURI := fmt.Sprintf("http://%s/auth/callback", listener.Addr().String())
	authorizeURL := buildAuthorizeURL(clientID, redirectURI, state, challenge)

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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>Signed in with ChatGPT. You can close this tab and return to HarnessMesh.</body></html>"))

		tc := newSIWCTokenClient()
		ts, err := tc.exchangeCode(r.Context(), clientID, code, verifier, redirectURI)
		done <- LoginFlowOutcome{Tokens: ts, Err: err}
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
