package store

import (
	"context"
	"database/sql"
	"time"
)

func (s *SQLiteStore) LoadMarketplaceState(ctx context.Context) ([]byte, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT state_json FROM marketplace_state WHERE id=1`).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return raw, err
}

func (s *SQLiteStore) SaveMarketplaceState(ctx context.Context, raw []byte, updatedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO marketplace_state(id,state_json,updated_at) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET state_json=excluded.state_json,updated_at=excluded.updated_at`, raw, updatedAt)
	return err
}
