package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/tenant"
)

// PostgresGovernanceStore persists tenant-scoped approvals and decisions. The
// full JSON payload is retained so the control plane can evolve without a
// destructive schema migration; the tenant and query keys remain relational.
type PostgresGovernanceStore struct{ db *sql.DB }

func NewPostgresGovernanceStore(db *sql.DB) (*PostgresGovernanceStore, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	return &PostgresGovernanceStore{db: db}, nil
}

func (s *PostgresGovernanceStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS governance_approvals (
 id TEXT PRIMARY KEY,
 tenant_id TEXT NOT NULL,
 status TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 payload JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_governance_approvals_tenant_status ON governance_approvals(tenant_id, status, created_at DESC);
CREATE TABLE IF NOT EXISTS governance_decisions (
 id TEXT NOT NULL,
 tenant_id TEXT NOT NULL,
 space_id TEXT NOT NULL,
 status TEXT NOT NULL,
 created_at TIMESTAMPTZ NOT NULL,
 payload JSONB NOT NULL,
 PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS idx_governance_decisions_tenant_space ON governance_decisions(tenant_id, space_id, created_at);
`)
	return err
}

func governanceTenant(ctx context.Context) (string, error) {
	identity, err := tenant.FromContext(ctx)
	if err != nil {
		return "", err
	}
	return identity.Tenant, nil
}

func (s *PostgresGovernanceStore) SaveApproval(ctx context.Context, approval *protocol.ApprovalRequest) error {
	if approval == nil || approval.ID == "" {
		return errors.New("approval id is required")
	}
	tenantID, err := governanceTenant(ctx)
	if err != nil {
		return err
	}
	if approval.CreatedAt.IsZero() {
		approval.CreatedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(approval)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO governance_approvals(id,tenant_id,status,created_at,payload) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,payload=EXCLUDED.payload`, approval.ID, tenantID, string(approval.Status), approval.CreatedAt, string(payload))
	return err
}

func (s *PostgresGovernanceStore) GetApproval(ctx context.Context, id string) (*protocol.ApprovalRequest, error) {
	tenantID, err := governanceTenant(ctx)
	if err != nil {
		return nil, err
	}
	var payload []byte
	err = s.db.QueryRowContext(ctx, `SELECT payload FROM governance_approvals WHERE id=$1 AND tenant_id=$2`, id, tenantID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var approval protocol.ApprovalRequest
	if err := json.Unmarshal(payload, &approval); err != nil {
		return nil, err
	}
	return &approval, nil
}

func (s *PostgresGovernanceStore) ListApprovals(ctx context.Context, status protocol.ApprovalStatus) ([]protocol.ApprovalRequest, error) {
	tenantID, err := governanceTenant(ctx)
	if err != nil {
		return nil, err
	}
	query := `SELECT payload FROM governance_approvals WHERE tenant_id=$1`
	args := []any{tenantID}
	if status != "" {
		query += ` AND status=$2`
		args = append(args, string(status))
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.ApprovalRequest
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var approval protocol.ApprovalRequest
		if err := json.Unmarshal(payload, &approval); err != nil {
			return nil, err
		}
		out = append(out, approval)
	}
	return out, rows.Err()
}

func (s *PostgresGovernanceStore) ExpireApprovals(ctx context.Context, before time.Time) (int64, error) {
	tenantID, err := governanceTenant(ctx)
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE governance_approvals SET status=$1,payload=jsonb_set(payload,'{status}',to_jsonb($1::text),true) WHERE tenant_id=$2 AND status=$3 AND created_at<$4`, string(protocol.ApprovalExpired), tenantID, string(protocol.ApprovalPending), before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *PostgresGovernanceStore) SaveDecision(ctx context.Context, decision *protocol.Decision) error {
	if decision == nil || decision.ID == "" || decision.SpaceID == "" {
		return errors.New("decision id and space id are required")
	}
	tenantID, err := governanceTenant(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if decision.CreatedAt.IsZero() {
		decision.CreatedAt = now
	}
	decision.UpdatedAt = now
	payload, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO governance_decisions(id,tenant_id,space_id,status,created_at,payload) VALUES($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT(tenant_id,id) DO UPDATE SET space_id=EXCLUDED.space_id,status=EXCLUDED.status,payload=EXCLUDED.payload`, decision.ID, tenantID, decision.SpaceID, string(decision.Status), decision.CreatedAt, string(payload))
	return err
}

func (s *PostgresGovernanceStore) GetDecision(ctx context.Context, spaceID, id string) (*protocol.Decision, error) {
	tenantID, err := governanceTenant(ctx)
	if err != nil {
		return nil, err
	}
	var payload []byte
	err = s.db.QueryRowContext(ctx, `SELECT payload FROM governance_decisions WHERE tenant_id=$1 AND space_id=$2 AND id=$3`, tenantID, spaceID, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var decision protocol.Decision
	if err := json.Unmarshal(payload, &decision); err != nil {
		return nil, err
	}
	return &decision, nil
}

func (s *PostgresGovernanceStore) ListDecisions(ctx context.Context, spaceID string) ([]protocol.Decision, error) {
	tenantID, err := governanceTenant(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM governance_decisions WHERE tenant_id=$1 AND space_id=$2 ORDER BY created_at ASC`, tenantID, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Decision
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var decision protocol.Decision
		if err := json.Unmarshal(payload, &decision); err != nil {
			return nil, err
		}
		out = append(out, decision)
	}
	return out, rows.Err()
}
