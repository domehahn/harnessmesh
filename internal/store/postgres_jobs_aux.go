package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

func (s *PostgresJobStore) ListJobs(ctx context.Context, tenantID, status string, limit int) ([]DurableJob, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `SELECT id FROM durable_jobs WHERE 1=1`
	args := []any{}
	n := 1
	if tenantID != "" {
		q += ` AND tenant=$` + string(rune('0'+n))
		args = append(args, tenantID)
		n++
	}
	if status != "" {
		q += ` AND status=$` + string(rune('0'+n))
		args = append(args, status)
		n++
	}
	q += ` ORDER BY created_at LIMIT $` + string(rune('0'+n))
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DurableJob
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		j, err := s.GetJob(ctx, id)
		if err != nil {
			return nil, err
		}
		if j != nil {
			out = append(out, *j)
		}
	}
	return out, rows.Err()
}
func (s *PostgresJobStore) SaveJobDependency(ctx context.Context, d *JobDependency) error {
	if d == nil || d.JobID == "" || d.DependsOn == "" {
		return errors.New("job dependency ids are required")
	}
	t, e := tenantID(ctx)
	if e != nil {
		return e
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO job_dependencies_pg(job_id,depends_on,tenant_id,status,created_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,job_id,depends_on) DO UPDATE SET status=EXCLUDED.status`, d.JobID, d.DependsOn, t, d.Status, d.CreatedAt)
	return e
}
func (s *PostgresJobStore) ListJobDependencies(ctx context.Context, jobID string) ([]JobDependency, error) {
	t, e := tenantID(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT job_id,depends_on,status,created_at FROM job_dependencies_pg WHERE tenant_id=$1 AND job_id=$2 ORDER BY depends_on`, t, jobID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []JobDependency
	for rows.Next() {
		var d JobDependency
		if e := rows.Scan(&d.JobID, &d.DependsOn, &d.Status, &d.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *PostgresJobStore) SaveAgentHealth(ctx context.Context, h *protocol.AgentHealth) error {
	if h == nil || h.Agent == "" {
		return errors.New("agent health agent is required")
	}
	t, e := tenantID(ctx)
	if e != nil {
		return e
	}
	if h.UpdatedAt.IsZero() {
		h.UpdatedAt = time.Now().UTC()
	}
	raw, e := json.Marshal(h)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO agent_health_pg(agent,tenant_id,updated_at,payload) VALUES($1,$2,$3,$4::jsonb) ON CONFLICT(tenant_id,agent) DO UPDATE SET updated_at=EXCLUDED.updated_at,payload=EXCLUDED.payload`, h.Agent, t, h.UpdatedAt, string(raw))
	return e
}
func (s *PostgresJobStore) GetAgentHealth(ctx context.Context, agent string) (*protocol.AgentHealth, error) {
	t, e := tenantID(ctx)
	if e != nil {
		return nil, e
	}
	var raw []byte
	e = s.db.QueryRowContext(ctx, `SELECT payload FROM agent_health_pg WHERE tenant_id=$1 AND agent=$2`, t, agent).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var h protocol.AgentHealth
	e = json.Unmarshal(raw, &h)
	return &h, e
}
func (s *PostgresJobStore) ListAgentHealth(ctx context.Context) ([]protocol.AgentHealth, error) {
	t, e := tenantID(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT payload FROM agent_health_pg WHERE tenant_id=$1 ORDER BY agent`, t)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []protocol.AgentHealth
	for rows.Next() {
		var raw []byte
		var h protocol.AgentHealth
		if e := rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e := json.Unmarshal(raw, &h); e != nil {
			return nil, e
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
func (s *PostgresJobStore) EnqueueRetry(ctx context.Context, item *RetryItem) error {
	if item == nil || item.ID == "" {
		return errors.New("retry item id is required")
	}
	t, e := tenantID(ctx)
	if e != nil {
		return e
	}
	if item.NextAttempt.IsZero() {
		item.NextAttempt = time.Now().UTC().Add(5 * time.Second)
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	raw, e := json.Marshal(item)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO job_retry_outbox_pg(id,tenant_id,next_attempt,priority,attempt,created_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb) ON CONFLICT(id) DO UPDATE SET next_attempt=EXCLUDED.next_attempt,priority=EXCLUDED.priority,attempt=EXCLUDED.attempt,payload=EXCLUDED.payload`, item.ID, t, item.NextAttempt, item.Priority, item.Attempt, item.CreatedAt, string(raw))
	return e
}
func (s *PostgresJobStore) ListRetries(ctx context.Context, limit int) ([]RetryItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	t, e := tenantID(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT payload FROM job_retry_outbox_pg WHERE tenant_id=$1 ORDER BY priority DESC,next_attempt LIMIT $2`, t, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []RetryItem
	for rows.Next() {
		var raw []byte
		var v RetryItem
		if e := rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e := json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *PostgresJobStore) ClaimRetries(ctx context.Context, now time.Time, limit int) ([]RetryItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	t, e := tenantID(ctx)
	if e != nil {
		return nil, e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id,payload FROM job_retry_outbox_pg WHERE tenant_id=$1 AND next_attempt<=$2 ORDER BY priority DESC,next_attempt FOR UPDATE SKIP LOCKED LIMIT $3`, t, now, limit)
	if e != nil {
		return nil, e
	}
	var out []RetryItem
	for rows.Next() {
		var id string
		var raw []byte
		if e := rows.Scan(&id, &raw); e != nil {
			return nil, e
		}
		var v RetryItem
		if e := json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		v.Attempt++
		v.NextAttempt = now.Add(5 * time.Minute)
		out = append(out, v)
		b, _ := json.Marshal(v)
		if _, e = tx.ExecContext(ctx, `UPDATE job_retry_outbox_pg SET attempt=$1,next_attempt=$2,payload=$3::jsonb WHERE tenant_id=$4 AND id=$5`, v.Attempt, v.NextAttempt, string(b), t, id); e != nil {
			return nil, e
		}
	}
	rows.Close()
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *PostgresJobStore) DeferRetry(ctx context.Context, id string, next time.Time, last string) error {
	if next.IsZero() {
		next = time.Now().UTC().Add(15 * time.Minute)
	}
	t, e := tenantID(ctx)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `UPDATE job_retry_outbox_pg SET next_attempt=$1,payload=jsonb_set(payload,'{last_error}',to_jsonb($2::text),true),attempt=GREATEST(attempt-1,0) WHERE tenant_id=$3 AND id=$4`, next, last, t, id)
	return e
}
func (s *PostgresJobStore) CompleteRetry(ctx context.Context, id string, retry bool, next time.Time, last string) error {
	t, e := tenantID(ctx)
	if e != nil {
		return e
	}
	if retry {
		if next.IsZero() {
			next = time.Now().UTC().Add(5 * time.Minute)
		}
		_, e = s.db.ExecContext(ctx, `UPDATE job_retry_outbox_pg SET next_attempt=$1,payload=jsonb_set(payload,'{last_error}',to_jsonb($2::text),true) WHERE tenant_id=$3 AND id=$4`, next, last, t, id)
		return e
	}
	if last == "" {
		_, e = s.db.ExecContext(ctx, `DELETE FROM job_retry_outbox_pg WHERE tenant_id=$1 AND id=$2`, t, id)
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO job_dead_letters_pg(id,tenant_id,dead_at,payload) SELECT id,tenant_id,$1,jsonb_set(payload,'{error}',to_jsonb($2::text),true) FROM job_retry_outbox_pg WHERE tenant_id=$3 AND id=$4 ON CONFLICT(id) DO UPDATE SET payload=EXCLUDED.payload,dead_at=EXCLUDED.dead_at`, time.Now().UTC(), last, t, id)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `DELETE FROM job_retry_outbox_pg WHERE tenant_id=$1 AND id=$2`, t, id)
	return e
}
func (s *PostgresJobStore) ListDeadLetters(ctx context.Context, limit int) ([]DeadLetter, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	t, e := tenantID(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT payload FROM job_dead_letters_pg WHERE tenant_id=$1 ORDER BY dead_at DESC LIMIT $2`, t, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []DeadLetter
	for rows.Next() {
		var raw []byte
		var d DeadLetter
		if e := rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e := json.Unmarshal(raw, &d); e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *PostgresJobStore) RequeueDeadLetter(ctx context.Context, id string, next time.Time, priority int) error {
	t, e := tenantID(ctx)
	if e != nil {
		return e
	}
	var raw []byte
	e = s.db.QueryRowContext(ctx, `SELECT payload FROM job_dead_letters_pg WHERE tenant_id=$1 AND id=$2`, t, id).Scan(&raw)
	if e != nil {
		return e
	}
	var d DeadLetter
	if e = json.Unmarshal(raw, &d); e != nil {
		return e
	}
	item := RetryItem{ID: d.ID, EventID: d.EventID, SpaceID: d.SpaceID, ParticipantID: d.ParticipantID, Payload: d.Payload, Priority: priority, NextAttempt: next, CreatedAt: d.CreatedAt, LastError: d.Error}
	if e = s.EnqueueRetry(ctx, &item); e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `DELETE FROM job_dead_letters_pg WHERE tenant_id=$1 AND id=$2`, t, id)
	return e
}
