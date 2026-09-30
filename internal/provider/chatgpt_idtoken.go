package provider

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// This file validates the ID token returned by the SIWC token endpoint, per
// developers.openai.com/siwc/token-sharing-open-source/sign-in's documented
// checklist: "Verify its signature against OpenAI's published JWKS. ...
// Check the issuer, audience against the issued client ID, expiration, and
// the nonce saved for this attempt. Use its validated `sub` as the account
// identity."
//
// The exact JWKS URL is not stated on the pages this mission could fetch;
// rather than fabricate an undocumented endpoint, this uses the standard,
// OpenAI-referenced mechanism the flow is explicitly described as ("an
// OpenID Connect sign-in with PKCE"): OpenID Connect Discovery 1.0
// (https://openid.net/specs/openid-connect-discovery-1_0.html) against the
// documented issuer, to locate jwks_uri.
const (
	siwcIssuer        = "https://auth.openai.com"
	siwcDiscoveryPath = "/.well-known/openid-configuration"
)

type openIDConfiguration struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

type jwkKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwkSet struct {
	Keys []jwkKey `json:"keys"`
}

// ClaimStrings represents the OIDC/JWT "aud" claim, which per RFC 7519
// §4.1.3 and the OIDC Core 1.0 ID Token spec may be encoded as EITHER a
// single JSON string or a JSON array of strings - both are valid, and a
// real OpenAI ID token has been observed using the array form
// (`"aud": ["oaiapp_..."]`). This mirrors the shape golang-jwt/jwt/v5's
// own ClaimStrings type uses; this package does not currently depend on
// that library (or any JWT library - see this file's verification below,
// which is deliberately stdlib-only crypto), so this is a small,
// standards-compliant equivalent rather than pulling in a new dependency
// for one field.
type ClaimStrings []string

// UnmarshalJSON accepts either encoding. Any other JSON shape (a number, an
// object, a bool, an array of non-strings, ...) is a malformed claim and
// fails closed with a clear error - it never panics and never silently
// produces an empty/zero audience that could accidentally satisfy a
// later mismatched check.
func (c *ClaimStrings) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*c = ClaimStrings{single}
		return nil
	}
	var multi []string
	if err := json.Unmarshal(data, &multi); err == nil {
		*c = ClaimStrings(multi)
		return nil
	}
	return fmt.Errorf("aud claim must be a JSON string or an array of strings")
}

// Contains reports whether v is one of the encoded audience values.
func (c ClaimStrings) Contains(v string) bool {
	for _, s := range c {
		if s == v {
			return true
		}
	}
	return false
}

// idTokenClaims is the subset of ID token claims this validator checks.
// This struct is deliberately used ONLY for ID tokens: OpenAI documents a
// structurally different audience for access tokens (the fixed resource
// https://api.openai.com/v1, validated server-side, never decoded or
// audience-checked by this client) versus ID tokens (audience is the
// issued OAuth client_id, validated here) - these are different validation
// domains and must never share a claims type or a validation code path.
type idTokenClaims struct {
	Iss string       `json:"iss"`
	Aud ClaimStrings `json:"aud"`
	// Azp ("authorized party") is OPTIONAL per OIDC Core 1.0 §3.1.3.7: when
	// an ID token's aud contains more than one value, the client SHOULD
	// verify azp (if present) equals its own client_id. It is never
	// required to be present, and its absence is not itself an error.
	Azp   string `json:"azp,omitempty"`
	Sub   string `json:"sub"`
	Nonce string `json:"nonce"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
	Email string `json:"email,omitempty"`
}

// idTokenVerifier fetches and caches OpenAI's published JWKS via standard
// OIDC discovery, and verifies RS256-signed ID tokens against it.
type idTokenVerifier struct {
	discoveryURL string // overridable in tests; production always uses siwcIssuer+siwcDiscoveryPath
	httpClient   *http.Client

	mu        sync.Mutex
	cachedSet *jwkSet
	cachedAt  time.Time
}

func newIDTokenVerifier() *idTokenVerifier {
	return &idTokenVerifier{discoveryURL: siwcIssuer + siwcDiscoveryPath, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

const jwksCacheTTL = 10 * time.Minute

func (v *idTokenVerifier) jwks(ctx context.Context) (*jwkSet, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cachedSet != nil && time.Since(v.cachedAt) < jwksCacheTTL {
		return v.cachedSet, nil
	}

	discReq, err := http.NewRequestWithContext(ctx, http.MethodGet, v.discoveryURL, nil)
	if err != nil {
		return nil, err
	}
	discResp, err := v.httpClient.Do(discReq)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery unreachable: %w", err)
	}
	defer discResp.Body.Close()
	if discResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery returned HTTP %d", discResp.StatusCode)
	}
	var conf openIDConfiguration
	if err := json.NewDecoder(io.LimitReader(discResp.Body, 1<<20)).Decode(&conf); err != nil {
		return nil, fmt.Errorf("parse OIDC discovery document: %w", err)
	}
	if conf.JWKSURI == "" {
		return nil, fmt.Errorf("OIDC discovery document has no jwks_uri")
	}

	jwksReq, err := http.NewRequestWithContext(ctx, http.MethodGet, conf.JWKSURI, nil)
	if err != nil {
		return nil, err
	}
	jwksResp, err := v.httpClient.Do(jwksReq)
	if err != nil {
		return nil, fmt.Errorf("JWKS endpoint unreachable: %w", err)
	}
	defer jwksResp.Body.Close()
	if jwksResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS endpoint returned HTTP %d", jwksResp.StatusCode)
	}
	var set jwkSet
	if err := json.NewDecoder(io.LimitReader(jwksResp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("parse JWKS: %w", err)
	}
	v.cachedSet = &set
	v.cachedAt = time.Now()
	return &set, nil
}

// Verify checks signature (against the published JWKS), issuer, audience
// (the issued client_id), expiration, and nonce, per the documented
// checklist, and returns the validated subject (account identity).
func (v *idTokenVerifier) Verify(ctx context.Context, idToken, expectedAudience, expectedNonce string) (string, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("malformed ID token")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("malformed ID token header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return "", fmt.Errorf("parse ID token header: %w", err)
	}
	if header.Alg != "RS256" {
		return "", fmt.Errorf("unsupported ID token signing algorithm %q (only RS256 is supported)", header.Alg)
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("malformed ID token payload: %w", err)
	}
	var claims idTokenClaims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return "", fmt.Errorf("parse ID token claims: %w", err)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("malformed ID token signature: %w", err)
	}

	set, err := v.jwks(ctx)
	if err != nil {
		return "", fmt.Errorf("fetch JWKS: %w", err)
	}
	pub, err := findRSAPublicKey(set, header.Kid)
	if err != nil {
		return "", err
	}

	signingInput := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return "", fmt.Errorf("ID token signature verification failed: %w", err)
	}

	if claims.Iss != siwcIssuer {
		return "", fmt.Errorf("ID token issuer %q does not match expected %q", claims.Iss, siwcIssuer)
	}
	// Per RFC 7519 §4.1.3 / OIDC Core 1.0, aud may be a single string or an
	// array of strings - ClaimStrings.UnmarshalJSON already normalized
	// either encoding; the validation invariant itself is unchanged and
	// unweakened: the issued client_id MUST be one of the encoded
	// audiences.
	if !claims.Aud.Contains(expectedAudience) {
		return "", fmt.Errorf("ID token audience %v does not contain the issued client_id %q", []string(claims.Aud), expectedAudience)
	}
	// OIDC Core 1.0 §3.1.3.7: when aud contains multiple values, the
	// client SHOULD verify azp (if present) identifies it - azp is
	// optional, so its absence is not itself an error, but a PRESENT and
	// mismatched azp is a legitimate rejection (it signals the token was
	// authorized for a different party even though this client_id is also
	// a listed audience).
	if len(claims.Aud) > 1 && claims.Azp != "" && claims.Azp != expectedAudience {
		return "", fmt.Errorf("ID token authorized party (azp) %q does not match the issued client_id %q", claims.Azp, expectedAudience)
	}
	if claims.Exp != 0 && time.Now().Unix() > claims.Exp {
		return "", fmt.Errorf("ID token has expired")
	}
	if claims.Nonce != expectedNonce {
		return "", fmt.Errorf("ID token nonce does not match the value generated for this sign-in attempt (possible replay)")
	}
	if claims.Sub == "" {
		return "", fmt.Errorf("ID token has no subject claim")
	}
	return claims.Sub, nil
}

func findRSAPublicKey(set *jwkSet, kid string) (*rsa.PublicKey, error) {
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		if kid != "" && k.Kid != kid {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		n := new(big.Int).SetBytes(nBytes)
		e := new(big.Int).SetBytes(eBytes)
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	}
	return nil, fmt.Errorf("no matching RSA key found in JWKS for kid %q", kid)
}

// jwkFromRSAPublicKey builds a JWK for the given RSA public key, used by
// tests to construct a fake JWKS document. Exported at package scope (not
// _test.go) only because it shares the unexported jwkKey type with the
// verifier above; never called from a production code path.
func jwkFromRSAPublicKey(kid string, pub *rsa.PublicKey) jwkKey {
	return jwkKey{
		Kty: "RSA", Kid: kid, Use: "sig", Alg: "RS256",
		N: base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}
