package store

import (
	"context"
	"database/sql"
	"errors"
)

// PostgresRepositorySet groups all currently migrated PostgreSQL slices. All
// repositories share the same connection pool and migration transaction
// boundary at deployment startup.
type PostgresRepositorySet struct {
	SaaS          *PostgresSaaSStore
	Sessions      *PostgresSessionStore
	Collaboration *PostgresCollaborationStore
	Governance    *PostgresGovernanceStore
	MeshCommit    *PostgresMeshCommitStore
	Experiments   *PostgresExperimentStore
	Spaces        *PostgresSpaceStore
}

func NewPostgresRepositorySet(db *sql.DB) (*PostgresRepositorySet, error) {
	if db == nil {
		return nil, errors.New("postgres database is required")
	}
	saas, err := NewPostgresSaaSStore(db)
	if err != nil {
		return nil, err
	}
	sessions, err := NewPostgresSessionStore(db)
	if err != nil {
		return nil, err
	}
	collaboration, err := NewPostgresCollaborationStore(db)
	if err != nil {
		return nil, err
	}
	governance, err := NewPostgresGovernanceStore(db)
	if err != nil {
		return nil, err
	}
	meshCommit, err := NewPostgresMeshCommitStore(db)
	if err != nil {
		return nil, err
	}
	experiments, err := NewPostgresExperimentStore(db)
	if err != nil {
		return nil, err
	}
	spaces, err := NewPostgresSpaceStore(db)
	if err != nil {
		return nil, err
	}
	return &PostgresRepositorySet{SaaS: saas, Sessions: sessions, Collaboration: collaboration, Governance: governance, MeshCommit: meshCommit, Experiments: experiments, Spaces: spaces}, nil
}

func (r *PostgresRepositorySet) Migrate(ctx context.Context) error {
	if r == nil || r.SaaS == nil || r.Sessions == nil || r.Collaboration == nil || r.Governance == nil || r.MeshCommit == nil || r.Experiments == nil || r.Spaces == nil {
		return errors.New("postgres repository set is incomplete")
	}
	if err := r.SaaS.Migrate(ctx); err != nil {
		return err
	}
	if err := r.Sessions.Migrate(ctx); err != nil {
		return err
	}
	if err := r.Collaboration.Migrate(ctx); err != nil {
		return err
	}
	if err := r.Governance.Migrate(ctx); err != nil {
		return err
	}
	if err := r.MeshCommit.Migrate(ctx); err != nil {
		return err
	}
	if err := r.Experiments.Migrate(ctx); err != nil {
		return err
	}
	return r.Spaces.Migrate(ctx)
}

// PostgresSaaSBackendDescriptor describes the production-ready portion of the
// PostgreSQL migration without pretending that the legacy full Store contract
// has already been replaced.
func PostgresSaaSBackendDescriptor() BackendDescriptor {
	return BackendDescriptor{Name: "postgres-saas-control-plane", Transactional: true, Distributed: true, ObjectArchive: false, SupportsReplay: true}
}
