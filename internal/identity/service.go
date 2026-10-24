// Package identity implements the SaaS directory and API-key lifecycle.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/store"
	"github.com/domehahn/harnessmesh/internal/tenant"
)

type Service struct{ Store store.IdentityStore }

func (s Service) IssueAPIKey(ctx context.Context, tenantID, subject string) (string, *store.APIKeyRecord, error) {
	if s.Store == nil {
		return "", nil, errors.New("identity store is required")
	}
	if tenantID == "" || subject == "" {
		return "", nil, errors.New("tenant and subject are required")
	}
	membership, err := s.Store.GetMembership(ctx, tenantID, subject)
	if err != nil {
		return "", nil, err
	}
	if membership == nil || membership.Status != "active" {
		return "", nil, errors.New("subject is not an active tenant member")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	secret := "hm_live_" + base64.RawURLEncoding.EncodeToString(raw)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
	record := &store.APIKeyRecord{ID: "key_" + base64.RawURLEncoding.EncodeToString(raw[:12]), TenantID: tenantID, Subject: subject, Prefix: secret[:16], Hash: hash, CreatedAt: time.Now().UTC()}
	if err := s.Store.CreateAPIKey(ctx, record); err != nil {
		return "", nil, err
	}
	record.Hash = ""
	return secret, record, nil
}

func (s Service) AuthenticateAPIKey(ctx context.Context, presented string) (tenant.Identity, error) {
	if s.Store == nil {
		return tenant.Identity{}, errors.New("identity store is required")
	}
	presented = strings.TrimSpace(presented)
	if presented == "" {
		return tenant.Identity{}, tenant.ErrUnauthenticated
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(presented)))
	key, err := s.Store.GetAPIKeyByHash(ctx, hash)
	if err != nil {
		return tenant.Identity{}, err
	}
	if key == nil {
		return tenant.Identity{}, tenant.ErrUnauthenticated
	}
	membership, err := s.Store.GetMembership(ctx, key.TenantID, key.Subject)
	if err != nil {
		return tenant.Identity{}, err
	}
	if membership == nil || membership.Status != "active" {
		return tenant.Identity{}, tenant.ErrForbidden
	}
	return tenant.Identity{Subject: key.Subject, Tenant: key.TenantID, Roles: map[string]bool{membership.Role: true}}, nil
}
