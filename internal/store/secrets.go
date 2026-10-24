package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *SQLiteStore) PutSecret(ctx context.Context, secret *SecretRecord) error {
	if secret == nil || secret.ID == "" || secret.TenantID == "" || secret.Name == "" || len(secret.Ciphertext) == 0 || len(secret.Nonce) == 0 {
		return errors.New("secret id, tenant, name, ciphertext and nonce are required")
	}
	if secret.CreatedAt.IsZero() {
		secret.CreatedAt = time.Now().UTC()
	}
	secret.UpdatedAt = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenant_secrets(id,tenant_id,name,ciphertext,nonce,metadata,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(tenant_id,name) DO UPDATE SET id=excluded.id,ciphertext=excluded.ciphertext,nonce=excluded.nonce,metadata=excluded.metadata,updated_at=excluded.updated_at`, secret.ID, secret.TenantID, secret.Name, secret.Ciphertext, secret.Nonce, secret.Metadata, secret.CreatedAt, secret.UpdatedAt)
	return err
}

func (s *SQLiteStore) ListSecrets(ctx context.Context, tenantID string) ([]SecretRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id,tenant_id,name,metadata,created_at,updated_at FROM tenant_secrets WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretRecord
	for rows.Next() {
		var item SecretRecord
		if err := rows.Scan(&item.ID, &item.TenantID, &item.Name, &item.Metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetSecret(ctx context.Context, tenantID, name string) (*SecretRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var item SecretRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,tenant_id,name,ciphertext,nonce,metadata,created_at,updated_at FROM tenant_secrets WHERE tenant_id=? AND name=?`, tenantID, name).Scan(&item.ID, &item.TenantID, &item.Name, &item.Ciphertext, &item.Nonce, &item.Metadata, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *SQLiteStore) DeleteSecret(ctx context.Context, tenantID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM tenant_secrets WHERE tenant_id=? AND name=?`, tenantID, name)
	return err
}

func (s *PostgresSaaSStore) PutSecret(ctx context.Context, secret *SecretRecord) error {
	if secret == nil || secret.ID == "" || secret.TenantID == "" || secret.Name == "" || len(secret.Ciphertext) == 0 || len(secret.Nonce) == 0 {
		return errors.New("secret id, tenant, name, ciphertext and nonce are required")
	}
	if secret.CreatedAt.IsZero() {
		secret.CreatedAt = time.Now().UTC()
	}
	secret.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO tenant_secrets(id,tenant_id,name,ciphertext,nonce,metadata,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(tenant_id,name) DO UPDATE SET id=EXCLUDED.id,ciphertext=EXCLUDED.ciphertext,nonce=EXCLUDED.nonce,metadata=EXCLUDED.metadata,updated_at=EXCLUDED.updated_at`, secret.ID, secret.TenantID, secret.Name, secret.Ciphertext, secret.Nonce, secret.Metadata, secret.CreatedAt, secret.UpdatedAt)
	return err
}
func (s *PostgresSaaSStore) ListSecrets(ctx context.Context, tenantID string) ([]SecretRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,tenant_id,name,metadata,created_at,updated_at FROM tenant_secrets WHERE tenant_id=$1 ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretRecord
	for rows.Next() {
		var item SecretRecord
		if err := rows.Scan(&item.ID, &item.TenantID, &item.Name, &item.Metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *PostgresSaaSStore) GetSecret(ctx context.Context, tenantID, name string) (*SecretRecord, error) {
	var item SecretRecord
	err := s.db.QueryRowContext(ctx, `SELECT id,tenant_id,name,ciphertext,nonce,metadata,created_at,updated_at FROM tenant_secrets WHERE tenant_id=$1 AND name=$2`, tenantID, name).Scan(&item.ID, &item.TenantID, &item.Name, &item.Ciphertext, &item.Nonce, &item.Metadata, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}
func (s *PostgresSaaSStore) DeleteSecret(ctx context.Context, tenantID, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tenant_secrets WHERE tenant_id=$1 AND name=$2`, tenantID, name)
	return err
}

var _ SecretStore = (*SQLiteStore)(nil)
var _ SecretStore = (*PostgresSaaSStore)(nil)
