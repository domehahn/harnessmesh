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

func TestIdentityHasScope(t *testing.T) {
	member := Identity{Subject: "member", Tenant: "t1", Roles: map[string]bool{"member": true}, Scopes: map[string]bool{"mcp:read": true}}
	if !member.HasScope("mcp:read") {
		t.Fatal("expected member to have mcp:read scope")
	}
	if member.HasScope("mcp:write") {
		t.Fatal("member must not have mcp:write scope")
	}
	if !member.HasScope("") {
		t.Fatal("empty scope should always be allowed")
	}

	admin := Identity{Subject: "admin", Tenant: "t1", Roles: map[string]bool{"admin": true}}
	if !admin.HasScope("mcp:write") {
		t.Fatal("admin must have all scopes")
	}

	owner := Identity{Subject: "owner", Tenant: "t1", Roles: map[string]bool{"owner": true}}
	if !owner.HasScope("any:scope") {
		t.Fatal("owner must have all scopes")
	}
}
