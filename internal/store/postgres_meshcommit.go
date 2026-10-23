package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

// PostgresMeshCommitStore is the tenant-aware persistence slice for the
// evidence-gated change protocol. JSONB keeps the protocol forward compatible;
// indexed parent/status columns provide safe and efficient control-plane reads.
type PostgresMeshCommitStore struct{ db *sql.DB }

func NewPostgresMeshCommitStore(db *sql.DB) (*PostgresMeshCommitStore, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	return &PostgresMeshCommitStore{db: db}, nil
}

func (s *PostgresMeshCommitStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS mesh_changes_pg (id TEXT NOT NULL, tenant_id TEXT NOT NULL, space_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, PRIMARY KEY(tenant_id,id));
CREATE INDEX IF NOT EXISTS idx_mesh_changes_pg_tenant_space_status ON mesh_changes_pg(tenant_id,space_id,status,created_at DESC);
CREATE TABLE IF NOT EXISTS mesh_change_paths_pg (tenant_id TEXT NOT NULL, change_id TEXT NOT NULL, path TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, PRIMARY KEY(tenant_id,change_id,path));
CREATE INDEX IF NOT EXISTS idx_mesh_change_paths_pg_change ON mesh_change_paths_pg(tenant_id,change_id,path);
CREATE TABLE IF NOT EXISTS mesh_proof_obligations_pg (id TEXT NOT NULL, tenant_id TEXT NOT NULL, change_id TEXT NOT NULL, status TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, PRIMARY KEY(tenant_id,id));
CREATE INDEX IF NOT EXISTS idx_mesh_obligations_pg_change ON mesh_proof_obligations_pg(tenant_id,change_id,created_at);
CREATE TABLE IF NOT EXISTS mesh_change_evidence_pg (id TEXT NOT NULL, tenant_id TEXT NOT NULL, change_id TEXT NOT NULL, obligation_id TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, PRIMARY KEY(tenant_id,id));
CREATE INDEX IF NOT EXISTS idx_mesh_evidence_pg_change ON mesh_change_evidence_pg(tenant_id,change_id,created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mesh_evidence_pg_obligation ON mesh_change_evidence_pg(tenant_id,obligation_id,created_at DESC);
CREATE TABLE IF NOT EXISTS mesh_gate_results_pg (id TEXT NOT NULL, tenant_id TEXT NOT NULL, change_id TEXT NOT NULL, evaluated_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, PRIMARY KEY(tenant_id,id));
CREATE INDEX IF NOT EXISTS idx_mesh_gates_pg_change ON mesh_gate_results_pg(tenant_id,change_id,evaluated_at DESC);`)
	return err
}

func meshTenant(ctx context.Context) (string, error) { return tenantID(ctx) }

func meshJSON(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }
func scanJSON(row interface{ Scan(...any) error }, dst any) error {
	var b []byte
	if err := row.Scan(&b); err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

func (s *PostgresMeshCommitStore) SaveMeshChange(ctx context.Context, change *protocol.MeshChange) error {
	if change == nil || change.ID == "" {
		return errors.New("change id is required")
	}
	t, err := meshTenant(ctx)
	if err != nil {
		return err
	}
	if change.CreatedAt.IsZero() {
		change.CreatedAt = time.Now().UTC()
	}
	change.UpdatedAt = time.Now().UTC()
	p, err := meshJSON(change)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO mesh_changes_pg(id,tenant_id,space_id,status,created_at,updated_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb) ON CONFLICT(tenant_id,id) DO UPDATE SET space_id=EXCLUDED.space_id,status=EXCLUDED.status,updated_at=EXCLUDED.updated_at,payload=EXCLUDED.payload`, change.ID, t, change.SpaceID, string(change.Status), change.CreatedAt, change.UpdatedAt, p)
	return err
}

func (s *PostgresMeshCommitStore) GetMeshChange(ctx context.Context, id string) (*protocol.MeshChange, error) {
	t, err := meshTenant(ctx)
	if err != nil {
		return nil, err
	}
	var c protocol.MeshChange
	err = scanJSON(s.db.QueryRowContext(ctx, `SELECT payload FROM mesh_changes_pg WHERE tenant_id=$1 AND id=$2`, t, id), &c)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &protocol.ChangeNotFoundError{ChangeID: id}
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *PostgresMeshCommitStore) ListMeshChanges(ctx context.Context, spaceID string, status protocol.MeshChangeStatus) ([]*protocol.MeshChange, error) {
	t, err := meshTenant(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT payload FROM mesh_changes_pg WHERE tenant_id=$1`
	args := []any{t}
	n := 2
	if spaceID != "" {
		q += ` AND space_id=$` + itoa(n)
		args = append(args, spaceID)
		n++
	}
	if status != "" {
		q += ` AND status=$` + itoa(n)
		args = append(args, string(status))
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*protocol.MeshChange
	for rows.Next() {
		var c protocol.MeshChange
		if err := scanJSON(rows, &c); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// itoa is deliberately tiny and avoids pulling formatting into SQL builders.
func itoa(n int) string {
	if n == 2 {
		return "2"
	}
	if n == 3 {
		return "3"
	}
	return "4"
}

func (s *PostgresMeshCommitStore) SaveChangePaths(ctx context.Context, paths []protocol.ChangePath) error {
	t, err := meshTenant(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range paths {
		if p.ChangeID == "" || p.Path == "" {
			return errors.New("change path requires change id and path")
		}
		if p.CreatedAt.IsZero() {
			p.CreatedAt = time.Now().UTC()
		}
		b, e := meshJSON(p)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO mesh_change_paths_pg(tenant_id,change_id,path,created_at,payload) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT(tenant_id,change_id,path) DO UPDATE SET created_at=EXCLUDED.created_at,payload=EXCLUDED.payload`, t, p.ChangeID, p.Path, p.CreatedAt, b); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *PostgresMeshCommitStore) GetChangePaths(ctx context.Context, changeID string) ([]protocol.ChangePath, error) {
	return queryMesh[protocol.ChangePath](ctx, s.db, `SELECT payload FROM mesh_change_paths_pg WHERE tenant_id=$1 AND change_id=$2 ORDER BY path`, changeID)
}

func (s *PostgresMeshCommitStore) SaveProofObligation(ctx context.Context, o *protocol.ProofObligation) error {
	if o == nil || o.ID == "" {
		return errors.New("obligation id is required")
	}
	t, e := meshTenant(ctx)
	if e != nil {
		return e
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	o.UpdatedAt = time.Now().UTC()
	b, e := meshJSON(o)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO mesh_proof_obligations_pg(id,tenant_id,change_id,status,created_at,updated_at,payload) VALUES($1,$2,$3,$4,$5,$6,$7::jsonb) ON CONFLICT(tenant_id,id) DO UPDATE SET status=EXCLUDED.status,updated_at=EXCLUDED.updated_at,payload=EXCLUDED.payload`, o.ID, t, o.ChangeID, string(o.Status), o.CreatedAt, o.UpdatedAt, b)
	return e
}
func (s *PostgresMeshCommitStore) GetProofObligation(ctx context.Context, id string) (*protocol.ProofObligation, error) {
	t, e := meshTenant(ctx)
	if e != nil {
		return nil, e
	}
	var o protocol.ProofObligation
	e = scanJSON(s.db.QueryRowContext(ctx, `SELECT payload FROM mesh_proof_obligations_pg WHERE tenant_id=$1 AND id=$2`, t, id), &o)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, &protocol.ProofObligationNotFoundError{ObligationID: id}
	}
	if e != nil {
		return nil, e
	}
	return &o, nil
}
func (s *PostgresMeshCommitStore) GetProofObligations(ctx context.Context, changeID string) ([]*protocol.ProofObligation, error) {
	x, e := queryMesh[protocol.ProofObligation](ctx, s.db, `SELECT payload FROM mesh_proof_obligations_pg WHERE tenant_id=$1 AND change_id=$2 ORDER BY created_at`, changeID)
	if e != nil {
		return nil, e
	}
	out := make([]*protocol.ProofObligation, len(x))
	for i := range x {
		out[i] = &x[i]
	}
	return out, nil
}
func (s *PostgresMeshCommitStore) UpdateProofObligationStatus(ctx context.Context, id string, status protocol.ProofObligationStatus, evidenceID string) error {
	t, e := meshTenant(ctx)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `UPDATE mesh_proof_obligations_pg SET status=$1,updated_at=$2,payload=jsonb_set(jsonb_set(payload,'{status}',to_jsonb($1::text),true),'{current_evidence_id}',to_jsonb($3::text),true) WHERE tenant_id=$4 AND id=$5`, string(status), time.Now().UTC(), evidenceID, t, id)
	return e
}

func (s *PostgresMeshCommitStore) SaveChangeEvidence(ctx context.Context, v *protocol.ChangeEvidence) error {
	if v == nil || v.ID == "" {
		return errors.New("evidence id is required")
	}
	t, e := meshTenant(ctx)
	if e != nil {
		return e
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	b, e := meshJSON(v)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO mesh_change_evidence_pg(id,tenant_id,change_id,obligation_id,created_at,payload) VALUES($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT(tenant_id,id) DO UPDATE SET payload=EXCLUDED.payload`, v.ID, t, v.ChangeID, v.ObligationID, v.CreatedAt, b)
	return e
}
func (s *PostgresMeshCommitStore) GetChangeEvidence(ctx context.Context, id string) (*protocol.ChangeEvidence, error) {
	t, e := meshTenant(ctx)
	if e != nil {
		return nil, e
	}
	var v protocol.ChangeEvidence
	e = scanJSON(s.db.QueryRowContext(ctx, `SELECT payload FROM mesh_change_evidence_pg WHERE tenant_id=$1 AND id=$2`, t, id), &v)
	if e != nil {
		return nil, e
	}
	return &v, nil
}
func (s *PostgresMeshCommitStore) GetChangeEvidenceForObligation(ctx context.Context, id string) ([]*protocol.ChangeEvidence, error) {
	x, e := queryMesh[protocol.ChangeEvidence](ctx, s.db, `SELECT payload FROM mesh_change_evidence_pg WHERE tenant_id=$1 AND obligation_id=$2 ORDER BY created_at DESC`, id)
	if e != nil {
		return nil, e
	}
	out := make([]*protocol.ChangeEvidence, len(x))
	for i := range x {
		out[i] = &x[i]
	}
	return out, nil
}
func (s *PostgresMeshCommitStore) GetChangeEvidenceForChange(ctx context.Context, id string) ([]*protocol.ChangeEvidence, error) {
	x, e := queryMesh[protocol.ChangeEvidence](ctx, s.db, `SELECT payload FROM mesh_change_evidence_pg WHERE tenant_id=$1 AND change_id=$2 ORDER BY created_at DESC`, id)
	if e != nil {
		return nil, e
	}
	out := make([]*protocol.ChangeEvidence, len(x))
	for i := range x {
		out[i] = &x[i]
	}
	return out, nil
}
func (s *PostgresMeshCommitStore) InvalidateChangeEvidence(ctx context.Context, id, reason string) error {
	t, e := meshTenant(ctx)
	if e != nil {
		return e
	}
	now := time.Now().UTC()
	_, e = s.db.ExecContext(ctx, `UPDATE mesh_change_evidence_pg SET payload=jsonb_set(jsonb_set(payload,'{valid}','false'::jsonb,true),'{invalidation_reason}',to_jsonb($1::text),true) WHERE tenant_id=$2 AND id=$3`, reason, t, id)
	_ = now
	return e
}

func (s *PostgresMeshCommitStore) SaveGateResult(ctx context.Context, r *protocol.GateResult) error {
	if r == nil || r.ID == "" {
		return errors.New("gate result id is required")
	}
	t, e := meshTenant(ctx)
	if e != nil {
		return e
	}
	if r.EvaluatedAt.IsZero() {
		r.EvaluatedAt = time.Now().UTC()
	}
	b, e := meshJSON(r)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO mesh_gate_results_pg(id,tenant_id,change_id,evaluated_at,payload) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT(tenant_id,id) DO UPDATE SET evaluated_at=EXCLUDED.evaluated_at,payload=EXCLUDED.payload`, r.ID, t, r.ChangeID, r.EvaluatedAt, b)
	return e
}
func (s *PostgresMeshCommitStore) GetLatestGateResult(ctx context.Context, id string) (*protocol.GateResult, error) {
	t, e := meshTenant(ctx)
	if e != nil {
		return nil, e
	}
	var r protocol.GateResult
	e = scanJSON(s.db.QueryRowContext(ctx, `SELECT payload FROM mesh_gate_results_pg WHERE tenant_id=$1 AND change_id=$2 ORDER BY evaluated_at DESC LIMIT 1`, t, id), &r)
	if e != nil {
		return nil, e
	}
	return &r, nil
}

func queryMesh[T any](ctx context.Context, db *sql.DB, q string, parent string) ([]T, error) {
	t, e := meshTenant(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := db.QueryContext(ctx, q, t, parent)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var v T
		if e := scanJSON(rows, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
