package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

func (s *PostgresCollaborationStore) RecordEventDelivery(ctx context.Context, d *protocol.EventDelivery) error {
	if d == nil || d.ID == "" {
		return errors.New("event delivery id is required")
	}
	t, err := tenantID(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO event_deliveries_pg(id,tenant_id,event_id,participant_id,payload_json) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT(tenant_id,id) DO UPDATE SET payload_json=EXCLUDED.payload_json`, d.ID, t, d.EventID, d.ParticipantID, string(raw))
	return err
}
func (s *PostgresCollaborationStore) GetEventDelivery(ctx context.Context, eventID, participantID string) (*protocol.EventDelivery, error) {
	t, e := tenantID(ctx)
	if e != nil {
		return nil, e
	}
	var raw []byte
	e = s.db.QueryRowContext(ctx, `SELECT payload_json FROM event_deliveries_pg WHERE tenant_id=$1 AND event_id=$2 AND participant_id=$3`, t, eventID, participantID).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var d protocol.EventDelivery
	e = json.Unmarshal(raw, &d)
	return &d, e
}
func (s *PostgresCollaborationStore) UpdateEventDeliveryStatus(ctx context.Context, id string, status protocol.DeliveryStatus, resultMsgID, skipReason string) error {
	t, e := tenantID(ctx)
	if e != nil {
		return e
	}
	now := time.Now().UTC()
	_, e = s.db.ExecContext(ctx, `UPDATE event_deliveries_pg SET payload_json=jsonb_set(jsonb_set(jsonb_set(jsonb_set(payload_json,'{status}',to_jsonb($1::text),true),'{result_message_id}',to_jsonb($2::text),true),'{skip_reason}',to_jsonb($3::text),true),'{completed_at}',CASE WHEN $1 IN ('pending','processing') THEN 'null'::jsonb ELSE to_jsonb($4::timestamptz) END,true) WHERE tenant_id=$5 AND id=$6`, string(status), resultMsgID, skipReason, now, t, id)
	return e
}
func (s *PostgresCollaborationStore) GetSpaceMessages(ctx context.Context, spaceID, channelID, threadID string, limit int) ([]*protocol.PeerEnvelope, error) {
	t, e := tenantID(ctx)
	if e != nil {
		return nil, e
	}
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	q := `SELECT payload_json FROM collaboration_messages WHERE tenant_id=$1 AND (session_id=$2 OR payload_json->>'space_id'=$2)`
	args := []any{t, spaceID}
	n := 3
	if channelID != "" {
		q += ` AND payload_json->>'channel_id'=$3`
		args = append(args, channelID)
		n++
	}
	if threadID != "" {
		q += ` AND payload_json->>'thread_id'=$` + string(rune('0'+n))
		args = append(args, threadID)
		n++
	}
	q += ` ORDER BY created_at LIMIT $` + string(rune('0'+n))
	args = append(args, limit)
	rows, e := s.db.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []*protocol.PeerEnvelope
	for rows.Next() {
		var raw []byte
		var v protocol.PeerEnvelope
		if e := rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e := json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}
