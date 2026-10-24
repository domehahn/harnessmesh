package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PostgresSaaSStore shares one PostgreSQL connection pool between the durable
// job runtime and the SaaS identity directory. It deliberately implements the
// narrow interfaces consumed by internal/jobs and internal/identity.
type PostgresSaaSStore struct{ db *sql.DB }

func NewPostgresSaaSStore(db *sql.DB) (*PostgresSaaSStore, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	return &PostgresSaaSStore{db: db}, nil
}

func (s *PostgresSaaSStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS durable_jobs (id TEXT PRIMARY KEY, tenant TEXT NOT NULL, kind TEXT NOT NULL, payload_json JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL DEFAULT 3, next_run_at TIMESTAMPTZ NOT NULL, lease_owner TEXT NOT NULL DEFAULT '', lease_until TIMESTAMPTZ NULL, last_error TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
CREATE INDEX IF NOT EXISTS idx_durable_jobs_claim ON durable_jobs(status, next_run_at, lease_until);
CREATE INDEX IF NOT EXISTS idx_durable_jobs_tenant ON durable_jobs(tenant, status);
CREATE TABLE IF NOT EXISTS job_events (id TEXT PRIMARY KEY, job_id TEXT NOT NULL, tenant TEXT NOT NULL, type TEXT NOT NULL, message TEXT NOT NULL DEFAULT '', payload_json JSONB NOT NULL DEFAULT '{}'::jsonb, created_at TIMESTAMPTZ NOT NULL);
CREATE INDEX IF NOT EXISTS idx_job_events_job ON job_events(job_id, created_at);
CREATE INDEX IF NOT EXISTS idx_job_events_tenant ON job_events(tenant, created_at);
CREATE TABLE IF NOT EXISTS control_leases (name TEXT PRIMARY KEY, owner TEXT NOT NULL, expires_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS tenants (id TEXT PRIMARY KEY, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS principals (subject TEXT PRIMARY KEY, email TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'active', created_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS tenant_memberships (tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, subject TEXT NOT NULL REFERENCES principals(subject) ON DELETE CASCADE, role TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active', created_at TIMESTAMPTZ NOT NULL, PRIMARY KEY (tenant_id, subject));
CREATE TABLE IF NOT EXISTS api_keys (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, subject TEXT NOT NULL REFERENCES principals(subject) ON DELETE CASCADE, prefix TEXT NOT NULL, hash TEXT NOT NULL UNIQUE, status TEXT NOT NULL DEFAULT 'active', created_at TIMESTAMPTZ NOT NULL, revoked_at TIMESTAMPTZ NULL);
CREATE INDEX IF NOT EXISTS idx_memberships_subject ON tenant_memberships(subject, status);
CREATE INDEX IF NOT EXISTS idx_api_keys_tenant ON api_keys(tenant_id, status);
CREATE TABLE IF NOT EXISTS tenant_secrets (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE, name TEXT NOT NULL, ciphertext BYTEA NOT NULL, nonce BYTEA NOT NULL, metadata JSONB NOT NULL DEFAULT '{}'::jsonb, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, UNIQUE(tenant_id,name));
CREATE INDEX IF NOT EXISTS idx_tenant_secrets_tenant ON tenant_secrets(tenant_id,name);`)
	return err
}

func (s *PostgresSaaSStore) CreateTenant(ctx context.Context, t *TenantRecord) error {
	if t == nil || t.ID == "" || t.Slug == "" || t.Name == "" {
		return errors.New("tenant id, slug, and name are required")
	}
	if t.Status == "" {
		t.Status = "active"
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenants(id,slug,name,status,created_at) VALUES($1,$2,$3,$4,$5)`, t.ID, t.Slug, t.Name, t.Status, t.CreatedAt)
	return err
}
func (s *PostgresSaaSStore) GetTenant(ctx context.Context, id string) (*TenantRecord, error) {
	var t TenantRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,slug,name,status,created_at FROM tenants WHERE id=$1`, id).Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}
func (s *PostgresSaaSStore) UpsertPrincipal(ctx context.Context, p *PrincipalRecord) error {
	if p == nil || p.Subject == "" {
		return errors.New("principal subject is required")
	}
	if p.Status == "" {
		p.Status = "active"
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO principals(subject,email,status,created_at) VALUES($1,$2,$3,$4) ON CONFLICT(subject) DO UPDATE SET email=EXCLUDED.email,status=EXCLUDED.status`, p.Subject, p.Email, p.Status, p.CreatedAt)
	return err
}
func (s *PostgresSaaSStore) SetMembership(ctx context.Context, m *MembershipRecord) error {
	if m == nil || m.TenantID == "" || m.Subject == "" || m.Role == "" {
		return errors.New("membership tenant, subject, and role are required")
	}
	if m.Status == "" {
		m.Status = "active"
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenant_memberships(tenant_id,subject,role,status,created_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,subject) DO UPDATE SET role=EXCLUDED.role,status=EXCLUDED.status`, m.TenantID, m.Subject, m.Role, m.Status, m.CreatedAt)
	return err
}
func (s *PostgresSaaSStore) GetMembership(ctx context.Context, tenantID, subject string) (*MembershipRecord, error) {
	var m MembershipRecord
	err := s.db.QueryRowContext(ctx, `SELECT tenant_id,subject,role,status,created_at FROM tenant_memberships WHERE tenant_id=$1 AND subject=$2`, tenantID, subject).Scan(&m.TenantID, &m.Subject, &m.Role, &m.Status, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}
func (s *PostgresSaaSStore) CreateAPIKey(ctx context.Context, k *APIKeyRecord) error {
	if k == nil || k.ID == "" || k.TenantID == "" || k.Subject == "" || k.Hash == "" {
		return errors.New("API key id, tenant, subject, and hash are required")
	}
	if k.Status == "" {
		k.Status = "active"
	}
	if k.CreatedAt.IsZero() {
		k.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys(id,tenant_id,subject,prefix,hash,status,created_at,revoked_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, k.ID, k.TenantID, k.Subject, k.Prefix, k.Hash, k.Status, k.CreatedAt, k.RevokedAt)
	return err
}
func (s *PostgresSaaSStore) GetAPIKeyByHash(ctx context.Context, hash string) (*APIKeyRecord, error) {
	var k APIKeyRecord
	var revoked sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id,tenant_id,subject,prefix,hash,status,created_at,revoked_at FROM api_keys WHERE hash=$1 AND status='active'`, hash).Scan(&k.ID, &k.TenantID, &k.Subject, &k.Prefix, &k.Hash, &k.Status, &k.CreatedAt, &revoked)
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
func (s *PostgresSaaSStore) RevokeAPIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET status='revoked',revoked_at=$1 WHERE id=$2`, time.Now().UTC(), id)
	return err
}

func (s *PostgresSaaSStore) CreateJob(ctx context.Context, j *DurableJob) error {
	return (&PostgresJobStore{db: s.db}).CreateJob(ctx, j)
}
func (s *PostgresSaaSStore) GetJob(ctx context.Context, id string) (*DurableJob, error) {
	return (&PostgresJobStore{db: s.db}).GetJob(ctx, id)
}
func (s *PostgresSaaSStore) ClaimJobs(ctx context.Context, now time.Time, owner string, limit int, lease time.Duration) ([]DurableJob, error) {
	return (&PostgresJobStore{db: s.db}).ClaimJobs(ctx, now, owner, limit, lease)
}
func (s *PostgresSaaSStore) UpdateJob(ctx context.Context, id, status string, nextRun time.Time, lastError string) error {
	return (&PostgresJobStore{db: s.db}).UpdateJob(ctx, id, status, nextRun, lastError)
}
func (s *PostgresSaaSStore) AcquireLease(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	return (&PostgresJobStore{db: s.db}).AcquireLease(ctx, name, owner, ttl)
}
func (s *PostgresSaaSStore) ReleaseLease(ctx context.Context, name, owner string) error {
	return (&PostgresJobStore{db: s.db}).ReleaseLease(ctx, name, owner)
}

// Compile-time contracts for the two SaaS subsystems.
var _ IdentityStore = (*PostgresSaaSStore)(nil)
