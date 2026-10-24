package identity

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/store"
)

func TestAPIKeyLifecycle(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateTenant(ctx, &store.TenantRecord{ID: "tenant-a", Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPrincipal(ctx, &store.PrincipalRecord{Subject: "user-a"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMembership(ctx, &store.MembershipRecord{TenantID: "tenant-a", Subject: "user-a", Role: "owner"}); err != nil {
		t.Fatal(err)
	}
	service := Service{Store: st}
	secret, record, err := service.IssueAPIKey(ctx, "tenant-a", "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || record.Hash != "" {
		t.Fatal("secret must be returned once and hash must not be returned")
	}
	identity, err := service.AuthenticateAPIKey(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Tenant != "tenant-a" || !identity.HasRole("owner") {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if err := st.RevokeAPIKey(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateAPIKey(ctx, secret); err == nil {
		t.Fatal("revoked key authenticated")
	}
}
