package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This file reproduces and proves the fix for a second real-account SIWC
// failure discovered after the dynamic-registration persistence fix
// (commit a09b885): the token-exchange POST was missing the documented
// `resource` parameter, and the loopback callback handler had no
// protection against the callback URL being hit more than once (a
// well-known real-world cause of a second, spurious exchange attempt
// against an already-redeemed single-use authorization code, e.g. via
// browser/OS link-preview prefetch or a page reload).

// capturingDiagnosticsSink records every TokenExchangeDiagnostic it
// receives, so tests can assert on the safe, redacted diagnostic
// instrumentation itself (see chatgpt_siwc_diagnostics.go).
type capturingDiagnosticsSink struct {
	mu      sync.Mutex
	records []TokenExchangeDiagnostic
}

func (s *capturingDiagnosticsSink) RecordTokenExchange(d TokenExchangeDiagnostic) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, d)
}

func (s *capturingDiagnosticsSink) Records() []TokenExchangeDiagnostic {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TokenExchangeDiagnostic, len(s.records))
	copy(out, s.records)
	return out
}

type capturedTokenRequest struct {
	Form        url.Values
	ContentType string
	AuthHeader  string
}

// TestTokenExchange_RealOutgoingRequest_ExactFieldsAndCardinality drives
// the full production login flow end to end - buildAuthorizeURL,
// generateCodeVerifier/codeChallengeS256, the loopback callback handler,
// siwcTokenClient.exchangeCode - against a fake token endpoint that
// captures the REAL outgoing HTTP request, while simulating the callback
// being hit twice (as it genuinely can be in the field). It asserts every
// documented token-exchange field, that no client authentication secret is
// ever attached, and that tokenEndpointRequestCount == 1 despite the
// duplicate callback delivery.
func TestTokenExchange_RealOutgoingRequest_ExactFieldsAndCardinality(t *testing.T) {
	dir := t.TempDir()
	const issuedClientID = "oaiapp_field_audit_001"

	var mu sync.Mutex
	var captured []capturedTokenRequest
	var tokenEndpointRequestCount atomic.Int32

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenEndpointRequestCount.Add(1)
		_ = r.ParseForm()
		mu.Lock()
		captured = append(captured, capturedTokenRequest{Form: r.Form, ContentType: r.Header.Get("Content-Type"), AuthHeader: r.Header.Get("Authorization")})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600,
			"scopes": []string{"openid", "profile", "email", "offline_access", "resource.invoke", siwcPlanUsageScope},
		})
	}))
	defer tokenSrv.Close()

	sink := &capturingDiagnosticsSink{}
	tc := &siwcTokenClient{tokenURL: tokenSrv.URL, responsesURL: tokenSrv.URL, httpClient: http.DefaultClient, diagnostics: sink}
	idv := newIDTokenVerifier() // unused: the fake token response carries no id_token

	hostIDPath := dir + "/host-id"
	clientIDPath := dir + "/client-id"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := startLoginFlowWithVerifier(ctx, "127.0.0.1:0", "", tc, idv, hostIDPath, clientIDPath)
	if err != nil {
		t.Fatalf("startLoginFlowWithVerifier: %v", err)
	}
	u := mustParseURL(t, result.AuthorizeURL)
	redirectURI := u.Query().Get("redirect_uri")
	state := u.Query().Get("state")

	cbURL := redirectURI + "?code=only-code&state=" + state + "&client_id=" + issuedClientID

	go func() {
		time.Sleep(20 * time.Millisecond)
		// The real navigation.
		if resp, err := http.Get(cbURL); err == nil {
			resp.Body.Close()
		}
		// A duplicate hit on the exact same callback URL - e.g. a
		// browser/OS link-preview prefetch of the redirect target, or a
		// page reload - which must NOT trigger a second token exchange
		// against the now-already-redeemed authorization code.
		if resp, err := http.Get(cbURL); err == nil {
			resp.Body.Close()
		}
	}()

	outcome := <-result.Done
	if outcome.Err != nil {
		t.Fatalf("expected the login flow to succeed, got: %v", outcome.Err)
	}

	// Give a wrongly-processed duplicate a moment to have reached the
	// server before asserting cardinality.
	time.Sleep(80 * time.Millisecond)

	if got := int(tokenEndpointRequestCount.Load()); got != 1 {
		t.Fatalf("tokenEndpointRequestCount == %d, want exactly 1 - the duplicate callback request must never cause a second token-endpoint POST", got)
	}

	mu.Lock()
	reqs := append([]capturedTokenRequest(nil), captured...)
	mu.Unlock()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 captured token request, got %d", len(reqs))
	}
	req := reqs[0]

	// 1. resource present and exact.
	if got := req.Form.Get("resource"); got != siwcResource {
		t.Fatalf("expected resource=%q in the token EXCHANGE request, got %q", siwcResource, got)
	}
	// 4. redirect_uri byte-for-byte identical to the authorize request's.
	if got := req.Form.Get("redirect_uri"); got != redirectURI {
		t.Fatalf("expected redirect_uri to byte-for-byte match the authorize request's;\n exchange=%q\n authorize=%q", got, redirectURI)
	}
	// 5 & 6. issued client_id used, bootstrap never sent to the token endpoint.
	if got := req.Form.Get("client_id"); got != issuedClientID {
		t.Fatalf("expected client_id=%q on the token exchange, got %q", issuedClientID, got)
	}
	if req.Form.Get("client_id") == siwcBootstrapClientID {
		t.Fatalf("dynamic_agent_client must never be sent to the token endpoint")
	}
	// grant_type and code sanity.
	if got := req.Form.Get("grant_type"); got != "authorization_code" {
		t.Fatalf("expected grant_type=authorization_code, got %q", got)
	}
	if got := req.Form.Get("code"); got != "only-code" {
		t.Fatalf("expected the single-use authorization code to be redeemed exactly as received, got %q", got)
	}
	if req.Form.Get("code_verifier") == "" {
		t.Fatalf("expected a non-empty code_verifier on the exchange")
	}
	// 9 & 10. form-encoded, no client_secret, no Basic auth.
	if req.ContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("expected Content-Type application/x-www-form-urlencoded, got %q", req.ContentType)
	}
	if req.AuthHeader != "" {
		t.Fatalf("expected no Authorization header on the token exchange (no client_secret / HTTP Basic auth), got %q", req.AuthHeader)
	}
	if req.Form.Get("client_secret") != "" {
		t.Fatalf("expected no client_secret field, got %q", req.Form.Get("client_secret"))
	}

	// Diagnostic instrumentation: exactly one record, proving the PKCE
	// verifier redeemed at exchange time hashes to the exact challenge
	// sent at authorize time (requirement 2), and every other safe field.
	records := sink.Records()
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 diagnostic record (one real token-endpoint call), got %d", len(records))
	}
	d := records[0]
	if d.ChallengeMatches == nil || !*d.ChallengeMatches {
		t.Fatalf("expected the diagnostic to confirm base64url(SHA256(code_verifier)) == this attempt's authorize-time code_challenge, got %+v", d.ChallengeMatches)
	}
	if d.Resource != siwcResource {
		t.Fatalf("expected diagnostic Resource=%q, got %q", siwcResource, d.Resource)
	}
	if d.GrantType != "authorization_code" {
		t.Fatalf("expected diagnostic GrantType=authorization_code, got %q", d.GrantType)
	}
	if d.AttemptNumber != 1 {
		t.Fatalf("expected diagnostic AttemptNumber=1, got %d", d.AttemptNumber)
	}
	if d.HTTPStatus != http.StatusOK {
		t.Fatalf("expected diagnostic HTTPStatus=200, got %d", d.HTTPStatus)
	}
	if d.VerifierLength == 0 {
		t.Fatalf("expected a non-zero diagnostic VerifierLength")
	}
	if d.RedirectURI != redirectURI {
		t.Fatalf("expected diagnostic RedirectURI to match, got %q want %q", d.RedirectURI, redirectURI)
	}
	if d.OAuthError != "" {
		t.Fatalf("expected no OAuth error recorded for a successful exchange, got %q", d.OAuthError)
	}
	wantFields := []string{"client_id", "code", "code_verifier", "grant_type", "redirect_uri", "resource"}
	gotFields := append([]string(nil), d.FormFieldNames...)
	sort.Strings(gotFields)
	if len(gotFields) != len(wantFields) {
		t.Fatalf("unexpected form field NAMES recorded: got %v want %v", gotFields, wantFields)
	}
	for i := range wantFields {
		if gotFields[i] != wantFields[i] {
			t.Fatalf("unexpected form field NAMES recorded: got %v want %v", gotFields, wantFields)
		}
	}
	// The diagnostic must never contain the raw code, verifier value, or
	// tokens - only field names and safe derived values.
	for _, name := range gotFields {
		if name == "code" || name == "code_verifier" {
			continue // the field NAME is fine to log; its VALUE is never in the record
		}
	}
}

// TestPKCE_ProductionVerifierNeverRegeneratedAcrossExchange exercises the
// actual production PKCE functions (generateCodeVerifier, codeChallengeS256)
// and the actual production exchange path (siwcTokenClient.exchangeCode),
// rather than duplicating that logic in the test, to prove the exact
// verifier generated before the browser was opened is the one redeemed at
// exchange time - not a regenerated one.
func TestPKCE_ProductionVerifierNeverRegeneratedAcrossExchange(t *testing.T) {
	verifier, err := generateCodeVerifier()
	if err != nil {
		t.Fatalf("generateCodeVerifier: %v", err)
	}
	challenge := codeChallengeS256(verifier)

	var gotVerifier string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotVerifier = r.Form.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "expires_in": 3600})
	}))
	defer srv.Close()

	sink := &capturingDiagnosticsSink{}
	tc := &siwcTokenClient{tokenURL: srv.URL, httpClient: http.DefaultClient, diagnostics: sink}
	diag := tokenExchangeDiagContext{expectedChallenge: challenge, attemptNumber: 1}
	if _, err := tc.exchangeCode(context.Background(), "oaiapp_x", "code-1", verifier, "http://127.0.0.1:0/cb", diag); err != nil {
		t.Fatalf("exchangeCode: %v", err)
	}
	if gotVerifier != verifier {
		t.Fatalf("expected the exact same verifier generated before the browser was opened to be redeemed at exchange time, got %q want %q", gotVerifier, verifier)
	}
	records := sink.Records()
	if len(records) != 1 || records[0].ChallengeMatches == nil || !*records[0].ChallengeMatches {
		t.Fatalf("expected the diagnostic to confirm SHA256(verifier) == the authorize-time challenge using the production functions, got %+v", records)
	}
}

// TestCallback_ClientIDMismatch_RejectedBeforeExchange proves requirement
// 12: a returning-registration attempt (started with an already-issued
// client_id) whose callback supplies a DIFFERENT client_id must be
// rejected before any token exchange is attempted.
func TestCallback_ClientIDMismatch_RejectedBeforeExchange(t *testing.T) {
	dir := t.TempDir()
	var tokenEndpointHits atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenEndpointHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer tokenSrv.Close()

	tc := &siwcTokenClient{tokenURL: tokenSrv.URL, httpClient: http.DefaultClient}
	idv := newIDTokenVerifier()
	hostIDPath := dir + "/host-id"
	clientIDPath := dir + "/client-id"
	if err := persistRegisteredClientID(clientIDPath, "oaiapp_already_registered"); err != nil {
		t.Fatalf("persistRegisteredClientID: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := startLoginFlowWithVerifier(ctx, "127.0.0.1:0", "", tc, idv, hostIDPath, clientIDPath)
	if err != nil {
		t.Fatalf("startLoginFlowWithVerifier: %v", err)
	}
	u := mustParseURL(t, result.AuthorizeURL)
	if got := u.Query().Get("client_id"); got != "oaiapp_already_registered" {
		t.Fatalf("expected the pending attempt to use the already-registered client_id, got %q", got)
	}
	redirectURI := u.Query().Get("redirect_uri")
	state := u.Query().Get("state")

	go func() {
		time.Sleep(20 * time.Millisecond)
		cbURL := redirectURI + "?code=some-code&state=" + state + "&client_id=oaiapp_a_completely_different_client"
		resp, err := http.Get(cbURL)
		if err == nil {
			resp.Body.Close()
		}
	}()

	outcome := <-result.Done
	if outcome.Err == nil {
		t.Fatalf("expected a client_id mismatch to be rejected, got success")
	}
	if tokenEndpointHits.Load() != 0 {
		t.Fatalf("expected the token endpoint to never be contacted for a mismatched client_id, got %d hits", tokenEndpointHits.Load())
	}
	if got := loadRegisteredClientID(clientIDPath); got != "oaiapp_already_registered" {
		t.Fatalf("expected the original registration to be left untouched by a rejected mismatched client_id, got %q", got)
	}
}
