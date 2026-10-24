// Package jobs provides the durable execution loop used by event-driven and
// autonomous agent workflows. It is intentionally storage-backed: a process
// restart loses no queued work and multiple workers coordinate through leases.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/domehahn/harnessmesh/internal/store"
)

type Handler func(context.Context, store.DurableJob) error

// JobStore is the minimal persistence contract required by the durable
// runtime. SQLite and the PostgreSQL adapter both implement it.
type JobStore interface {
	CreateJob(context.Context, *store.DurableJob) error
	GetJob(context.Context, string) (*store.DurableJob, error)
	ClaimJobs(context.Context, time.Time, string, int, time.Duration) ([]store.DurableJob, error)
	UpdateJob(context.Context, string, string, time.Time, string) error
}

type LeaseStore interface {
	AcquireLease(context.Context, string, string, time.Duration) (bool, error)
	ReleaseLease(context.Context, string, string) error
}

type EventStore interface {
	AppendJobEvent(context.Context, *store.JobEvent) error
}

type Runtime struct {
	Store           JobStore
	Owner           string
	Lease           time.Duration
	MaxAttempts     int
	Backoff         time.Duration
	AuthorizeTenant func(string) bool
}

type WorkerConfig struct {
	PollInterval time.Duration
	Concurrency  int
	LeaderLease  string
	LeaderTTL    time.Duration
}

func (r Runtime) Submit(ctx context.Context, job *store.DurableJob) error {
	if r.Store == nil {
		return errors.New("job store is required")
	}
	if job == nil || job.Tenant == "" {
		return errors.New("job tenant is required")
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = r.MaxAttempts
	}
	if err := r.Store.CreateJob(ctx, job); err != nil {
		return err
	}
	r.recordEvent(ctx, job, "submitted", "job accepted by durable runtime", nil)
	return nil
}

// RunOnce claims and executes up to limit jobs. It returns the number claimed;
// individual handler failures are persisted and do not stop unrelated jobs.
func (r Runtime) RunOnce(ctx context.Context, limit int, handler Handler) (int, error) {
	if r.Store == nil || r.Owner == "" {
		return 0, errors.New("job store and owner are required")
	}
	if handler == nil {
		return 0, errors.New("job handler is required")
	}
	lease := r.Lease
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	jobs, err := r.Store.ClaimJobs(ctx, time.Now().UTC(), r.Owner, limit, lease)
	if err != nil {
		return 0, err
	}
	for _, job := range jobs {
		r.recordEvent(ctx, &job, "claimed", fmt.Sprintf("worker %s claimed attempt %d", r.Owner, job.Attempts), nil)
		if r.AuthorizeTenant != nil && !r.AuthorizeTenant(job.Tenant) {
			_ = r.Store.UpdateJob(ctx, job.ID, "dead", time.Now().UTC(), "tenant authorization denied")
			r.recordEvent(ctx, &job, "dead", "tenant authorization denied", nil)
			continue
		}
		err := handler(ctx, job)
		if err == nil {
			if updateErr := r.Store.UpdateJob(ctx, job.ID, "completed", time.Now().UTC(), ""); updateErr != nil {
				return len(jobs), updateErr
			}
			r.recordEvent(ctx, &job, "completed", "job handler completed successfully", nil)
			continue
		}
		next := time.Now().UTC().Add(r.retryDelay(job.Attempts))
		status := "retry"
		if job.Attempts >= job.MaxAttempts {
			status = "dead"
		}
		if updateErr := r.Store.UpdateJob(ctx, job.ID, status, next, fmt.Sprintf("%v", err)); updateErr != nil {
			return len(jobs), updateErr
		}
		r.recordEvent(ctx, &job, status, err.Error(), map[string]any{"next_run_at": next})
	}
	return len(jobs), nil
}

func (r Runtime) recordEvent(ctx context.Context, job *store.DurableJob, eventType, message string, payload any) {
	if job == nil {
		return
	}
	events, ok := r.Store.(EventStore)
	if !ok {
		return
	}
	raw := []byte(`{}`)
	if payload != nil {
		if encoded, err := json.Marshal(payload); err == nil {
			raw = encoded
		}
	}
	_ = events.AppendJobEvent(ctx, &store.JobEvent{JobID: job.ID, Tenant: job.Tenant, Type: eventType, Message: message, Payload: raw})
}

// Run keeps polling until ctx is cancelled. Each poll claims work through the
// store lease, so multiple processes can run this loop safely.
func (r Runtime) Run(ctx context.Context, cfg WorkerConfig, handler Handler) error {
	if handler == nil {
		return errors.New("job handler is required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	sem := make(chan struct{}, cfg.Concurrency)
	if cfg.LeaderLease != "" {
		leaseStore, ok := r.Store.(LeaseStore)
		if !ok {
			return errors.New("configured job store does not support leader leases")
		}
		if cfg.LeaderTTL <= 0 {
			cfg.LeaderTTL = 15 * time.Second
		}
		defer func() { _ = leaseStore.ReleaseLease(context.Background(), cfg.LeaderLease, r.Owner) }()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if cfg.LeaderLease != "" {
				leaseStore := r.Store.(LeaseStore)
				acquired, err := leaseStore.AcquireLease(ctx, cfg.LeaderLease, r.Owner, cfg.LeaderTTL)
				if err != nil {
					return err
				}
				if !acquired {
					continue
				}
			}
			for i := 0; i < cfg.Concurrency; i++ {
				select {
				case sem <- struct{}{}:
				default:
					continue
				}
				go func() { defer func() { <-sem }(); _, _ = r.RunOnce(ctx, 1, handler) }()
			}
		}
	}
}

func (r Runtime) retryDelay(attempt int) time.Duration {
	base := r.Backoff
	if base <= 0 {
		base = 5 * time.Second
	}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	return base * time.Duration(1<<(attempt-1))
}
