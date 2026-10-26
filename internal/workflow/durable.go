package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/domehahn/harnessmesh/internal/jobs"
	"github.com/domehahn/harnessmesh/internal/store"
)

// DurableWorkflow is the integration boundary for external durable engines
// such as Temporal. HarnessMesh owns policy, tenant identity and evidence;
// an adapter owns scheduling and recovery. Keeping this contract small avoids
// making an external workflow product a core dependency.
type DurableWorkflow interface {
	Submit(context.Context, WorkflowRequest) (string, error)
	Get(context.Context, string) (WorkflowExecution, error)
	Cancel(context.Context, string, string) error
}

type WorkflowRequest struct {
	ID       string         `json:"id"`
	Tenant   string         `json:"tenant"`
	Kind     string         `json:"kind"`
	Payload  map[string]any `json:"payload,omitempty"`
	Deadline time.Time      `json:"deadline,omitempty"`
}

type WorkflowExecution struct {
	ID        string    `json:"id"`
	Tenant    string    `json:"tenant"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// JobAdapter is the built-in durable implementation. It maps the same
// contract to HarnessMesh's leased job store and is suitable for local,
// SQLite and PostgreSQL deployments. Temporal/Restate adapters can implement
// DurableWorkflow without changing callers.
type JobAdapter struct {
	Runtime jobs.Runtime
	Store   jobs.JobStore
}

func (a JobAdapter) Submit(ctx context.Context, req WorkflowRequest) (string, error) {
	if a.Store == nil {
		return "", errors.New("durable job store is required")
	}
	if req.Tenant == "" || req.Kind == "" {
		return "", errors.New("workflow tenant and kind are required")
	}
	id := req.ID
	if id == "" {
		id = "workflow_" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	raw := map[string]any{"kind": req.Kind, "payload": req.Payload}
	job := &store.DurableJob{ID: id, Tenant: req.Tenant, Kind: req.Kind, Payload: mustJSON(raw), Status: "queued", CreatedAt: time.Now().UTC(), NextRunAt: time.Now().UTC()}
	if err := a.Runtime.Submit(ctx, job); err != nil {
		return "", err
	}
	return id, nil
}

func (a JobAdapter) Get(ctx context.Context, id string) (WorkflowExecution, error) {
	if a.Store == nil {
		return WorkflowExecution{}, errors.New("durable job store is required")
	}
	job, err := a.Store.GetJob(ctx, id)
	if err != nil {
		return WorkflowExecution{}, err
	}
	return WorkflowExecution{ID: job.ID, Tenant: job.Tenant, Kind: job.Kind, Status: job.Status, Error: job.LastError, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt}, nil
}

func (a JobAdapter) Cancel(ctx context.Context, id, reason string) error {
	if a.Store == nil {
		return errors.New("durable job store is required")
	}
	return a.Store.UpdateJob(ctx, id, "cancelled", time.Now().UTC(), reason)
}

func mustJSON(value any) []byte { raw, _ := json.Marshal(value); return raw }
