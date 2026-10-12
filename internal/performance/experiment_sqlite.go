package performance

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/domehahn/harnessmesh/internal/store"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// StoreBackedExperimentStore is the durable Phase-10 adapter. It reuses the
// existing Store/SQLite transaction boundary; no second database is created.
type StoreBackedExperimentStore struct{ Store store.Store }

func NewStoreBackedExperimentStore(s store.Store) *StoreBackedExperimentStore {
	return &StoreBackedExperimentStore{Store: s}
}
func (s *StoreBackedExperimentStore) Create(ctx context.Context, e Experiment) error {
	if err := e.Definition.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err = s.Store.GetExperiment(ctx, string(e.Definition.ID)); err == nil {
		return ErrExperimentConflict
	}
	return s.Store.SaveExperiment(ctx, string(e.Definition.ID), e.Definition.Tenant, string(e.State), e.Definition.MaxExecutions, e.Definition.MaxConcurrent, e.Reserved, e.Consumed, e.Released, e.Executed, string(b), e.UpdatedAt)
}
func (s *StoreBackedExperimentStore) Get(ctx context.Context, id ExperimentID) (Experiment, error) {
	r, err := s.Store.GetExperiment(ctx, string(id))
	if err != nil {
		return Experiment{}, ErrExperimentNotFound
	}
	var e Experiment
	if err = json.Unmarshal([]byte(r.PayloadJSON), &e); err != nil {
		return Experiment{}, fmt.Errorf("decode experiment: %w", err)
	}
	e.State = ExperimentState(r.State)
	e.Reserved = r.Reserved
	e.Consumed = r.Consumed
	e.Released = r.Released
	e.Executed = r.Executed
	e.UpdatedAt = r.UpdatedAt
	return e, nil
}
func (s *StoreBackedExperimentStore) Save(ctx context.Context, e Experiment) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.Store.SaveExperiment(ctx, string(e.Definition.ID), e.Definition.Tenant, string(e.State), e.Definition.MaxExecutions, e.Definition.MaxConcurrent, e.Reserved, e.Consumed, e.Released, e.Executed, string(b), e.UpdatedAt)
}
func (s *StoreBackedExperimentStore) Reserve(ctx context.Context, id ExperimentID, d supervisor.DecisionID, now time.Time) (bool, error) {
	return s.Store.ReserveExperiment(ctx, string(id), string(d), now)
}
func (s *StoreBackedExperimentStore) Release(ctx context.Context, id ExperimentID, d supervisor.DecisionID, now time.Time) error {
	return s.Store.ReleaseExperiment(ctx, string(id), string(d), now)
}
func (s *StoreBackedExperimentStore) Consume(ctx context.Context, id ExperimentID, d supervisor.DecisionID, now time.Time) error {
	return s.Store.ConsumeExperiment(ctx, string(id), string(d), now)
}
func (s *StoreBackedExperimentStore) SaveAdmission(ctx context.Context, a Admission) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return s.Store.SaveExperimentAdmission(ctx, string(a.ExperimentID), string(a.DecisionID), string(b), a.CreatedAt)
}
func (s *StoreBackedExperimentStore) GetAdmission(ctx context.Context, id ExperimentID, d supervisor.DecisionID) (Admission, error) {
	b, err := s.Store.GetExperimentAdmission(ctx, string(id), string(d))
	if err != nil {
		return Admission{}, ErrExperimentNotFound
	}
	var a Admission
	if err = json.Unmarshal([]byte(b), &a); err != nil {
		return Admission{}, err
	}
	return a, nil
}
func (s *StoreBackedExperimentStore) Audit(ctx context.Context, a ExperimentAudit) error {
	return s.Store.SaveExperimentAudit(ctx, string(a.ExperimentID), string(a.DecisionID), a.Event, a.Reason, a.At)
}
func (s *StoreBackedExperimentStore) UpdateSummary(ctx context.Context, id ExperimentID, mutate func(*Experiment)) error {
	return s.Store.UpdateExperiment(ctx, string(id), func(r store.ExperimentRecord) (store.ExperimentRecord, error) {
		var e Experiment
		if err := json.Unmarshal([]byte(r.PayloadJSON), &e); err != nil {
			return r, fmt.Errorf("decode experiment: %w", err)
		}
		// The SQL columns, just read inside THIS transaction, are
		// authoritative for the budget ledger - never the JSON blob's
		// own (possibly stale) copies of these same fields.
		e.State = ExperimentState(r.State)
		e.Reserved = r.Reserved
		e.Consumed = r.Consumed
		e.Released = r.Released
		e.Executed = r.Executed
		mutate(&e)
		b, err := json.Marshal(e)
		if err != nil {
			return r, err
		}
		r.State = string(e.State)
		r.Reserved = e.Reserved
		r.Consumed = e.Consumed
		r.Released = e.Released
		r.Executed = e.Executed
		r.PayloadJSON = string(b)
		r.UpdatedAt = e.UpdatedAt
		return r, nil
	})
}

var _ ExperimentStore = (*StoreBackedExperimentStore)(nil)
