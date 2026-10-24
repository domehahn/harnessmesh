package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

func (s *PostgresCollaborationStore) GetFinding(ctx context.Context, sessionID, findingID string) (*protocol.FindingPayload, error) {
	tid, err := tenantID(ctx)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = s.db.QueryRowContext(ctx, `SELECT payload_json FROM collaboration_findings WHERE tenant_id=$1 AND session_id=$2 AND id=$3`, tid, sessionID, findingID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f protocol.FindingPayload
	err = json.Unmarshal(raw, &f)
	return &f, err
}
func (s *PostgresCollaborationStore) UpdateFindingStatus(ctx context.Context, sessionID, findingID string, status protocol.FindingStatus) error {
	tid, err := tenantID(ctx)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE collaboration_findings SET payload_json=jsonb_set(payload_json,'{status}',to_jsonb($1::text),true) WHERE tenant_id=$2 AND session_id=$3 AND id=$4`, string(status), tid, sessionID, findingID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("finding not found")
	}
	return nil
}
func (s *PostgresCollaborationStore) GetEvidenceByFinding(ctx context.Context, sessionID, findingID string) ([]protocol.EvidencePayload, error) {
	tid, e := tenantID(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT payload_json FROM collaboration_evidence WHERE tenant_id=$1 AND session_id=$2 AND payload_json->>'finding_id'=$3 ORDER BY created_at`, tid, sessionID, findingID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []protocol.EvidencePayload
	for rows.Next() {
		var raw []byte
		var v protocol.EvidencePayload
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
func (s *PostgresCollaborationStore) SaveChallenge(ctx context.Context, sessionID string, ch *protocol.ChallengePayload) error {
	if ch == nil || ch.ID == "" {
		return errors.New("challenge id is required")
	}
	return s.auxSave(ctx, "challenge", sessionID, ch.ID, ch, ch.CreatedAt)
}
func (s *PostgresCollaborationStore) GetChallenges(ctx context.Context, sessionID string) ([]protocol.ChallengePayload, error) {
	var out []protocol.ChallengePayload
	return out, s.auxList(ctx, "challenge", sessionID, &out)
}
func (s *PostgresCollaborationStore) SaveResolution(ctx context.Context, sessionID string, r *protocol.ResolutionPayload) error {
	if r == nil || r.ID == "" {
		return errors.New("resolution id is required")
	}
	return s.auxSave(ctx, "resolution", sessionID, r.ID, r, r.Timestamp)
}
func (s *PostgresCollaborationStore) GetResolutions(ctx context.Context, sessionID string) ([]protocol.ResolutionPayload, error) {
	var out []protocol.ResolutionPayload
	return out, s.auxList(ctx, "resolution", sessionID, &out)
}

func (s *PostgresCollaborationStore) SaveIdempotency(ctx context.Context, key, sessionID, resultJSON string) error {
	return s.auxSave(ctx, "idempotency", sessionID, key, json.RawMessage(resultJSON), time.Now().UTC())
}
func (s *PostgresCollaborationStore) GetIdempotency(ctx context.Context, sessionID, key string) (string, error) {
	tid, e := tenantID(ctx)
	if e != nil {
		return "", e
	}
	var raw []byte
	e = s.db.QueryRowContext(ctx, `SELECT payload_json FROM collaboration_aux WHERE kind='idempotency' AND tenant_id=$1 AND session_id=$2 AND id=$3`, tid, sessionID, key).Scan(&raw)
	return string(raw), e
}
