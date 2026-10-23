// Package tenant contains the identity and authorization primitives shared by
// SaaS-facing transports. Tenant identity is derived from authenticated
// context, never from an arbitrary request field.
package tenant

import (
	"context"
	"errors"
	"strings"
)

var ErrUnauthenticated = errors.New("tenant identity is missing")
var ErrTenantMismatch = errors.New("tenant boundary violation")
var ErrForbidden = errors.New("operation is forbidden")

type Identity struct {
	Subject string
	Tenant  string
	Roles   map[string]bool
	Scopes  map[string]bool
}

func (i Identity) Valid() bool {
	return strings.TrimSpace(i.Subject) != "" && strings.TrimSpace(i.Tenant) != ""
}

func (i Identity) HasRole(role string) bool { return i.Roles[strings.TrimSpace(role)] }
func (i Identity) HasScope(scope string) bool {
	return scope == "" || i.Scopes[strings.TrimSpace(scope)]
}

type contextKey struct{}

func WithIdentity(ctx context.Context, identity Identity) context.Context {
	identity.Tenant = strings.TrimSpace(identity.Tenant)
	identity.Subject = strings.TrimSpace(identity.Subject)
	return context.WithValue(ctx, contextKey{}, identity)
}

func FromContext(ctx context.Context) (Identity, error) {
	identity, ok := ctx.Value(contextKey{}).(Identity)
	if !ok || !identity.Valid() {
		return Identity{}, ErrUnauthenticated
	}
	return identity, nil
}

func RequireTenant(ctx context.Context, requested string) (Identity, error) {
	identity, err := FromContext(ctx)
	if err != nil {
		return Identity{}, err
	}
	requested = strings.TrimSpace(requested)
	if requested != "" && requested != identity.Tenant {
		return Identity{}, ErrTenantMismatch
	}
	return identity, nil
}

func Authorize(ctx context.Context, requestedTenant, role, scope string) error {
	identity, err := RequireTenant(ctx, requestedTenant)
	if err != nil {
		return err
	}
	if role != "" && !identity.HasRole(role) {
		return ErrForbidden
	}
	if scope != "" && !identity.HasScope(scope) {
		return ErrForbidden
	}
	return nil
}
