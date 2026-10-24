package store

import (
	"context"
	"errors"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

// PostgresStore composes the PostgreSQL repository slices into the complete
// Store contract consumed by the collaboration engine. The individual slices
// remain independently testable, while deployments get one persistence seam.
// The SQL pool is owned by OpenPostgresRepositories and must be closed by the
// caller that opened it.
type PostgresStore struct {
	*PostgresSaaSStore
	*PostgresJobStore
	*PostgresSessionStore
	*PostgresCollaborationStore
	*PostgresGovernanceStore
	*PostgresMeshCommitStore
	*PostgresExperimentStore
	*PostgresSpaceStore
}

func NewPostgresStore(repositories *PostgresRepositorySet) (*PostgresStore, error) {
	if repositories == nil || repositories.SaaS == nil || repositories.Sessions == nil || repositories.Collaboration == nil || repositories.Governance == nil || repositories.MeshCommit == nil || repositories.Experiments == nil || repositories.Spaces == nil {
		return nil, errors.New("postgres repository set is incomplete")
	}
	return &PostgresStore{PostgresSaaSStore: repositories.SaaS, PostgresJobStore: &PostgresJobStore{db: repositories.SaaS.db}, PostgresSessionStore: repositories.Sessions, PostgresCollaborationStore: repositories.Collaboration, PostgresGovernanceStore: repositories.Governance, PostgresMeshCommitStore: repositories.MeshCommit, PostgresExperimentStore: repositories.Experiments, PostgresSpaceStore: repositories.Spaces}, nil
}

func (s *PostgresStore) Close() error { return nil }

// Explicit forwarding resolves the overlapping job and budget methods that
// are intentionally present in more than one narrow repository interface.
func (s *PostgresStore) CreateJob(ctx context.Context, j *DurableJob) error {
	return s.PostgresJobStore.CreateJob(ctx, j)
}
func (s *PostgresStore) GetJob(ctx context.Context, id string) (*DurableJob, error) {
	return s.PostgresJobStore.GetJob(ctx, id)
}
func (s *PostgresStore) ListJobs(ctx context.Context, tenant, status string, limit int) ([]DurableJob, error) {
	return s.PostgresJobStore.ListJobs(ctx, tenant, status, limit)
}
func (s *PostgresStore) ClaimJobs(ctx context.Context, now time.Time, owner string, limit int, lease time.Duration) ([]DurableJob, error) {
	return s.PostgresJobStore.ClaimJobs(ctx, now, owner, limit, lease)
}
func (s *PostgresStore) UpdateJob(ctx context.Context, id, status string, nextRun time.Time, lastError string) error {
	return s.PostgresJobStore.UpdateJob(ctx, id, status, nextRun, lastError)
}
func (s *PostgresStore) AcquireLease(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	return s.PostgresJobStore.AcquireLease(ctx, name, owner, ttl)
}
func (s *PostgresStore) ReleaseLease(ctx context.Context, name, owner string) error {
	return s.PostgresJobStore.ReleaseLease(ctx, name, owner)
}
func (s *PostgresStore) AppendJobEvent(ctx context.Context, event *JobEvent) error {
	return s.PostgresJobStore.AppendJobEvent(ctx, event)
}
func (s *PostgresStore) ListJobEvents(ctx context.Context, jobID string, limit int) ([]JobEvent, error) {
	return s.PostgresJobStore.ListJobEvents(ctx, jobID, limit)
}
func (s *PostgresStore) UpdateBudget(ctx context.Context, id string, budget protocol.BudgetStatus) error {
	return s.PostgresSessionStore.UpdateBudget(ctx, id, budget)
}

var _ Store = (*PostgresStore)(nil)
var _ JobEventStore = (*PostgresStore)(nil)
var _ ApprovalAuditStore = (*PostgresStore)(nil)
var _ IdentityStore = (*PostgresStore)(nil)
var _ DirectoryStore = (*PostgresStore)(nil)
var _ SecretStore = (*PostgresStore)(nil)
