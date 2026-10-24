package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresExperimentStore provides the durable performance-observation and
// experiment control plane, including atomic reservation accounting.
type PostgresExperimentStore struct{ db *sql.DB }

func NewPostgresExperimentStore(db *sql.DB) (*PostgresExperimentStore, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	return &PostgresExperimentStore{db: db}, nil
}

func (s *PostgresExperimentStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS performance_observations_pg (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, task_class TEXT NOT NULL, agent_id TEXT NOT NULL, status TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL);
CREATE INDEX IF NOT EXISTS idx_perf_obs_pg_query ON performance_observations_pg(tenant_id,task_class,agent_id,created_at);
CREATE TABLE IF NOT EXISTS experiments_pg (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, state TEXT NOT NULL, max_executions INTEGER NOT NULL, max_concurrent INTEGER NOT NULL, reserved INTEGER NOT NULL, consumed INTEGER NOT NULL, released INTEGER NOT NULL, executed INTEGER NOT NULL, payload JSONB NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS experiment_reservations_pg (experiment_id TEXT NOT NULL, decision_id TEXT NOT NULL, status TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, PRIMARY KEY(experiment_id,decision_id));
CREATE TABLE IF NOT EXISTS experiment_admissions_pg (experiment_id TEXT NOT NULL, decision_id TEXT NOT NULL, tenant_id TEXT NOT NULL, payload JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL, PRIMARY KEY(experiment_id,decision_id));
CREATE TABLE IF NOT EXISTS experiment_audit_pg (id BIGSERIAL PRIMARY KEY, experiment_id TEXT NOT NULL, decision_id TEXT NOT NULL, tenant_id TEXT NOT NULL, event TEXT NOT NULL, reason TEXT NOT NULL, at TIMESTAMPTZ NOT NULL);`)
	return err
}

func (s *PostgresExperimentStore) SavePerformanceObservation(ctx context.Context, id, tenantID, taskClass, agentID, status string, createdAt time.Time, payloadJSON string) error {
	t, err := tenantIDFromContext(ctx)
	if err != nil {
		return err
	}
	if tenantID != t {
		return fmt.Errorf("tenant mismatch")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO performance_observations_pg(id,tenant_id,task_class,agent_id,status,created_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb) ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,payload=EXCLUDED.payload`, id, t, taskClass, agentID, status, createdAt, payloadJSON)
	return err
}
func (s *PostgresExperimentStore) GetPerformanceObservation(ctx context.Context, id string) (string, error) {
	t, e := tenantIDFromContext(ctx)
	if e != nil {
		return "", e
	}
	var p []byte
	e = s.db.QueryRowContext(ctx, `SELECT payload FROM performance_observations_pg WHERE tenant_id=$1 AND id=$2`, t, id).Scan(&p)
	return string(p), e
}
func (s *PostgresExperimentStore) QueryPerformanceObservations(ctx context.Context, tenantID, taskClass, agentID string) ([]string, error) {
	t, e := tenantIDFromContext(ctx)
	if e != nil {
		return nil, e
	}
	if tenantID != "" && tenantID != t {
		return nil, fmt.Errorf("tenant mismatch")
	}
	q := `SELECT payload FROM performance_observations_pg WHERE tenant_id=$1`
	args := []any{t}
	n := 2
	if taskClass != "" {
		q += fmt.Sprintf(` AND task_class=$%d`, n)
		args = append(args, taskClass)
		n++
	}
	if agentID != "" {
		q += fmt.Sprintf(` AND agent_id=$%d`, n)
		args = append(args, agentID)
	}
	q += ` ORDER BY created_at`
	rows, e := s.db.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p []byte
		if e := rows.Scan(&p); e != nil {
			return nil, e
		}
		out = append(out, string(p))
	}
	return out, rows.Err()
}

func (s *PostgresExperimentStore) SaveExperiment(ctx context.Context, id, tenantID, state string, maxExecutions, maxConcurrent, reserved, consumed, released, executed int, payloadJSON string, updatedAt time.Time) error {
	t, e := tenantIDFromContext(ctx)
	if e != nil {
		return e
	}
	if tenantID != t {
		return fmt.Errorf("tenant mismatch")
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO experiments_pg(id,tenant_id,state,max_executions,max_concurrent,reserved,consumed,released,executed,payload,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11) ON CONFLICT(id) DO UPDATE SET state=EXCLUDED.state,max_executions=EXCLUDED.max_executions,max_concurrent=EXCLUDED.max_concurrent,reserved=EXCLUDED.reserved,consumed=EXCLUDED.consumed,released=EXCLUDED.released,executed=EXCLUDED.executed,payload=EXCLUDED.payload,updated_at=EXCLUDED.updated_at WHERE experiments_pg.tenant_id=EXCLUDED.tenant_id`, id, t, state, maxExecutions, maxConcurrent, reserved, consumed, released, executed, payloadJSON, updatedAt)
	return e
}
func (s *PostgresExperimentStore) GetExperiment(ctx context.Context, id string) (ExperimentRecord, error) {
	t, e := tenantIDFromContext(ctx)
	if e != nil {
		return ExperimentRecord{}, e
	}
	var r ExperimentRecord
	var p []byte
	e = s.db.QueryRowContext(ctx, `SELECT id,tenant_id,state,max_executions,max_concurrent,reserved,consumed,released,executed,payload,updated_at FROM experiments_pg WHERE tenant_id=$1 AND id=$2`, t, id).Scan(&r.ID, &r.Tenant, &r.State, &r.MaxExecutions, &r.MaxConcurrent, &r.Reserved, &r.Consumed, &r.Released, &r.Executed, &p, &r.UpdatedAt)
	r.PayloadJSON = string(p)
	return r, e
}
func (s *PostgresExperimentStore) ReserveExperiment(ctx context.Context, id, decisionID string, now time.Time) (bool, error) {
	t, e := tenantIDFromContext(ctx)
	if e != nil {
		return false, e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	var state string
	var maxExec, maxConc, reserved, consumed int
	e = tx.QueryRowContext(ctx, `SELECT state,max_executions,max_concurrent,reserved,consumed FROM experiments_pg WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, t, id).Scan(&state, &maxExec, &maxConc, &reserved, &consumed)
	if e != nil {
		return false, e
	}
	if state != "enabled" || (maxExec > 0 && consumed+reserved >= maxExec) || (maxConc > 0 && reserved >= maxConc) {
		return false, nil
	}
	res, e := tx.ExecContext(ctx, `INSERT INTO experiment_reservations_pg(experiment_id,decision_id,status,created_at) VALUES($1,$2,'reserved',$3) ON CONFLICT DO NOTHING`, id, decisionID, now)
	if e != nil {
		return false, e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return true, tx.Commit()
	}
	if _, e = tx.ExecContext(ctx, `UPDATE experiments_pg SET reserved=reserved+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, t, id); e != nil {
		return false, e
	}
	return true, tx.Commit()
}
func (s *PostgresExperimentStore) transition(ctx context.Context, id, decisionID string, now time.Time, from, to string, delta string) error {
	t, e := tenantIDFromContext(ctx)
	if e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `UPDATE experiment_reservations_pg SET status=$1 WHERE experiment_id=$2 AND decision_id=$3 AND status=$4`, to, id, decisionID, from)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if to == "consumed" {
			return fmt.Errorf("experiment reservation not found: %s/%s", id, decisionID)
		}
		return tx.Commit()
	}
	if _, e = tx.ExecContext(ctx, `UPDATE experiments_pg SET reserved=GREATEST(reserved-1,0),`+delta+`=`+delta+`+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, t, id); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *PostgresExperimentStore) ReleaseExperiment(ctx context.Context, id, decisionID string, now time.Time) error {
	return s.transition(ctx, id, decisionID, now, "reserved", "released", "released")
}
func (s *PostgresExperimentStore) ConsumeExperiment(ctx context.Context, id, decisionID string, now time.Time) error {
	t, e := tenantIDFromContext(ctx)
	if e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `UPDATE experiment_reservations_pg SET status='consumed' WHERE experiment_id=$1 AND decision_id=$2 AND status='reserved'`, id, decisionID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("experiment reservation not found: %s/%s", id, decisionID)
	}
	if _, e = tx.ExecContext(ctx, `UPDATE experiments_pg SET reserved=GREATEST(reserved-1,0),consumed=consumed+1,executed=executed+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, t, id); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *PostgresExperimentStore) SaveExperimentAdmission(ctx context.Context, id, decisionID, payloadJSON string, createdAt time.Time) error {
	t, e := tenantIDFromContext(ctx)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO experiment_admissions_pg(experiment_id,decision_id,tenant_id,payload,created_at) VALUES($1,$2,$3,$4::jsonb,$5) ON CONFLICT(experiment_id,decision_id) DO UPDATE SET payload=EXCLUDED.payload`, id, decisionID, t, payloadJSON, createdAt)
	return e
}
func (s *PostgresExperimentStore) GetExperimentAdmission(ctx context.Context, id, decisionID string) (string, error) {
	t, e := tenantIDFromContext(ctx)
	if e != nil {
		return "", e
	}
	var p []byte
	e = s.db.QueryRowContext(ctx, `SELECT payload FROM experiment_admissions_pg WHERE tenant_id=$1 AND experiment_id=$2 AND decision_id=$3`, t, id, decisionID).Scan(&p)
	return string(p), e
}
func (s *PostgresExperimentStore) SaveExperimentAudit(ctx context.Context, id, decisionID, event, reason string, at time.Time) error {
	t, e := tenantIDFromContext(ctx)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO experiment_audit_pg(experiment_id,decision_id,tenant_id,event,reason,at) VALUES($1,$2,$3,$4,$5,$6)`, id, decisionID, t, event, reason, at)
	return e
}
func (s *PostgresExperimentStore) UpdateExperiment(ctx context.Context, id string, mutate func(ExperimentRecord) (ExperimentRecord, error)) error {
	t, e := tenantIDFromContext(ctx)
	if e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var r ExperimentRecord
	var p []byte
	e = tx.QueryRowContext(ctx, `SELECT id,tenant_id,state,max_executions,max_concurrent,reserved,consumed,released,executed,payload,updated_at FROM experiments_pg WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, t, id).Scan(&r.ID, &r.Tenant, &r.State, &r.MaxExecutions, &r.MaxConcurrent, &r.Reserved, &r.Consumed, &r.Released, &r.Executed, &p, &r.UpdatedAt)
	if e != nil {
		return e
	}
	r.PayloadJSON = string(p)
	u, e := mutate(r)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE experiments_pg SET state=$1,reserved=$2,consumed=$3,released=$4,executed=$5,payload=$6::jsonb,updated_at=$7 WHERE tenant_id=$8 AND id=$9`, u.State, u.Reserved, u.Consumed, u.Released, u.Executed, u.PayloadJSON, u.UpdatedAt, t, id)
	if e != nil {
		return e
	}
	return tx.Commit()
}

func tenantIDFromContext(ctx context.Context) (string, error) { return tenantID(ctx) }
