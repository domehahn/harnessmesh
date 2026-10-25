package store

import (
	"context"
	"database/sql"
)

func (s *SQLiteStore) ListMemberships(ctx context.Context, tenantID string) ([]MembershipRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id,subject,role,status,created_at FROM tenant_memberships WHERE tenant_id=? ORDER BY subject`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MembershipRecord
	for rows.Next() {
		var item MembershipRecord
		if err := rows.Scan(&item.TenantID, &item.Subject, &item.Role, &item.Status, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListMembershipsForSubject(ctx context.Context, subject string) ([]MembershipRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id,subject,role,status,created_at FROM tenant_memberships WHERE subject=? AND status='active' ORDER BY tenant_id`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MembershipRecord
	for rows.Next() {
		var item MembershipRecord
		if err := rows.Scan(&item.TenantID, &item.Subject, &item.Role, &item.Status, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListAPIKeys(ctx context.Context, tenantID string) ([]APIKeyRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id,tenant_id,subject,prefix,status,created_at,revoked_at FROM api_keys WHERE tenant_id=? ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKeyRecord
	for rows.Next() {
		var item APIKeyRecord
		var revoked sql.NullTime
		if err := rows.Scan(&item.ID, &item.TenantID, &item.Subject, &item.Prefix, &item.Status, &item.CreatedAt, &revoked); err != nil {
			return nil, err
		}
		if revoked.Valid {
			item.RevokedAt = &revoked.Time
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresSaaSStore) ListMemberships(ctx context.Context, tenantID string) ([]MembershipRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id,subject,role,status,created_at FROM tenant_memberships WHERE tenant_id=$1 ORDER BY subject`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MembershipRecord
	for rows.Next() {
		var item MembershipRecord
		if err := rows.Scan(&item.TenantID, &item.Subject, &item.Role, &item.Status, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresSaaSStore) ListMembershipsForSubject(ctx context.Context, subject string) ([]MembershipRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tenant_id,subject,role,status,created_at FROM tenant_memberships WHERE subject=$1 AND status='active' ORDER BY tenant_id`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MembershipRecord
	for rows.Next() {
		var item MembershipRecord
		if err := rows.Scan(&item.TenantID, &item.Subject, &item.Role, &item.Status, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresSaaSStore) ListAPIKeys(ctx context.Context, tenantID string) ([]APIKeyRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,tenant_id,subject,prefix,status,created_at,revoked_at FROM api_keys WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIKeyRecord
	for rows.Next() {
		var item APIKeyRecord
		var revoked sql.NullTime
		if err := rows.Scan(&item.ID, &item.TenantID, &item.Subject, &item.Prefix, &item.Status, &item.CreatedAt, &revoked); err != nil {
			return nil, err
		}
		if revoked.Valid {
			item.RevokedAt = &revoked.Time
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
