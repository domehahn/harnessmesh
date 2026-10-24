package tenant

import (
	"context"
	"errors"
	"testing"
)

func TestTenantContextFailsClosed(t *testing.T) {
	ctx := WithIdentity(context.Background(), Identity{Subject: "user-1", Tenant: "acme", Roles: map[string]bool{"operator": true}, Scopes: map[string]bool{"jobs:write": true}})
	if err := Authorize(ctx, "acme", "operator", "jobs:write"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(Authorize(ctx, "other", "operator", "jobs:write"), ErrTenantMismatch) {
		t.Fatal("expected tenant mismatch")
	}
	if !errors.Is(Authorize(ctx, "acme", "admin", "jobs:write"), ErrForbidden) {
		t.Fatal("expected role denial")
	}
	if _, err := FromContext(context.Background()); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("expected unauthenticated context")
	}
}
