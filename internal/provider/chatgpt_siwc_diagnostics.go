package provider

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"time"
)

// This file implements safe, opt-in diagnostic instrumentation for the
// SIWC token-exchange boundary. It exists specifically to debug bugs like
// a required field (e.g. the documented `resource` parameter) being
// silently missing from the outgoing token-exchange POST, which otherwise
// only manifests as an opaque invalid_grant from OpenAI's token endpoint.
//
// It NEVER logs: the authorization code, the full PKCE verifier (only its
// length), the access token, the refresh token, the ID token, or cookies.
// It is entirely inert (DiagnosticsSink is nil) unless a caller explicitly
// opts in via HARNESSMESH_SIWC_DEBUG=1 or injects a sink directly (tests
// do this to make assertions without touching the environment).

// TokenExchangeDiagnostic is a fully-redacted record of one SIWC
// token-endpoint HTTP call.
type TokenExchangeDiagnostic struct {
	Time           time.Time
	Host           string
	Path           string
	Method         string
	FormFieldNames []string // field NAMES only - never values, except the safe ones below
	GrantType      string
	ClientIDSafe   string // full bootstrap constant, or a short prefix of an issued client_id
	Resource       string
	RedirectURI    string
	VerifierLength int
	// ChallengeMatches is nil when not applicable to this grant (e.g.
	// refresh_token has no PKCE verifier to check), otherwise true/false:
	// whether base64url_no_padding(SHA256(code_verifier)) on this exchange
	// equals the code_challenge this attempt's authorize request sent.
	ChallengeMatches *bool
	AttemptNumber    int
	HTTPStatus       int
	OAuthError       string
	OAuthErrorDesc   string
}

// DiagnosticsSink receives TokenExchangeDiagnostic records.
type DiagnosticsSink interface {
	RecordTokenExchange(TokenExchangeDiagnostic)
}

// stderrDiagnosticsLogger is the only production DiagnosticsSink
// implementation: one redacted line to stderr per token-endpoint call.
type stderrDiagnosticsLogger struct{}

func (stderrDiagnosticsLogger) RecordTokenExchange(d TokenExchangeDiagnostic) {
	challengeMatches := "n/a"
	if d.ChallengeMatches != nil {
		challengeMatches = fmt.Sprintf("%v", *d.ChallengeMatches)
	}
	fmt.Fprintf(os.Stderr,
		"[siwc-diag] %s %s %s%s fields=%v grant_type=%q client_id=%q resource=%q redirect_uri=%q verifier_len=%d challenge_match=%s attempt=%d status=%d oauth_error=%q oauth_error_description=%q\n",
		d.Time.Format(time.RFC3339), d.Method, d.Host, d.Path, d.FormFieldNames, d.GrantType, d.ClientIDSafe,
		d.Resource, d.RedirectURI, d.VerifierLength, challengeMatches, d.AttemptNumber, d.HTTPStatus, d.OAuthError, d.OAuthErrorDesc)
}

// diagnosticsSinkFromEnv returns a DiagnosticsSink only when explicitly
// opted into via HARNESSMESH_SIWC_DEBUG=1 - a normal `harnessmesh provider
// auth chatgpt` run emits nothing extra by default.
func diagnosticsSinkFromEnv() DiagnosticsSink {
	if os.Getenv("HARNESSMESH_SIWC_DEBUG") == "1" {
		return stderrDiagnosticsLogger{}
	}
	return nil
}

// safeClientIDIdentifier returns a value safe to include in diagnostics:
// the bootstrap constant in full (a fixed, publicly documented value, not
// a secret), or a short, non-reversible-enough prefix of an issued
// client_id. client_ids are public-client identifiers, not secrets, but
// this still avoids ever writing the complete value to diagnostic output.
func safeClientIDIdentifier(clientID string) string {
	if clientID == siwcBootstrapClientID || len(clientID) <= 12 {
		return clientID
	}
	return clientID[:12] + "…"
}

func sortedFormFieldNames(form url.Values) []string {
	names := make([]string, 0, len(form))
	for k := range form {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
