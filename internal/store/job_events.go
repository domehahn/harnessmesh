package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func normalizeJobEvent(event *JobEvent) error {
	if event == nil || event.JobID == "" || event.Tenant == "" || event.Type == "" {
		return errors.New("job event job, tenant, and type are required")
	}
	if event.ID == "" {
		event.ID = fmt.Sprintf("jobevt_%d", time.Now().UTC().UnixNano())
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if len(event.Payload) == 0 {
		event.Payload = json.RawMessage(`{}`)
	}
	return nil
}

func (s *SQLiteStore) AppendJobEvent(ctx context.Context, event *JobEvent) error {
	if err := normalizeJobEvent(event); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_events(id,job_id,tenant,type,message,payload_json,created_at) VALUES(?,?,?,?,?,?,?)`, event.ID, event.JobID, event.Tenant, event.Type, event.Message, string(event.Payload), event.CreatedAt)
	return err
}

func (s *SQLiteStore) ListJobEvents(ctx context.Context, jobID string, limit int) ([]JobEvent, error) {
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id,job_id,tenant,type,message,payload_json,created_at FROM job_events WHERE job_id=? ORDER BY created_at ASC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	return scanJobEvents(rows)
}

func scanJobEvents(rows *sql.Rows) ([]JobEvent, error) {
	defer rows.Close()
	var events []JobEvent
	for rows.Next() {
		var event JobEvent
		var payload string
		if err := rows.Scan(&event.ID, &event.JobID, &event.Tenant, &event.Type, &event.Message, &payload, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.Payload = json.RawMessage(payload)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *PostgresJobStore) AppendJobEvent(ctx context.Context, event *JobEvent) error {
	if err := normalizeJobEvent(event); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_events(id,job_id,tenant,type,message,payload_json,created_at) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7)`, event.ID, event.JobID, event.Tenant, event.Type, event.Message, string(event.Payload), event.CreatedAt)
	return err
}

func (s *PostgresJobStore) ListJobEvents(ctx context.Context, jobID string, limit int) ([]JobEvent, error) {
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,job_id,tenant,type,message,payload_json,created_at FROM job_events WHERE job_id=$1 ORDER BY created_at ASC LIMIT $2`, jobID, limit)
	if err != nil {
		return nil, err
	}
	return scanJobEvents(rows)
}

func (s *PostgresSaaSStore) AppendJobEvent(ctx context.Context, event *JobEvent) error {
	if err := normalizeJobEvent(event); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_events(id,job_id,tenant,type,message,payload_json,created_at) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7)`, event.ID, event.JobID, event.Tenant, event.Type, event.Message, string(event.Payload), event.CreatedAt)
	return err
}

func (s *PostgresSaaSStore) ListJobEvents(ctx context.Context, jobID string, limit int) ([]JobEvent, error) {
	if jobID == "" {
		return nil, errors.New("job id is required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,job_id,tenant,type,message,payload_json,created_at FROM job_events WHERE job_id=$1 ORDER BY created_at ASC LIMIT $2`, jobID, limit)
	if err != nil {
		return nil, err
	}
	return scanJobEvents(rows)
}
