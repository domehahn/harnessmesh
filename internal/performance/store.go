package performance

import (
	"context"
	"sync"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// PerformanceStore is the persistence boundary for PerformanceObservation
// records. Save is idempotent: saving the exact same observation twice
// (same ID, same content) is a safe no-op; saving a DIFFERENT
// observation under an existing ID that is not a recognized
// finalization update (see MemoryPerformanceStore.Save) is a
// DuplicateObservation - a duplicate event must never inflate metrics.
type PerformanceStore interface {
	Save(ctx context.Context, obs PerformanceObservation) error
	Get(ctx context.Context, id ObservationID) (PerformanceObservation, error)
	Query(ctx context.Context, filter ObservationFilter) ([]PerformanceObservation, error)
}

// ObservationFilter scopes a query. Tenant is REQUIRED unless AllTenants
// is explicitly set - fail closed on an unscoped query, mirroring ADR
// 0002 Phase 2B's tenant discipline: absence of a tenant is never
// treated as "any tenant."
type ObservationFilter struct {
	Tenant        string
	AllTenants    bool
	TaskClass     TaskClass          // empty = any
	AgentID       supervisor.AgentID // empty = any
	TaskID        supervisor.TaskID  // empty = any
	FinalizedOnly bool
}

func (f ObservationFilter) validate() error {
	if !f.AllTenants && f.Tenant == "" {
		return &CrossTenantQueryDenied{Reason: "no tenant specified and AllTenants not explicitly set"}
	}
	return nil
}

func (f ObservationFilter) matches(obs PerformanceObservation) bool {
	if !f.AllTenants && obs.Tenant != f.Tenant {
		return false
	}
	if f.TaskClass != "" && obs.TaskClass != f.TaskClass {
		return false
	}
	if f.TaskID != "" && obs.TaskID != f.TaskID {
		return false
	}
	if f.AgentID != "" && obs.AgentID != f.AgentID {
		return false
	}
	if f.FinalizedOnly && obs.Status != ObservationFinalized {
		return false
	}
	return true
}

// MemoryPerformanceStore is a race-safe, deterministic, in-memory
// PerformanceStore for tests and for proving this package's idempotency/
// tenant-isolation guarantees independent of any real persistence layer -
// the same role MemoryEvidenceStore played in ADR 0004.
type MemoryPerformanceStore struct {
	mu           sync.Mutex
	observations map[ObservationID]PerformanceObservation
}

// NewMemoryPerformanceStore constructs an empty MemoryPerformanceStore.
func NewMemoryPerformanceStore() *MemoryPerformanceStore {
	return &MemoryPerformanceStore{observations: map[ObservationID]PerformanceObservation{}}
}

// Save is idempotent: an identical replay (same ID, same immutable
// identity fields: TaskID/ExecutionID/TaskClass/AgentID/ExecutionOutcome)
// is a safe no-op. A call with the SAME ID but a different
// Status/QualityOutcome/Metrics/ReviewerAgentIDs/ReviewGroupID is treated
// as a legitimate finalization update (the documented "mutable
// projection" - see ADR 0005) and replaces the stored record. A call
// with the same ID but DIFFERENT immutable identity fields is rejected
// as a DuplicateObservation - that would mean two unrelated executions
// collided on the same idempotency key, which must never silently merge.
func (s *MemoryPerformanceStore) Save(ctx context.Context, obs PerformanceObservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.observations[obs.ID]
	if ok {
		if existing.TaskID != obs.TaskID || existing.ExecutionID != obs.ExecutionID ||
			existing.TaskClass != obs.TaskClass || existing.AgentID != obs.AgentID ||
			existing.Tenant != obs.Tenant {
			return &DuplicateObservation{ID: obs.ID}
		}
		// Never allow a finalized record to be silently reverted to
		// pending by a stale/older write racing in after finalization -
		// history is not rewritten backwards.
		if existing.Status == ObservationFinalized && obs.Status == ObservationPending {
			return nil
		}
	}
	s.observations[obs.ID] = obs
	return nil
}

func (s *MemoryPerformanceStore) Get(ctx context.Context, id ObservationID) (PerformanceObservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	obs, ok := s.observations[id]
	if !ok {
		return PerformanceObservation{}, &ObservationNotFound{ID: id}
	}
	return obs, nil
}

func (s *MemoryPerformanceStore) Query(ctx context.Context, filter ObservationFilter) ([]PerformanceObservation, error) {
	if err := filter.validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PerformanceObservation
	for _, obs := range s.observations {
		if filter.matches(obs) {
			out = append(out, obs)
		}
	}
	return out, nil
}

var _ PerformanceStore = (*MemoryPerformanceStore)(nil)
