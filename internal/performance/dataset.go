package performance

import "context"

// DecisionDataset is the deterministic, dependency-free data-access
// boundary this phase prepares for FUTURE routing-decision use - no ML
// dependency, no Python/pandas service, just a clean read interface over
// PerformanceStore. Nothing in this package or anywhere else calls
// DecisionDataset to make or alter a routing decision; it exists so a
// later phase has one clean seam to build that on, rather than reaching
// into PerformanceStore's persistence details directly.
type DecisionDataset interface {
	Observations(ctx context.Context, filter ObservationFilter) ([]PerformanceObservation, error)
}

// StoreDataset adapts any PerformanceStore to DecisionDataset.
type StoreDataset struct {
	Store PerformanceStore
}

// NewStoreDataset constructs a StoreDataset over store.
func NewStoreDataset(store PerformanceStore) *StoreDataset {
	return &StoreDataset{Store: store}
}

func (d *StoreDataset) Observations(ctx context.Context, filter ObservationFilter) ([]PerformanceObservation, error) {
	return d.Store.Query(ctx, filter)
}

var _ DecisionDataset = (*StoreDataset)(nil)
