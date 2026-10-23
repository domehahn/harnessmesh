package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

// PostgresSpaceStore persists the collaboration control plane that surrounds
// session messages. All rows carry the authenticated tenant boundary.
type PostgresSpaceStore struct{ db *sql.DB }

func NewPostgresSpaceStore(db *sql.DB) (*PostgresSpaceStore, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	return &PostgresSpaceStore{db: db}, nil
}
func (s *PostgresSpaceStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS collaboration_spaces_pg (id TEXT NOT NULL, tenant_id TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, PRIMARY KEY(tenant_id,id));
CREATE TABLE IF NOT EXISTS collaboration_channels_pg (id TEXT NOT NULL, tenant_id TEXT NOT NULL, space_id TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, PRIMARY KEY(tenant_id,id));
CREATE INDEX IF NOT EXISTS idx_channels_pg_space ON collaboration_channels_pg(tenant_id,space_id,created_at);
CREATE TABLE IF NOT EXISTS collaboration_threads_pg (id TEXT NOT NULL, tenant_id TEXT NOT NULL, space_id TEXT NOT NULL, channel_id TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, PRIMARY KEY(tenant_id,id));
CREATE INDEX IF NOT EXISTS idx_threads_pg_channel ON collaboration_threads_pg(tenant_id,space_id,channel_id,created_at);
CREATE TABLE IF NOT EXISTS collaboration_subscriptions_pg (id TEXT NOT NULL, tenant_id TEXT NOT NULL, space_id TEXT NOT NULL, participant_id TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, PRIMARY KEY(tenant_id,id));
CREATE INDEX IF NOT EXISTS idx_subscriptions_pg_participant ON collaboration_subscriptions_pg(tenant_id,space_id,participant_id);
CREATE TABLE IF NOT EXISTS collaboration_cursors_pg (tenant_id TEXT NOT NULL, space_id TEXT NOT NULL, participant_id TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, PRIMARY KEY(tenant_id,space_id,participant_id));
CREATE TABLE IF NOT EXISTS collaboration_summaries_pg (id BIGSERIAL PRIMARY KEY, tenant_id TEXT NOT NULL, space_id TEXT NOT NULL, target_id TEXT NOT NULL, summary_text TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL);
CREATE INDEX IF NOT EXISTS idx_summaries_pg_target ON collaboration_summaries_pg(tenant_id,space_id,target_id,created_at);`)
	return err
}
func (s *PostgresSpaceStore) tenant(ctx context.Context) (string, error) { return tenantID(ctx) }
func encodeSpace(v any) (string, error)                                  { b, e := json.Marshal(v); return string(b), e }
func decodeSpace(row interface{ Scan(...any) error }, dst any) error {
	var b []byte
	if e := row.Scan(&b); e != nil {
		return e
	}
	return json.Unmarshal(b, dst)
}
func (s *PostgresSpaceStore) SaveSpace(ctx context.Context, sp *protocol.CollaborationSpace) error {
	if sp == nil || sp.ID == "" {
		return errors.New("space id is required")
	}
	t, e := s.tenant(ctx)
	if e != nil {
		return e
	}
	if sp.CreatedAt.IsZero() {
		sp.CreatedAt = time.Now().UTC()
	}
	sp.UpdatedAt = time.Now().UTC()
	b, e := encodeSpace(sp)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO collaboration_spaces_pg(id,tenant_id,created_at,updated_at,payload) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT(tenant_id,id) DO UPDATE SET updated_at=EXCLUDED.updated_at,payload=EXCLUDED.payload`, sp.ID, t, sp.CreatedAt, sp.UpdatedAt, b)
	return e
}
func (s *PostgresSpaceStore) GetSpace(ctx context.Context, id string) (*protocol.CollaborationSpace, error) {
	t, e := s.tenant(ctx)
	if e != nil {
		return nil, e
	}
	var sp protocol.CollaborationSpace
	e = decodeSpace(s.db.QueryRowContext(ctx, `SELECT payload FROM collaboration_spaces_pg WHERE tenant_id=$1 AND id=$2`, t, id), &sp)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return &sp, nil
}
func (s *PostgresSpaceStore) ListSpaces(ctx context.Context) ([]*protocol.CollaborationSpace, error) {
	t, e := s.tenant(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT payload FROM collaboration_spaces_pg WHERE tenant_id=$1 ORDER BY created_at DESC`, t)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []*protocol.CollaborationSpace
	for rows.Next() {
		var sp protocol.CollaborationSpace
		if e := decodeSpace(rows, &sp); e != nil {
			return nil, e
		}
		out = append(out, &sp)
	}
	return out, rows.Err()
}
func (s *PostgresSpaceStore) UpdateSpaceLifecycle(ctx context.Context, id string, state protocol.SpaceLifecycleState) error {
	t, e := s.tenant(ctx)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `UPDATE collaboration_spaces_pg SET updated_at=$1,payload=jsonb_set(payload,'{lifecycle_state}',to_jsonb($2::text),true) WHERE tenant_id=$3 AND id=$4`, time.Now().UTC(), string(state), t, id)
	return e
}
func (s *PostgresSpaceStore) AddSpaceParticipant(ctx context.Context, spaceID string, p *protocol.SpaceParticipant) error {
	if p == nil || p.ID == "" {
		return errors.New("participant id is required")
	}
	sp, e := s.GetSpace(ctx, spaceID)
	if e != nil {
		return e
	}
	if sp == nil {
		return fmt.Errorf("space %q not found", spaceID)
	}
	if sp.Participants == nil {
		sp.Participants = map[string]protocol.SpaceParticipant{}
	}
	if p.JoinedAt.IsZero() {
		p.JoinedAt = time.Now().UTC()
	}
	p.UpdatedAt = time.Now().UTC()
	sp.Participants[p.ID] = *p
	return s.SaveSpace(ctx, sp)
}
func (s *PostgresSpaceStore) UpdateParticipantMode(ctx context.Context, spaceID, participantID string, mode protocol.ParticipantActivityMode) error {
	sp, e := s.GetSpace(ctx, spaceID)
	if e != nil {
		return e
	}
	if sp == nil {
		return fmt.Errorf("space %q not found", spaceID)
	}
	p, ok := sp.Participants[participantID]
	if !ok {
		return fmt.Errorf("participant %q not found", participantID)
	}
	p.Mode = mode
	p.UpdatedAt = time.Now().UTC()
	sp.Participants[participantID] = p
	return s.SaveSpace(ctx, sp)
}
func (s *PostgresSpaceStore) SaveEntity(ctx context.Context, table, id, spaceID string, v any, created time.Time) error {
	t, e := s.tenant(ctx)
	if e != nil {
		return e
	}
	b, e := encodeSpace(v)
	if e != nil {
		return e
	}
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO `+table+`(id,tenant_id,space_id,created_at,payload) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT(tenant_id,id) DO UPDATE SET payload=EXCLUDED.payload`, id, t, spaceID, created, b)
	return e
}
func (s *PostgresSpaceStore) CreateChannel(ctx context.Context, ch *protocol.Channel) error {
	if ch == nil || ch.ID == "" {
		return errors.New("channel id is required")
	}
	return s.SaveEntity(ctx, "collaboration_channels_pg", ch.ID, ch.SpaceID, ch, ch.CreatedAt)
}
func (s *PostgresSpaceStore) GetChannel(ctx context.Context, spaceID, id string) (*protocol.Channel, error) {
	t, e := s.tenant(ctx)
	if e != nil {
		return nil, e
	}
	var ch protocol.Channel
	e = decodeSpace(s.db.QueryRowContext(ctx, `SELECT payload FROM collaboration_channels_pg WHERE tenant_id=$1 AND space_id=$2 AND id=$3`, t, spaceID, id), &ch)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, &protocol.ChannelNotFoundError{Channel: id, SpaceID: spaceID}
	}
	if e != nil {
		return nil, e
	}
	return &ch, nil
}
func (s *PostgresSpaceStore) ListChannels(ctx context.Context, spaceID string) ([]protocol.Channel, error) {
	return querySpace[protocol.Channel](ctx, s.db, `SELECT payload FROM collaboration_channels_pg WHERE tenant_id=$1 AND space_id=$2 ORDER BY created_at`, spaceID)
}
func (s *PostgresSpaceStore) CreateThread(ctx context.Context, th *protocol.Thread) error {
	if th == nil || th.ID == "" {
		return errors.New("thread id is required")
	}
	return s.SaveEntity(ctx, "collaboration_threads_pg", th.ID, th.SpaceID, th, th.CreatedAt)
}
func (s *PostgresSpaceStore) GetThread(ctx context.Context, id string) (*protocol.Thread, error) {
	t, e := s.tenant(ctx)
	if e != nil {
		return nil, e
	}
	var th protocol.Thread
	e = decodeSpace(s.db.QueryRowContext(ctx, `SELECT payload FROM collaboration_threads_pg WHERE tenant_id=$1 AND id=$2`, t, id), &th)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, &protocol.ThreadNotFoundError{ThreadID: id}
	}
	if e != nil {
		return nil, e
	}
	return &th, nil
}
func (s *PostgresSpaceStore) ListThreads(ctx context.Context, spaceID, channelID string) ([]protocol.Thread, error) {
	t, e := s.tenant(ctx)
	if e != nil {
		return nil, e
	}
	q := `SELECT payload FROM collaboration_threads_pg WHERE tenant_id=$1 AND space_id=$2`
	args := []any{t, spaceID}
	if channelID != "" {
		q += ` AND channel_id=$3`
		args = append(args, channelID)
	}
	q += ` ORDER BY created_at`
	rows, e := s.db.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []protocol.Thread
	for rows.Next() {
		var v protocol.Thread
		if e := decodeSpace(rows, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *PostgresSpaceStore) SaveSubscription(ctx context.Context, sub *protocol.Subscription) error {
	if sub == nil || sub.ID == "" {
		return errors.New("subscription id is required")
	}
	return s.SaveEntity(ctx, "collaboration_subscriptions_pg", sub.ID, sub.SpaceID, sub, sub.CreatedAt)
}
func (s *PostgresSpaceStore) GetSubscriptions(ctx context.Context, spaceID string) ([]protocol.Subscription, error) {
	return querySpace[protocol.Subscription](ctx, s.db, `SELECT payload FROM collaboration_subscriptions_pg WHERE tenant_id=$1 AND space_id=$2 ORDER BY created_at`, spaceID)
}
func (s *PostgresSpaceStore) GetParticipantSubscriptions(ctx context.Context, spaceID, pid string) ([]protocol.Subscription, error) {
	t, e := s.tenant(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT payload FROM collaboration_subscriptions_pg WHERE tenant_id=$1 AND space_id=$2 AND participant_id=$3 ORDER BY created_at`, t, spaceID, pid)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []protocol.Subscription
	for rows.Next() {
		var v protocol.Subscription
		if e := decodeSpace(rows, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *PostgresSpaceStore) DeleteSubscription(ctx context.Context, spaceID, id string) error {
	t, e := s.tenant(ctx)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `DELETE FROM collaboration_subscriptions_pg WHERE tenant_id=$1 AND space_id=$2 AND id=$3`, t, spaceID, id)
	return e
}
func (s *PostgresSpaceStore) UpdateParticipantCursor(ctx context.Context, c *protocol.ParticipantCursor) error {
	if c == nil {
		return errors.New("cursor is required")
	}
	t, e := s.tenant(ctx)
	if e != nil {
		return e
	}
	c.UpdatedAt = time.Now().UTC()
	b, e := encodeSpace(c)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO collaboration_cursors_pg(tenant_id,space_id,participant_id,updated_at,payload) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT(tenant_id,space_id,participant_id) DO UPDATE SET updated_at=EXCLUDED.updated_at,payload=EXCLUDED.payload`, t, c.SpaceID, c.ParticipantID, c.UpdatedAt, b)
	return e
}
func (s *PostgresSpaceStore) GetParticipantCursor(ctx context.Context, spaceID, pid string) (*protocol.ParticipantCursor, error) {
	t, e := s.tenant(ctx)
	if e != nil {
		return nil, e
	}
	var c protocol.ParticipantCursor
	e = decodeSpace(s.db.QueryRowContext(ctx, `SELECT payload FROM collaboration_cursors_pg WHERE tenant_id=$1 AND space_id=$2 AND participant_id=$3`, t, spaceID, pid), &c)
	if errors.Is(e, sql.ErrNoRows) {
		return &protocol.ParticipantCursor{SpaceID: spaceID, ParticipantID: pid, Mode: protocol.ParticipantModeActive, UpdatedAt: time.Now().UTC()}, nil
	}
	if e != nil {
		return nil, e
	}
	return &c, nil
}
func (s *PostgresSpaceStore) SaveSummary(ctx context.Context, spaceID, targetType, targetID, text string, source []string) error {
	t, e := s.tenant(ctx)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO collaboration_summaries_pg(tenant_id,space_id,target_id,summary_text,created_at) VALUES($1,$2,$3,$4,$5)`, t, spaceID, targetID, text, time.Now().UTC())
	return e
}
func (s *PostgresSpaceStore) GetSummaries(ctx context.Context, spaceID, targetID string) ([]string, error) {
	t, e := s.tenant(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT summary_text FROM collaboration_summaries_pg WHERE tenant_id=$1 AND space_id=$2 AND target_id=$3 ORDER BY created_at`, t, spaceID, targetID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if e := rows.Scan(&v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func querySpace[T any](ctx context.Context, db *sql.DB, q, parent string) ([]T, error) {
	t, e := tenantID(ctx)
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
		if e := decodeSpace(rows, &v); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
