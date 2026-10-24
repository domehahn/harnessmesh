package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/store"
)

func TestRuntimePersistsRetryAndCompletion(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := Runtime{Store: st, Owner: "worker-a", Lease: time.Minute, Backoff: time.Millisecond, MaxAttempts: 2}
	if err := r.Submit(context.Background(), &store.DurableJob{ID: "job-1", Tenant: "tenant-a", Kind: "agent.task", Payload: json.RawMessage(`{"task":"x"}`)}); err != nil {
		t.Fatal(err)
	}
	n, err := r.RunOnce(context.Background(), 1, func(context.Context, store.DurableJob) error { return errors.New("temporary") })
	if err != nil || n != 1 {
		t.Fatalf("first run: n=%d err=%v", n, err)
	}
	j, err := st.GetJob(context.Background(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != "retry" || j.Attempts != 1 {
		t.Fatalf("unexpected retry state: %+v", j)
	}
	if err := st.UpdateJob(context.Background(), "job-1", "pending", time.Now().UTC(), ""); err != nil {
		t.Fatal(err)
	}
	n, err = r.RunOnce(context.Background(), 1, func(context.Context, store.DurableJob) error { return nil })
	if err != nil || n != 1 {
		t.Fatalf("second run: n=%d err=%v", n, err)
	}
	j, err = st.GetJob(context.Background(), "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != "completed" {
		t.Fatalf("expected completed job: %+v", j)
	}
	events, err := st.ListJobEvents(context.Background(), "job-1", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 4 || events[0].Type != "submitted" || events[len(events)-1].Type != "completed" {
		t.Fatalf("expected durable execution history, got %+v", events)
	}
}

func TestRuntimeRechecksTenantBeforeExecution(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "tenant-jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := Runtime{Store: st, Owner: "worker-a", AuthorizeTenant: func(tenant string) bool { return tenant == "tenant-a" }}
	if err := r.Submit(context.Background(), &store.DurableJob{ID: "job-cross", Tenant: "tenant-b", Kind: "agent.task"}); err != nil {
		t.Fatal(err)
	}
	run := false
	if _, err := r.RunOnce(context.Background(), 1, func(context.Context, store.DurableJob) error { run = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if run {
		t.Fatal("cross-tenant handler must not execute")
	}
	job, err := st.GetJob(context.Background(), "job-cross")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "dead" || job.LastError != "tenant authorization denied" {
		t.Fatalf("unexpected tenant rejection: %+v", job)
	}
}
