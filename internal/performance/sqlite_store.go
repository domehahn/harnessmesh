package performance

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/domehahn/harnessmesh/internal/store"
)

// StoreBackedPerformanceStore adapts the repository's existing
// persistent store.Store to PerformanceStore, per ADR 0002 Phase 6's
// explicit instruction to integrate with the existing store abstraction
// rather than build a new custom database layer. The full
// PerformanceObservation is persisted as one JSON payload (store.Store's
// `performance_observations` table, migration version 13) - store.Store
// never imports this package's types, preserving the established
// one-way dependency direction.
type StoreBackedPerformanceStore struct {
	Store store.Store
}

// NewStoreBackedPerformanceStore constructs the adapter over a real
// store.Store.
func NewStoreBackedPerformanceStore(s store.Store) *StoreBackedPerformanceStore {
	return &StoreBackedPerformanceStore{Store: s}
}

func (s *StoreBackedPerformanceStore) Save(ctx context.Context, obs PerformanceObservation) error {
	// Idempotency/anti-rewrite discipline is enforced here, before
	// touching the store, exactly mirroring MemoryPerformanceStore -
	// callers get identical guarantees from either implementation.
	existingJSON, err := s.Store.GetPerformanceObservation(ctx, string(obs.ID))
	if err == nil {
		var existing PerformanceObservation
		if decodeErr := json.Unmarshal([]byte(existingJSON), &existing); decodeErr == nil {
			if existing.TaskID != obs.TaskID || existing.ExecutionID != obs.ExecutionID ||
				existing.TaskClass != obs.TaskClass || existing.AgentID != obs.AgentID ||
				existing.Tenant != obs.Tenant {
				return &DuplicateObservation{ID: obs.ID}
			}
			if existing.Status == ObservationFinalized && obs.Status == ObservationPending {
				return nil
			}
		}
	}

	payload, err := json.Marshal(obs)
	if err != nil {
		return fmt.Errorf("encode performance observation: %w", err)
	}
	return s.Store.SavePerformanceObservation(ctx, string(obs.ID), obs.Tenant, string(obs.TaskClass), string(obs.AgentID), string(obs.Status), obs.CreatedAt, string(payload))
}

func (s *StoreBackedPerformanceStore) Get(ctx context.Context, id ObservationID) (PerformanceObservation, error) {
	payload, err := s.Store.GetPerformanceObservation(ctx, string(id))
	if err != nil {
		return PerformanceObservation{}, &ObservationNotFound{ID: id}
	}
	var obs PerformanceObservation
	if err := json.Unmarshal([]byte(payload), &obs); err != nil {
		return PerformanceObservation{}, fmt.Errorf("decode performance observation %q: %w", id, err)
	}
	return obs, nil
}

func (s *StoreBackedPerformanceStore) Query(ctx context.Context, filter ObservationFilter) ([]PerformanceObservation, error) {
	if err := filter.validate(); err != nil {
		return nil, err
	}
	tenant := filter.Tenant
	if filter.AllTenants {
		tenant = ""
	}
	rows, err := s.Store.QueryPerformanceObservations(ctx, tenant, string(filter.TaskClass), string(filter.AgentID))
	if err != nil {
		return nil, err
	}
	out := make([]PerformanceObservation, 0, len(rows))
	for _, payload := range rows {
		var obs PerformanceObservation
		if err := json.Unmarshal([]byte(payload), &obs); err != nil {
			return nil, fmt.Errorf("decode performance observation row: %w", err)
		}
		if filter.FinalizedOnly && obs.Status != ObservationFinalized {
			continue
		}
		if filter.TaskID != "" && obs.TaskID != filter.TaskID {
			continue
		}
		out = append(out, obs)
	}
	return out, nil
}

var _ PerformanceStore = (*StoreBackedPerformanceStore)(nil)
