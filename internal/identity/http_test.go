package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/domehahn/harnessmesh/internal/store"
)

func TestIdentityAPIRequiresTenantOwner(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	_ = st.CreateTenant(ctx, &store.TenantRecord{ID: "tenant-a", Slug: "acme", Name: "Acme"})
	_ = st.UpsertPrincipal(ctx, &store.PrincipalRecord{Subject: "owner"})
	_ = st.SetMembership(ctx, &store.MembershipRecord{TenantID: "tenant-a", Subject: "owner", Role: "owner"})
	key, _, err := (Service{Store: st}).IssueAPIKey(ctx, "tenant-a", "owner")
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(st)
	req := httptest.NewRequest(http.MethodGet, "/v1/identity/me", nil)
	req.Header.Set("X-HarnessMesh-API-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: %d", rec.Code)
	}
	body := strings.NewReader(`{"subject":"member","role":"member"}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/identity/tenants/tenant-a", body)
	req.Header.Set("X-HarnessMesh-API-Key", key)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("member: %d %s", rec.Code, rec.Body.String())
	}
	var membership store.MembershipRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &membership); err != nil {
		t.Fatal(err)
	}
	if membership.Subject != "member" {
		t.Fatalf("unexpected membership: %+v", membership)
	}
	bad := httptest.NewRequest(http.MethodGet, "/v1/identity/me", nil)
	badRec := httptest.NewRecorder()
	h.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", badRec.Code)
	}
}
