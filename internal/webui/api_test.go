package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/identity"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
	"github.com/domehahn/harnessmesh/internal/tenant"
)

func TestDashboardAPIIsTenantScopedAndCanSubmitJob(t *testing.T) {
	db, err := store.OpenSQLite(t.TempDir() + "/dashboard.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.CreateTenant(ctx, &store.TenantRecord{ID: "tenant-a", Slug: "a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPrincipal(ctx, &store.PrincipalRecord{Subject: "user-a"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMembership(ctx, &store.MembershipRecord{TenantID: "tenant-a", Subject: "user-a", Role: "owner"}); err != nil {
		t.Fatal(err)
	}
	key, _, err := (identity.Service{Store: db}).IssueAPIKey(ctx, "tenant-a", "user-a")
	if err != nil {
		t.Fatal(err)
	}
	identityContext := tenant.WithIdentity(ctx, tenant.Identity{Subject: "user-a", Tenant: "tenant-a", Roles: map[string]bool{"owner": true}})
	if err := db.SaveApproval(identityContext, &protocol.ApprovalRequest{ID: "approval-1", Agent: "reviewer", Reason: "test", Status: protocol.ApprovalPending, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	h := APIHandler(db)
	body := bytes.NewBufferString(`{"kind":"agent.task","payload":{"task":"test"}}`)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/jobs", body)
	r.Header.Set("X-HarnessMesh-API-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("submit status=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/summary", nil)
	r.Header.Set("X-HarnessMesh-API-Key", key)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("summary status=%d", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("dashboard API security headers missing: %v", w.Header())
	}
	var summary struct {
		Tenant string             `json:"tenant"`
		Jobs   []store.DurableJob `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Tenant != "tenant-a" || len(summary.Jobs) != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/optimization", nil)
	r.Header.Set("X-HarnessMesh-API-Key", key)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("optimization status=%d body=%s", w.Code, w.Body.String())
	}
	var optimization struct {
		Tenant  string `json:"tenant"`
		Credits struct {
			Available bool `json:"available"`
		} `json:"credits"`
		ByAgent []any `json:"by_agent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &optimization); err != nil {
		t.Fatal(err)
	}
	if optimization.Tenant != "tenant-a" || optimization.Credits.Available {
		t.Fatalf("unexpected optimization projection: %+v", optimization)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/dashboard/approvals/approval-1", bytes.NewBufferString(`{"status":"approved"}`))
	r.Header.Set("X-HarnessMesh-API-Key", key)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("approval status=%d body=%s", w.Code, w.Body.String())
	}
	approved, err := db.GetApproval(identityContext, "approval-1")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != protocol.ApprovalApproved || approved.DecidedBy != "user-a" || approved.DecidedAt == nil {
		t.Fatalf("approval was not recorded: %+v", approved)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/summary", nil)
	r.Header.Set("X-HarnessMesh-API-Key", "invalid")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid key status=%d", w.Code)
	}
}

func TestDashboardAPIAcceptsOAuthIntrospection(t *testing.T) {
	db, err := store.OpenSQLite(t.TempDir() + "/oauth-dashboard.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateTenant(context.Background(), &store.TenantRecord{ID: "tenant-keycloak", Slug: "keycloak", Name: "Keycloak"}); err != nil {
		t.Fatal(err)
	}
	introspection := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.FormValue("token") != "oidc-access-token" {
			http.Error(w, "invalid introspection request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true,"sub":"keycloak-user","tenant":"tenant-keycloak","roles":["owner"],"exp":4102444800}`))
	}))
	defer introspection.Close()
	t.Setenv("HARNESSMESH_DASHBOARD_OAUTH_INTROSPECTION_URL", introspection.URL)
	t.Setenv("HARNESSMESH_DASHBOARD_OAUTH_CLIENT_SECRET", "client-secret")
	r := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/summary", nil)
	r.Header.Set("Authorization", "Bearer oidc-access-token")
	w := httptest.NewRecorder()
	APIHandler(db).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("oauth dashboard status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestProviderQuotaProjection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer quota-secret" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"example","used_percent":42.5,"next_available_at":"2030-01-01T00:00:00Z","remaining":575,"limit":1000}`))
	}))
	defer server.Close()
	t.Setenv("HARNESSMESH_PROVIDER_QUOTA_URL", server.URL)
	t.Setenv("HARNESSMESH_PROVIDER_QUOTA_TOKEN", "quota-secret")
	quota := providerQuota(context.Background())
	if !quota.Available || quota.UsedPercent == nil || *quota.UsedPercent != 42.5 || quota.Provider != "example" {
		t.Fatalf("unexpected quota projection: %+v", quota)
	}
}
