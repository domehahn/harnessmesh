package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

// PostgresSessionStore is the first tenant-aware Collaboration repository
// slice. It is intentionally independent from the legacy SQLite Store so the
// migration can be exercised incrementally.
type PostgresSessionStore struct{ db *sql.DB }

func NewPostgresSessionStore(db *sql.DB) (*PostgresSessionStore, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	return &PostgresSessionStore{db: db}, nil
}

func (s *PostgresSessionStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS collaboration_sessions (
 id TEXT NOT NULL,
 tenant_id TEXT NOT NULL,
 repo_root TEXT NOT NULL,
 task TEXT NOT NULL,
 status TEXT NOT NULL,
 stop_reason TEXT NOT NULL DEFAULT '',
 writer_participant TEXT NOT NULL DEFAULT '',
 participants_json JSONB NOT NULL DEFAULT '{}'::jsonb,
 budget_json JSONB NOT NULL DEFAULT '{}'::jsonb,
 metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY (tenant_id, id)
);
CREATE INDEX IF NOT EXISTS idx_collaboration_sessions_tenant ON collaboration_sessions(tenant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_collaboration_sessions_status ON collaboration_sessions(tenant_id, status);`)
	return err
}

func (s *PostgresSessionStore) SaveSession(ctx context.Context, session *Session) error {
	if session == nil || session.ID == "" || session.TenantID == "" {
		return errors.New("session id and tenant id are required")
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}
	session.UpdatedAt = time.Now().UTC()
	participants, err := json.Marshal(session.Participants)
	if err != nil {
		return err
	}
	budget, err := json.Marshal(session.Budget)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(session.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO collaboration_sessions(id,tenant_id,repo_root,task,status,stop_reason,writer_participant,participants_json,budget_json,metadata_json,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10::jsonb,$11,$12) ON CONFLICT(tenant_id, id) DO UPDATE SET repo_root=EXCLUDED.repo_root,task=EXCLUDED.task,status=EXCLUDED.status,stop_reason=EXCLUDED.stop_reason,writer_participant=EXCLUDED.writer_participant,participants_json=EXCLUDED.participants_json,budget_json=EXCLUDED.budget_json,metadata_json=EXCLUDED.metadata_json,updated_at=EXCLUDED.updated_at`, session.ID, session.TenantID, session.RepoRoot, session.Task, session.Status, session.StopReason, session.WriterParticipant, string(participants), string(budget), string(metadata), session.CreatedAt, session.UpdatedAt)
	return err
}

func (s *PostgresSessionStore) GetSession(ctx context.Context, id string) (*Session, error) {
	tenantID, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	return s.getSession(ctx, `WHERE id=$1 AND tenant_id=$2`, id, tenantID)
}
func (s *PostgresSessionStore) GetSessionForTenant(ctx context.Context, id, tenantID string) (*Session, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}
	return s.getSession(ctx, `WHERE id=$1 AND tenant_id=$2`, id, tenantID)
}
func (s *PostgresSessionStore) getSession(ctx context.Context, where string, args ...any) (*Session, error) {
	var session Session
	var participants, budget, metadata []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,tenant_id,repo_root,task,status,stop_reason,writer_participant,participants_json,budget_json,metadata_json,created_at,updated_at FROM collaboration_sessions `+where, args...).Scan(&session.ID, &session.TenantID, &session.RepoRoot, &session.Task, &session.Status, &session.StopReason, &session.WriterParticipant, &participants, &budget, &metadata, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(participants, &session.Participants)
	_ = json.Unmarshal(budget, &session.Budget)
	_ = json.Unmarshal(metadata, &session.Metadata)
	return &session, nil
}

func (s *PostgresSessionStore) ListSessionsForTenant(ctx context.Context, tenantID string) ([]*Session, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM collaboration_sessions WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	out := make([]*Session, 0, len(ids))
	for _, id := range ids {
		session, err := s.GetSessionForTenant(ctx, id, tenantID)
		if err != nil {
			return nil, err
		}
		if session != nil {
			out = append(out, session)
		}
	}
	return out, rows.Err()
}

func (s *PostgresSessionStore) UpdateSessionStatus(ctx context.Context, id, status, reason string) error {
	tenantID, err := tenantID(ctx)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE collaboration_sessions SET status=$1,stop_reason=$2,updated_at=$3 WHERE tenant_id=$4 AND id=$5`, status, reason, time.Now().UTC(), tenantID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &protocol.SessionNotFoundError{SessionID: id}
	}
	return nil
}

func (s *PostgresSessionStore) ListSessions(ctx context.Context) ([]*Session, error) {
	tenantID, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	return s.ListSessionsForTenant(ctx, tenantID)
}

func (s *PostgresSessionStore) UpdateSessionBudget(ctx context.Context, id string, budget protocol.BudgetStatus) error {
	tenantID, err := tenantID(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(budget)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE collaboration_sessions SET budget_json=$1::jsonb,updated_at=$2 WHERE tenant_id=$3 AND id=$4`, string(raw), time.Now().UTC(), tenantID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return &protocol.SessionNotFoundError{SessionID: id}
	}
	return nil
}

func (s *PostgresSessionStore) UpdateBudget(ctx context.Context, id string, budget protocol.BudgetStatus) error {
	return s.UpdateSessionBudget(ctx, id, budget)
}

func (s *PostgresSessionStore) UpdateParticipantHarnessSession(ctx context.Context, sessionID, agentName, harnessSessionID string) error {
	session, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if session == nil {
		return &protocol.SessionNotFoundError{SessionID: sessionID}
	}
	participant, ok := session.Participants[agentName]
	if !ok {
		return errors.New("participant not found")
	}
	participant.HarnessSessionID = harnessSessionID
	session.Participants[agentName] = participant
	return s.SaveSession(ctx, session)
}
