package provider

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// signTestIDToken builds and RS256-signs a minimal JWT for tests, without
// any third-party JWT library, mirroring exactly what the production
// verifier (chatgpt_idtoken.go) expects.
func signTestIDToken(t *testing.T, priv *rsa.PrivateKey, kid string, claims idTokenClaims) string {
	t.Helper()
	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign test ID token: %v", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newFakeOIDCServer(t *testing.T, priv *rsa.PrivateKey, kid string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   siwcIssuer, // production code checks claims.Iss against the fixed siwcIssuer constant
			"jwks_uri": srv.URL + "/jwks.json",
		})
	})
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jwkKey{jwkFromRSAPublicKey(kid, &priv.PublicKey)}})
	})
	srv = httptest.NewServer(mux)
	return srv
}

func TestIDTokenVerifier_ValidToken(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	srv := newFakeOIDCServer(t, priv, "kid-1")
	defer srv.Close()

	claims := idTokenClaims{Iss: siwcIssuer, Aud: "oaiapp_test123", Sub: "user-abc", Nonce: "nonce-xyz", Exp: time.Now().Add(time.Hour).Unix(), Iat: time.Now().Unix()}
	tok := signTestIDToken(t, priv, "kid-1", claims)

	v := &idTokenVerifier{discoveryURL: srv.URL + "/.well-known/openid-configuration", httpClient: http.DefaultClient}
	sub, err := v.Verify(context.Background(), tok, "oaiapp_test123", "nonce-xyz")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if sub != "user-abc" {
		t.Fatalf("expected subject user-abc, got %q", sub)
	}
}

func TestIDTokenVerifier_WrongSignature(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	otherPriv, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := newFakeOIDCServer(t, priv, "kid-1") // JWKS advertises priv's public key
	defer srv.Close()

	claims := idTokenClaims{Iss: siwcIssuer, Aud: "oaiapp_x", Sub: "u", Nonce: "n", Exp: time.Now().Add(time.Hour).Unix()}
	tok := signTestIDToken(t, otherPriv, "kid-1", claims) // signed with a DIFFERENT key

	v := &idTokenVerifier{discoveryURL: srv.URL + "/.well-known/openid-configuration", httpClient: http.DefaultClient}
	if _, err := v.Verify(context.Background(), tok, "oaiapp_x", "n"); err == nil {
		t.Fatalf("expected signature verification to fail for a token signed with an unrelated key")
	}
}

func TestIDTokenVerifier_WrongAudience(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := newFakeOIDCServer(t, priv, "kid-1")
	defer srv.Close()
	claims := idTokenClaims{Iss: siwcIssuer, Aud: "oaiapp_actual", Sub: "u", Nonce: "n", Exp: time.Now().Add(time.Hour).Unix()}
	tok := signTestIDToken(t, priv, "kid-1", claims)

	v := &idTokenVerifier{discoveryURL: srv.URL + "/.well-known/openid-configuration", httpClient: http.DefaultClient}
	if _, err := v.Verify(context.Background(), tok, "oaiapp_different", "n"); err == nil {
		t.Fatalf("expected an audience mismatch to be rejected")
	}
}

func TestIDTokenVerifier_WrongNonce(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := newFakeOIDCServer(t, priv, "kid-1")
	defer srv.Close()
	claims := idTokenClaims{Iss: siwcIssuer, Aud: "oaiapp_x", Sub: "u", Nonce: "actual-nonce", Exp: time.Now().Add(time.Hour).Unix()}
	tok := signTestIDToken(t, priv, "kid-1", claims)

	v := &idTokenVerifier{discoveryURL: srv.URL + "/.well-known/openid-configuration", httpClient: http.DefaultClient}
	if _, err := v.Verify(context.Background(), tok, "oaiapp_x", "replayed-nonce"); err == nil {
		t.Fatalf("expected a nonce mismatch (possible replay) to be rejected")
	}
}

func TestIDTokenVerifier_ExpiredToken(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := newFakeOIDCServer(t, priv, "kid-1")
	defer srv.Close()
	claims := idTokenClaims{Iss: siwcIssuer, Aud: "oaiapp_x", Sub: "u", Nonce: "n", Exp: time.Now().Add(-time.Hour).Unix()}
	tok := signTestIDToken(t, priv, "kid-1", claims)

	v := &idTokenVerifier{discoveryURL: srv.URL + "/.well-known/openid-configuration", httpClient: http.DefaultClient}
	if _, err := v.Verify(context.Background(), tok, "oaiapp_x", "n"); err == nil {
		t.Fatalf("expected an expired ID token to be rejected")
	}
}

func TestIDTokenVerifier_WrongIssuer(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := newFakeOIDCServer(t, priv, "kid-1")
	defer srv.Close()
	claims := idTokenClaims{Iss: "https://evil.example.com", Aud: "oaiapp_x", Sub: "u", Nonce: "n", Exp: time.Now().Add(time.Hour).Unix()}
	tok := signTestIDToken(t, priv, "kid-1", claims)

	v := &idTokenVerifier{discoveryURL: srv.URL + "/.well-known/openid-configuration", httpClient: http.DefaultClient}
	if _, err := v.Verify(context.Background(), tok, "oaiapp_x", "n"); err == nil {
		t.Fatalf("expected a wrong issuer to be rejected")
	}
}

func TestIDTokenVerifier_JWKSCached(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	var jwksHits int
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": siwcIssuer, "jwks_uri": srv.URL + "/jwks.json"})
	})
	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		jwksHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwkSet{Keys: []jwkKey{jwkFromRSAPublicKey("kid-1", &priv.PublicKey)}})
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	v := &idTokenVerifier{discoveryURL: srv.URL + "/.well-known/openid-configuration", httpClient: http.DefaultClient}
	claims := idTokenClaims{Iss: siwcIssuer, Aud: "oaiapp_x", Sub: "u", Nonce: "n", Exp: time.Now().Add(time.Hour).Unix()}
	tok := signTestIDToken(t, priv, "kid-1", claims)

	for i := 0; i < 3; i++ {
		if _, err := v.Verify(context.Background(), tok, "oaiapp_x", "n"); err != nil {
			t.Fatalf("Verify #%d: %v", i, err)
		}
	}
	if jwksHits != 1 {
		t.Fatalf("expected the JWKS to be fetched once and cached, got %d fetches", jwksHits)
	}
}
