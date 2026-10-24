package knowledge

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/tenant"
)

func TestArchiveTenantSearchIsolation(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "tenant.hmkz"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	acme := tenant.WithIdentity(context.Background(), tenant.Identity{Subject: "u1", Tenant: "acme"})
	other := tenant.WithIdentity(context.Background(), tenant.Identity{Subject: "u2", Tenant: "other"})
	if err := a.Append(acme, Record{ID: "acme-1", Kind: "note", Text: "shared architecture"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Append(other, Record{ID: "other-1", Kind: "note", Text: "shared architecture"}); err != nil {
		t.Fatal(err)
	}
	results, err := a.Search(acme, "shared", SearchOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].TenantID != "acme" {
		t.Fatalf("unexpected tenant results: %+v", results)
	}
	if _, err := a.Search(acme, "shared", SearchOptions{TenantID: "other", Limit: 10}); !errors.Is(err, tenant.ErrTenantMismatch) {
		t.Fatal("expected tenant mismatch")
	}
	if _, err := a.StatsForTenant(acme); err != nil {
		t.Fatal(err)
	}
}
