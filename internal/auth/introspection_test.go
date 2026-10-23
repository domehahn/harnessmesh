package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTokenFileRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("old-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !MatchToken("", path, "old-token") || MatchToken("", path, "new-token") {
		t.Fatal("initial token-file value was not enforced")
	}
	if err := os.WriteFile(path, []byte("new-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if MatchToken("", path, "old-token") || !MatchToken("", path, "new-token") {
		t.Fatal("rotated token-file value was not reloaded")
	}
}

func TestIntrospectToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.FormValue("token") != "good" {
			t.Fatalf("unexpected introspection request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer client-secret" {
			t.Fatalf("missing client authentication")
		}
		_, _ = w.Write([]byte(`{"active":true,"sub":"user-1","scope":"mesh:read mesh:write"}`))
	}))
	defer server.Close()

	info, ok := IntrospectToken(context.Background(), server.URL, "good", "client-secret")
	if !ok || info.Subject != "user-1" || !HasScope(info, "mesh:write") || HasScope(info, "admin") {
		t.Fatalf("unexpected token info: %+v, ok=%v", info, ok)
	}
}

func TestIntrospectTokenRejectsExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"active":true,"exp":1}`))
	}))
	defer server.Close()
	if _, ok := IntrospectToken(context.Background(), server.URL, "expired", ""); ok {
		t.Fatal("expired token must be rejected")
	}
}
