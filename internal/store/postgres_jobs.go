package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// PostgresJobStore is the first PostgreSQL-backed slice of the Store model.
// The SQL driver is injected by the application (for example pgx/stdlib), so
// this package does not force a driver on local SQLite-only installations.
type PostgresJobStore struct{ db *sql.DB }

func NewPostgresJobStore(db *sql.DB) (*PostgresJobStore, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	return &PostgresJobStore{db: db}, nil
}

func (s *PostgresJobStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS durable_jobs (
 id TEXT PRIMARY KEY,
 tenant TEXT NOT NULL,
 kind TEXT NOT NULL,
 payload_json JSONB NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending',
 attempts INTEGER NOT NULL DEFAULT 0,
 max_attempts INTEGER NOT NULL DEFAULT 3,
 next_run_at TIMESTAMPTZ NOT NULL,
 lease_owner TEXT NOT NULL DEFAULT '',
 lease_until TIMESTAMPTZ NULL,
 last_error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_durable_jobs_claim ON durable_jobs(status, next_run_at, lease_until);
CREATE INDEX IF NOT EXISTS idx_durable_jobs_tenant ON durable_jobs(tenant, status);
CREATE TABLE IF NOT EXISTS control_leases (
 name TEXT PRIMARY KEY,
 owner TEXT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS job_retry_outbox_pg (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, next_attempt TIMESTAMPTZ NOT NULL, priority INTEGER NOT NULL DEFAULT 0, attempt INTEGER NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL);
CREATE INDEX IF NOT EXISTS idx_job_retry_pg_claim ON job_retry_outbox_pg(next_attempt,priority DESC);
CREATE TABLE IF NOT EXISTS job_dead_letters_pg (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, dead_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL);
CREATE TABLE IF NOT EXISTS job_dependencies_pg (job_id TEXT NOT NULL, depends_on TEXT NOT NULL, tenant_id TEXT NOT NULL, status TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, PRIMARY KEY(tenant_id,job_id,depends_on));
CREATE TABLE IF NOT EXISTS agent_health_pg (agent TEXT NOT NULL, tenant_id TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, PRIMARY KEY(tenant_id,agent)
);`)
	return err
}

func (s *PostgresJobStore) CreateJob(ctx context.Context, job *DurableJob) error {
	if job == nil || job.ID == "" || job.Tenant == "" || job.Kind == "" {
		return errors.New("job id, tenant, and kind are required")
	}
	if job.Status == "" {
		job.Status = "pending"
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = 3
	}
	if job.NextRunAt.IsZero() {
		job.NextRunAt = time.Now().UTC()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	job.UpdatedAt = job.CreatedAt
	_, err := s.db.ExecContext(ctx, `INSERT INTO durable_jobs(id,tenant,kind,payload_json,status,attempts,max_attempts,next_run_at,lease_owner,lease_until,last_error,created_at,updated_at) VALUES($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, job.ID, job.Tenant, job.Kind, string(job.Payload), job.Status, job.Attempts, job.MaxAttempts, job.NextRunAt, job.LeaseOwner, nullableTime(job.LeaseUntil), job.LastError, job.CreatedAt, job.UpdatedAt)
	return err
}

func (s *PostgresJobStore) GetJob(ctx context.Context, id string) (*DurableJob, error) {
	var j DurableJob
	var payload []byte
	var lease sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id,tenant,kind,payload_json,status,attempts,max_attempts,next_run_at,lease_owner,lease_until,last_error,created_at,updated_at FROM durable_jobs WHERE id=$1`, id).Scan(&j.ID, &j.Tenant, &j.Kind, &payload, &j.Status, &j.Attempts, &j.MaxAttempts, &j.NextRunAt, &j.LeaseOwner, &lease, &j.LastError, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.Payload = json.RawMessage(payload)
	if lease.Valid {
		j.LeaseUntil = lease.Time
	}
	return &j, nil
}

func (s *PostgresJobStore) ClaimJobs(ctx context.Context, now time.Time, owner string, limit int, lease time.Duration) ([]DurableJob, error) {
	if owner == "" {
		return nil, errors.New("job lease owner is required")
	}
	if limit <= 0 {
		limit = 1
	}
	if lease <= 0 {
		lease = time.Minute
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM durable_jobs WHERE status IN ('pending','retry') AND next_run_at <= $1 AND (lease_until IS NULL OR lease_until <= $1) ORDER BY next_run_at,created_at FOR UPDATE SKIP LOCKED LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	until := now.Add(lease)
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE durable_jobs SET status='running',attempts=attempts+1,lease_owner=$1,lease_until=$2,updated_at=$3 WHERE id=$4`, owner, until, now, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]DurableJob, 0, len(ids))
	for _, id := range ids {
		j, err := s.GetJob(ctx, id)
		if err != nil {
			return nil, err
		}
		if j != nil {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (s *PostgresJobStore) UpdateJob(ctx context.Context, id, status string, nextRun time.Time, lastError string) error {
	if id == "" || status == "" {
		return errors.New("job id and status are required")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE durable_jobs SET status=$1,next_run_at=$2,lease_owner='',lease_until=NULL,last_error=$3,updated_at=$4 WHERE id=$5`, status, nextRun, lastError, time.Now().UTC(), id)
	return err
}

func (s *PostgresJobStore) AcquireLease(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	if name == "" || owner == "" {
		return false, errors.New("lease name and owner are required")
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	now := time.Now().UTC()
	expires := now.Add(ttl)
	res, err := s.db.ExecContext(ctx, `INSERT INTO control_leases(name,owner,expires_at) VALUES($1,$2,$3) ON CONFLICT(name) DO UPDATE SET owner=EXCLUDED.owner,expires_at=EXCLUDED.expires_at WHERE control_leases.expires_at <= $4 OR control_leases.owner=$2`, name, owner, expires, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *PostgresJobStore) ReleaseLease(ctx context.Context, name, owner string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM control_leases WHERE name=$1 AND owner=$2`, name, owner)
	return err
}
