package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "github.com/lib/pq"
)

// OpenPostgresRepositories opens the production PostgreSQL control-plane
// pool, verifies connectivity, and applies every repository-slice migration.
// The caller owns the returned *sql.DB and must close it during shutdown.
func OpenPostgresRepositories(ctx context.Context, dsn string) (*PostgresRepositorySet, *sql.DB, error) {
	if dsn == "" {
		return nil, nil, errors.New("postgres DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("ping postgres: %w", err)
	}
	repositories, err := NewPostgresRepositorySet(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	if err := repositories.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("migrate postgres repositories: %w", err)
	}
	return repositories, db, nil
}
