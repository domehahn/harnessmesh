package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

func (s *PostgresCollaborationStore) SaveRoutingDecision(ctx context.Context, r *RoutingDecisionRecord) error {
	if r == nil || r.ID == "" {
		return errors.New("routing decision id is required")
	}
	return s.auxSave(ctx, "routing", r.SessionID, r.ID, r, r.Timestamp)
}
func (s *PostgresCollaborationStore) GetRoutingDecisions(ctx context.Context, sessionID string) ([]RoutingDecisionRecord, error) {
	tid, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json FROM collaboration_aux WHERE kind='routing' AND tenant_id=$1 AND session_id=$2 ORDER BY created_at`, tid, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoutingDecisionRecord
	for rows.Next() {
		var raw []byte
		var v RoutingDecisionRecord
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *PostgresCollaborationStore) SaveUsage(ctx context.Context, u *UsageRecord) error {
	if u == nil || u.ID == "" {
		return errors.New("usage id is required")
	}
	return s.auxSave(ctx, "usage", u.SessionID, u.ID, u, u.Timestamp)
}
func (s *PostgresCollaborationStore) GetUsage(ctx context.Context, sessionID string) ([]UsageRecord, error) {
	tid, e := tenantID(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT payload_json FROM collaboration_aux WHERE kind='usage' AND tenant_id=$1 AND session_id=$2 ORDER BY created_at`, tid, sessionID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []UsageRecord
	for rows.Next() {
		var raw []byte
		var v UsageRecord
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
func (s *PostgresCollaborationStore) UpdateBudget(ctx context.Context, id string, budget protocol.BudgetStatus) error {
	if id == "" {
		return errors.New("id is required to update budget")
	}
	raw, e := json.Marshal(budget)
	if e != nil {
		return e
	}
	return s.auxSave(ctx, "budget", "", id, json.RawMessage(raw), time.Now().UTC())
}
func (s *PostgresCollaborationStore) GetGlobalBudget(ctx context.Context, scope string) (*BudgetLedger, error) {
	tid, e := tenantID(ctx)
	if e != nil {
		return nil, e
	}
	var raw []byte
	e = s.db.QueryRowContext(ctx, `SELECT payload_json FROM collaboration_aux WHERE kind='global_budget' AND tenant_id=$1 AND id=$2`, tid, scope).Scan(&raw)
	if e != nil {
		return nil, e
	}
	var b BudgetLedger
	e = json.Unmarshal(raw, &b)
	return &b, e
}
func (s *PostgresCollaborationStore) UpdateGlobalBudget(ctx context.Context, b *BudgetLedger) error {
	if b == nil || b.Scope == "" {
		return errors.New("budget scope is required")
	}
	return s.auxSave(ctx, "global_budget", "", b.Scope, b, b.UpdatedAt)
}
