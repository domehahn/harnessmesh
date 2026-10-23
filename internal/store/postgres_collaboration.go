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

// PostgresCollaborationStore persists the session-scoped collaboration
// payloads with an explicit tenant column. Payload JSON keeps this migration
// compatible with the existing protocol while relational indexes provide the
// tenant/session lookup boundary.
type PostgresCollaborationStore struct{ db *sql.DB }

func NewPostgresCollaborationStore(db *sql.DB) (*PostgresCollaborationStore, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	return &PostgresCollaborationStore{db: db}, nil
}

func (s *PostgresCollaborationStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS collaboration_messages (id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,session_id TEXT NOT NULL,payload_json JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL);CREATE INDEX IF NOT EXISTS idx_collab_messages_tenant_session ON collaboration_messages(tenant_id,session_id,created_at);CREATE TABLE IF NOT EXISTS collaboration_findings (id TEXT NOT NULL,tenant_id TEXT NOT NULL,session_id TEXT NOT NULL,payload_json JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL,PRIMARY KEY(tenant_id,id));CREATE INDEX IF NOT EXISTS idx_collab_findings_tenant_session ON collaboration_findings(tenant_id,session_id,created_at);CREATE TABLE IF NOT EXISTS collaboration_evidence (id TEXT NOT NULL,tenant_id TEXT NOT NULL,session_id TEXT NOT NULL,payload_json JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL);CREATE INDEX IF NOT EXISTS idx_collab_evidence_tenant_session ON collaboration_evidence(tenant_id,session_id,created_at);CREATE TABLE IF NOT EXISTS collaboration_events (id BIGSERIAL PRIMARY KEY,tenant_id TEXT NOT NULL,session_id TEXT NOT NULL,event_type TEXT NOT NULL,payload_json JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL);CREATE INDEX IF NOT EXISTS idx_collab_events_tenant_session ON collaboration_events(tenant_id,session_id,created_at);CREATE TABLE IF NOT EXISTS collaboration_aux (kind TEXT NOT NULL,id TEXT NOT NULL,tenant_id TEXT NOT NULL,session_id TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL,payload_json JSONB NOT NULL,PRIMARY KEY(kind,tenant_id,id));CREATE INDEX IF NOT EXISTS idx_collab_aux_session ON collaboration_aux(kind,tenant_id,session_id,created_at);CREATE TABLE IF NOT EXISTS event_deliveries_pg (id TEXT NOT NULL,tenant_id TEXT NOT NULL,event_id TEXT NOT NULL,participant_id TEXT NOT NULL,payload_json JSONB NOT NULL,PRIMARY KEY(tenant_id,id));CREATE UNIQUE INDEX IF NOT EXISTS idx_event_deliveries_pg_lookup ON event_deliveries_pg(tenant_id,event_id,participant_id);`)
	return err
}

func (s *PostgresCollaborationStore) auxSave(ctx context.Context, kind, sessionID, id string, value any, createdAt time.Time) error {
	tid, err := tenantID(ctx)
	if err != nil {
		return err
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO collaboration_aux(kind,id,tenant_id,session_id,created_at,payload_json) VALUES($1,$2,$3,$4,$5,$6::jsonb) ON CONFLICT(kind,tenant_id,id) DO UPDATE SET payload_json=EXCLUDED.payload_json`, kind, id, tid, sessionID, createdAt, string(raw))
	return err
}
func (s *PostgresCollaborationStore) auxList(ctx context.Context, kind, sessionID string, out any) error {
	tid, err := tenantID(ctx)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json FROM collaboration_aux WHERE kind=$1 AND tenant_id=$2 AND session_id=$3 ORDER BY created_at`, kind, tid, sessionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	switch v := out.(type) {
	case *[]protocol.ChallengePayload:
		for rows.Next() {
			var raw []byte
			var x protocol.ChallengePayload
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &x); err != nil {
				return err
			}
			*v = append(*v, x)
		}
	case *[]protocol.ResolutionPayload:
		for rows.Next() {
			var raw []byte
			var x protocol.ResolutionPayload
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &x); err != nil {
				return err
			}
			*v = append(*v, x)
		}
	}
	return rows.Err()
}

func tenantID(ctx context.Context) (string, error) {
	identity, err := tenant.FromContext(ctx)
	if err != nil {
		return "", err
	}
	return identity.Tenant, nil
}
func (s *PostgresCollaborationStore) SaveMessage(ctx context.Context, env *protocol.PeerEnvelope) error {
	if env == nil || env.ID == "" {
		return errors.New("message id is required")
	}
	tid, err := tenantID(ctx)
	if err != nil {
		return err
	}
	sid := env.SessionID
	if sid == "" {
		sid = env.SpaceID
	}
	if sid == "" {
		return errors.New("message session or space is required")
	}
	if env.CreatedAt.IsZero() {
		env.CreatedAt = time.Now().UTC()
	}
	payload, _ := json.Marshal(env)
	_, err = s.db.ExecContext(ctx, `INSERT INTO collaboration_messages(id,tenant_id,session_id,payload_json,created_at) VALUES($1,$2,$3,$4::jsonb,$5)`, env.ID, tid, sid, string(payload), env.CreatedAt)
	return err
}
func (s *PostgresCollaborationStore) GetMessages(ctx context.Context, sessionID string) ([]*protocol.PeerEnvelope, error) {
	tid, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json FROM collaboration_messages WHERE tenant_id=$1 AND session_id=$2 ORDER BY created_at`, tid, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*protocol.PeerEnvelope
	for rows.Next() {
		var raw []byte
		var env protocol.PeerEnvelope
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, err
		}
		out = append(out, &env)
	}
	return out, rows.Err()
}
func (s *PostgresCollaborationStore) SaveFinding(ctx context.Context, sessionID string, f *protocol.FindingPayload) error {
	if f == nil || f.ID == "" {
		return errors.New("finding id is required")
	}
	tid, err := tenantID(ctx)
	if err != nil {
		return err
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}
	f.SessionID = sessionID
	raw, _ := json.Marshal(f)
	_, err = s.db.ExecContext(ctx, `INSERT INTO collaboration_findings(id,tenant_id,session_id,payload_json,created_at) VALUES($1,$2,$3,$4::jsonb,$5)`, f.ID, tid, sessionID, string(raw), f.CreatedAt)
	return err
}
func (s *PostgresCollaborationStore) GetFindings(ctx context.Context, sessionID string) ([]protocol.FindingPayload, error) {
	tid, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json FROM collaboration_findings WHERE tenant_id=$1 AND session_id=$2 ORDER BY created_at`, tid, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.FindingPayload
	for rows.Next() {
		var raw []byte
		var f protocol.FindingPayload
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (s *PostgresCollaborationStore) SaveEvidence(ctx context.Context, sessionID string, e *protocol.EvidencePayload) error {
	if e == nil || e.ID == "" {
		return errors.New("evidence id is required")
	}
	tid, err := tenantID(ctx)
	if err != nil {
		return err
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	raw, _ := json.Marshal(e)
	_, err = s.db.ExecContext(ctx, `INSERT INTO collaboration_evidence(id,tenant_id,session_id,payload_json,created_at) VALUES($1,$2,$3,$4::jsonb,$5)`, e.ID, tid, sessionID, string(raw), e.CreatedAt)
	return err
}
func (s *PostgresCollaborationStore) GetEvidence(ctx context.Context, sessionID string) ([]protocol.EvidencePayload, error) {
	tid, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json FROM collaboration_evidence WHERE tenant_id=$1 AND session_id=$2 ORDER BY created_at`, tid, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.EvidencePayload
	for rows.Next() {
		var raw []byte
		var e protocol.EvidencePayload
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *PostgresCollaborationStore) EmitEvent(ctx context.Context, sessionID, eventType string, payload any) error {
	tid, err := tenantID(ctx)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(payload)
	_, err = s.db.ExecContext(ctx, `INSERT INTO collaboration_events(tenant_id,session_id,event_type,payload_json,created_at) VALUES($1,$2,$3,$4::jsonb,$5)`, tid, sessionID, eventType, string(raw), time.Now().UTC())
	return err
}
func (s *PostgresCollaborationStore) GetEvents(ctx context.Context, sessionID string) ([]EventRecord, error) {
	tid, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,event_type,created_at,payload_json FROM collaboration_events WHERE tenant_id=$1 AND session_id=$2 ORDER BY id`, tid, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRecord
	for rows.Next() {
		var e EventRecord
		var raw []byte
		if err := rows.Scan(&e.ID, &e.SessionID, &e.EventType, &e.Timestamp, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &e.Payload)
		out = append(out, e)
	}
	return out, rows.Err()
}
