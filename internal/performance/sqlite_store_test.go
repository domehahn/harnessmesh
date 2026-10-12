package performance

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/store"
)

func newTestSQLiteStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(dir, "perf.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestStoreBackedPerformanceStore_SaveGetRoundTrip(t *testing.T) {
	st := newTestSQLiteStore(t)
	pstore := NewStoreBackedPerformanceStore(st)
	ctx := context.Background()

	obs, err := NewObservation(baseInput())
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	if err := pstore.Save(ctx, obs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := pstore.Get(ctx, obs.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TaskID != obs.TaskID || got.AgentID != obs.AgentID {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, obs)
	}
}

func TestStoreBackedPerformanceStore_IdempotentReplay(t *testing.T) {
	st := newTestSQLiteStore(t)
	pstore := NewStoreBackedPerformanceStore(st)
	ctx := context.Background()
	obs, _ := NewObservation(baseInput())
	for i := 0; i < 3; i++ {
		if err := pstore.Save(ctx, obs); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
	all, err := pstore.Query(ctx, ObservationFilter{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 row after 3 replays against the real DB, got %d", len(all))
	}
}

func TestStoreBackedPerformanceStore_DuplicateIdentityConflictRejected(t *testing.T) {
	st := newTestSQLiteStore(t)
	pstore := NewStoreBackedPerformanceStore(st)
	ctx := context.Background()
	obs, _ := NewObservation(baseInput())
	pstore.Save(ctx, obs)

	forged := obs
	forged.AgentID = "attacker-agent"
	err := pstore.Save(ctx, forged)
	var want *DuplicateObservation
	if !errors.As(err, &want) {
		t.Fatalf("expected DuplicateObservation against the real store, got %v (%T)", err, err)
	}
}

func TestStoreBackedPerformanceStore_TenantIsolation(t *testing.T) {
	st := newTestSQLiteStore(t)
	pstore := NewStoreBackedPerformanceStore(st)
	ctx := context.Background()

	a := baseInput()
	a.Tenant = "tenant-a"
	obsA, _ := NewObservation(a)
	b := baseInput()
	b.Tenant = "tenant-b"
	b.TaskID = "T2"
	obsB, _ := NewObservation(b)
	pstore.Save(ctx, obsA)
	pstore.Save(ctx, obsB)

	onlyA, err := pstore.Query(ctx, ObservationFilter{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(onlyA) != 1 || onlyA[0].Tenant != "tenant-a" {
		t.Fatalf("expected exactly 1 tenant-a row via the real store, got %+v", onlyA)
	}
}

func TestStoreBackedPerformanceStore_AggregateAgainstRealDB(t *testing.T) {
	st := newTestSQLiteStore(t)
	pstore := NewStoreBackedPerformanceStore(st)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		in := baseInput()
		in.TaskID = taskIDLike(i)
		in.ExecutionID = string(in.TaskID) + "#1"
		obs, _ := NewObservation(in)
		obs.Status = ObservationFinalized
		obs.QualityOutcome = QualityVerified
		if err := pstore.Save(ctx, obs); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	dataset := NewStoreDataset(pstore)
	summary, err := Aggregate(ctx, dataset, ObservationFilter{Tenant: "tenant-a"}, "security-agent-a", TaskClassSecurityReview)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if summary.VerifiedCount != 3 {
		t.Fatalf("expected 3 verified against the real DB, got %d", summary.VerifiedCount)
	}
}
