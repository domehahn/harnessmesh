package provider

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// This file reproduces and proves the fix for a real-account SIWC failure
// discovered after the token-exchange `resource` parameter fix (commit
// 2a50ea4): real ID tokens progressed all the way to signature/issuer/
// nonce validation, but ID-token validation itself then failed with
//   "json: cannot unmarshal array into Go struct field
//    idTokenClaims.aud of type string"
// The real OpenAI ID token encodes aud as a JSON array
// (`"aud": ["oaiapp_..."]`), which RFC 7519 §4.1.3 / OIDC Core 1.0
// explicitly permit as an alternative to a bare string - the production
// idTokenClaims.Aud field was typed `string` and could not decode it.

// signTestIDTokenRawClaims builds and RS256-signs a JWT from a literal
// claims map, giving tests full control over exactly how each claim is
// JSON-encoded (in particular, `aud` as a bare string vs. a JSON array) -
// something a Go struct's default marshaling can't produce on demand.
func signTestIDTokenRawClaims(t *testing.T, priv *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign test ID token: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// TestClaimStrings_UnmarshalJSON is a focused unit test on the aud-claim
// decoder in isolation, independent of the full token-verification path.
func TestClaimStrings_UnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		want    ClaimStrings
		wantErr bool
	}{
		{name: "single_string", json: `"oaiapp_x"`, want: ClaimStrings{"oaiapp_x"}},
		{name: "array_single", json: `["oaiapp_x"]`, want: ClaimStrings{"oaiapp_x"}},
		{name: "array_multi", json: `["a","oaiapp_x"]`, want: ClaimStrings{"a", "oaiapp_x"}},
		{name: "array_empty", json: `[]`, want: ClaimStrings{}},
		{name: "wrong_type_number", json: `12345`, wantErr: true},
		{name: "wrong_type_object", json: `{"a":1}`, wantErr: true},
		{name: "wrong_type_bool", json: `true`, wantErr: true},
		{name: "wrong_type_array_of_numbers", json: `[1,2]`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c ClaimStrings
			err := json.Unmarshal([]byte(tc.json), &c) // must never panic, regardless of input
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %s, got none (value=%v)", tc.json, c)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.json, err)
			}
			if len(c) != len(tc.want) {
				t.Fatalf("got %v want %v", c, tc.want)
			}
			for i := range c {
				if c[i] != tc.want[i] {
					t.Fatalf("got %v want %v", c, tc.want)
				}
			}
		})
	}
}

// TestIDTokenAudience_Matrix drives the full production Verify() path
// (structural parse -> signature -> issuer -> audience -> azp -> expiry ->
// nonce -> subject) end to end for every required audience scenario (A-I;
// J - invalid signature - is covered by its own dedicated test below, and
// by the pre-existing TestIDTokenVerifier_WrongSignature).
func TestIDTokenAudience_Matrix(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	srv := newFakeOIDCServer(t, priv, "kid-aud")
	defer srv.Close()
	v := &idTokenVerifier{discoveryURL: srv.URL + "/.well-known/openid-configuration", httpClient: http.DefaultClient}

	const expectedClientID = "oaiapp_expected"
	baseClaims := func() map[string]any {
		return map[string]any{
			"iss":   siwcIssuer,
			"sub":   "user-1",
			"nonce": "nonce-1",
			"exp":   time.Now().Add(time.Hour).Unix(),
		}
	}

	tests := []struct {
		name            string
		mutate          func(m map[string]any)
		nonce           string // defaults to "nonce-1" (matches baseClaims)
		wantErr         bool
		wantErrContains string
	}{
		{
			name:   "A_single_string_aud_matches",
			mutate: func(m map[string]any) { m["aud"] = expectedClientID },
		},
		{
			name:   "B_array_single_aud_matches",
			mutate: func(m map[string]any) { m["aud"] = []string{expectedClientID} },
		},
		{
			name:   "C_array_multi_aud_contains_expected",
			mutate: func(m map[string]any) { m["aud"] = []string{"other", expectedClientID} },
			// Per OIDC Core 1.0 §3.1.3.7, a multi-valued aud is acceptable
			// as long as the expected client_id is one of the values and
			// no PRESENT azp claims a different party - neither is true
			// here (no azp at all), so this must PASS.
		},
		{
			name:            "D_array_only_wrong_value",
			mutate:          func(m map[string]any) { m["aud"] = []string{"other"} },
			wantErr:         true,
			wantErrContains: "audience",
		},
		{
			name:            "E_array_empty",
			mutate:          func(m map[string]any) { m["aud"] = []string{} },
			wantErr:         true,
			wantErrContains: "audience",
		},
		{
			name:            "F_aud_missing",
			mutate:          func(m map[string]any) { delete(m, "aud") },
			wantErr:         true,
			wantErrContains: "audience",
		},
		{
			name:            "G_aud_wrong_json_type",
			mutate:          func(m map[string]any) { m["aud"] = 12345 },
			wantErr:         true,
			wantErrContains: "claims",
		},
		{
			name:            "H_correct_audience_wrong_issuer",
			mutate:          func(m map[string]any) { m["aud"] = expectedClientID; m["iss"] = "https://evil.example.com" },
			wantErr:         true,
			wantErrContains: "issuer",
		},
		{
			name:            "I_correct_audience_wrong_nonce",
			mutate:          func(m map[string]any) { m["aud"] = expectedClientID },
			nonce:           "replayed-nonce",
			wantErr:         true,
			wantErrContains: "nonce",
		},
		{
			name: "multi_aud_with_matching_azp",
			mutate: func(m map[string]any) {
				m["aud"] = []string{"other", expectedClientID}
				m["azp"] = expectedClientID
			},
		},
		{
			name: "multi_aud_with_mismatched_azp_rejected",
			mutate: func(m map[string]any) {
				m["aud"] = []string{"other", expectedClientID}
				m["azp"] = "other" // present and names a DIFFERENT party -> must reject per OIDC §3.1.3.7
			},
			wantErr:         true,
			wantErrContains: "azp",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims := baseClaims()
			tc.mutate(claims)
			tok := signTestIDTokenRawClaims(t, priv, "kid-aud", claims)
			nonce := tc.nonce
			if nonce == "" {
				nonce = "nonce-1"
			}
			sub, err := v.Verify(context.Background(), tok, expectedClientID, nonce)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got success (sub=%q)", sub)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErrContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected success, got error: %v", err)
			}
			if sub != "user-1" {
				t.Fatalf("expected subject user-1, got %q", sub)
			}
		})
	}
}

// J: correct audience but an invalid signature must still fail closed -
// audience correctness must never substitute for signature verification.
func TestIDTokenAudience_J_CorrectAudienceInvalidSignature(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	otherPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	srv := newFakeOIDCServer(t, priv, "kid-j") // JWKS advertises priv's public key
	defer srv.Close()

	claims := map[string]any{
		"iss": siwcIssuer, "aud": "oaiapp_expected", "sub": "u", "nonce": "n",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	tok := signTestIDTokenRawClaims(t, otherPriv, "kid-j", claims) // signed with a DIFFERENT key

	v := &idTokenVerifier{discoveryURL: srv.URL + "/.well-known/openid-configuration", httpClient: http.DefaultClient}
	_, err = v.Verify(context.Background(), tok, "oaiapp_expected", "n")
	if err == nil {
		t.Fatalf("expected signature verification to fail even though audience is correct")
	}
	if strings.Contains(err.Error(), "audience") {
		t.Fatalf("expected a signature failure, not an audience-mismatch error: %v", err)
	}
}

// TestIDTokenVerifier_RealWorldArrayAudienceRegression reproduces the
// exact real-account failure ("json: cannot unmarshal array into Go
// struct field idTokenClaims.aud of type string") byte-for-byte: a raw
// JSON payload with aud encoded as a single-element array, run through the
// actual production claims parser (idTokenClaims/ClaimStrings) and the
// actual production validation path (idTokenVerifier.Verify) - not a
// synthetic double of either.
func TestIDTokenVerifier_RealWorldArrayAudienceRegression(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	srv := newFakeOIDCServer(t, priv, "kid-real")
	defer srv.Close()

	const issuedClientID = "oaiapp_real_world_abc123"
	rawPayload := []byte(fmt.Sprintf(
		`{"iss":%q,"aud":[%q],"sub":"user-real","nonce":"nonce-real","exp":%d}`,
		siwcIssuer, issuedClientID, time.Now().Add(time.Hour).Unix(),
	))

	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": "kid-real"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(rawPayload)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign test ID token: %v", err)
	}
	tok := signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)

	v := &idTokenVerifier{discoveryURL: srv.URL + "/.well-known/openid-configuration", httpClient: http.DefaultClient}
	sub, err := v.Verify(context.Background(), tok, issuedClientID, "nonce-real")
	if err != nil {
		t.Fatalf("production ID-token verification must accept a real-world array-encoded aud claim, got: %v", err)
	}
	if sub != "user-real" {
		t.Fatalf("expected subject user-real, got %q", sub)
	}
}
