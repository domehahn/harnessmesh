package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *SQLiteStore) CreateTenant(ctx context.Context, t *TenantRecord) error {
	if t == nil || t.ID == "" || t.Slug == "" || t.Name == "" {
		return errors.New("tenant id, slug, and name are required")
	}
	if t.Status == "" {
		t.Status = "active"
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenants(id,slug,name,status,created_at) VALUES(?,?,?,?,?)`, t.ID, t.Slug, t.Name, t.Status, t.CreatedAt)
	return err
}

func (s *SQLiteStore) GetTenant(ctx context.Context, id string) (*TenantRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var t TenantRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,slug,name,status,created_at FROM tenants WHERE id=?`, id).Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *SQLiteStore) UpsertPrincipal(ctx context.Context, p *PrincipalRecord) error {
	if p == nil || p.Subject == "" {
		return errors.New("principal subject is required")
	}
	if p.Status == "" {
		p.Status = "active"
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO principals(subject,email,status,created_at) VALUES(?,?,?,?) ON CONFLICT(subject) DO UPDATE SET email=excluded.email,status=excluded.status`, p.Subject, p.Email, p.Status, p.CreatedAt)
	return err
}

func (s *SQLiteStore) SetMembership(ctx context.Context, m *MembershipRecord) error {
	if m == nil || m.TenantID == "" || m.Subject == "" || m.Role == "" {
		return errors.New("membership tenant, subject, and role are required")
	}
	if m.Status == "" {
		m.Status = "active"
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenant_memberships(tenant_id,subject,role,status,created_at) VALUES(?,?,?,?,?) ON CONFLICT(tenant_id,subject) DO UPDATE SET role=excluded.role,status=excluded.status`, m.TenantID, m.Subject, m.Role, m.Status, m.CreatedAt)
	return err
}

func (s *SQLiteStore) GetMembership(ctx context.Context, tenantID, subject string) (*MembershipRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var m MembershipRecord
	err := s.db.QueryRowContext(ctx, `SELECT tenant_id,subject,role,status,created_at FROM tenant_memberships WHERE tenant_id=? AND subject=?`, tenantID, subject).Scan(&m.TenantID, &m.Subject, &m.Role, &m.Status, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *SQLiteStore) CreateAPIKey(ctx context.Context, k *APIKeyRecord) error {
	if k == nil || k.ID == "" || k.TenantID == "" || k.Subject == "" || k.Hash == "" {
		return errors.New("API key id, tenant, subject, and hash are required")
	}
	if k.Status == "" {
		k.Status = "active"
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys(id,tenant_id,subject,prefix,hash,status,created_at,revoked_at) VALUES(?,?,?,?,?,?,?,?)`, k.ID, k.TenantID, k.Subject, k.Prefix, k.Hash, k.Status, k.CreatedAt, k.RevokedAt)
	return err
}

func (s *SQLiteStore) GetAPIKeyByHash(ctx context.Context, hash string) (*APIKeyRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var k APIKeyRecord
	var revoked sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id,tenant_id,subject,prefix,hash,status,created_at,revoked_at FROM api_keys WHERE hash=? AND status='active'`, hash).Scan(&k.ID, &k.TenantID, &k.Subject, &k.Prefix, &k.Hash, &k.Status, &k.CreatedAt, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if revoked.Valid {
		k.RevokedAt = &revoked.Time
	}
	return &k, nil
}

func (s *SQLiteStore) RevokeAPIKey(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET status='revoked',revoked_at=? WHERE id=?`, now, id)
	return err
}
