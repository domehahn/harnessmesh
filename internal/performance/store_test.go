package performance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

func TestStore_SaveGetRoundTrip(t *testing.T) {
	store := NewMemoryPerformanceStore()
	obs, _ := NewObservation(baseInput())
	if err := store.Save(context.Background(), obs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Get(context.Background(), obs.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TaskID != obs.TaskID {
		t.Fatalf("round-trip mismatch")
	}
}

func TestNegative_DuplicateExecutionResultDoesNotDoubleCount(t *testing.T) {
	store := NewMemoryPerformanceStore()
	obs, _ := NewObservation(baseInput())
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := store.Save(ctx, obs); err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
	}
	all, _ := store.Query(ctx, ObservationFilter{Tenant: "tenant-a"})
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 stored observation after 5 identical replays, got %d", len(all))
	}
}

func TestNegative_SameOutcomeReplayedTwiceNoDoubleCount(t *testing.T) {
	store := NewMemoryPerformanceStore()
	ctx := context.Background()
	obs, _ := NewObservation(baseInput())
	finalized := FinalizeWithReviewStub(obs)
	store.Save(ctx, obs)
	store.Save(ctx, finalized)
	store.Save(ctx, finalized) // replay of the SAME finalized state
	got, _ := store.Get(ctx, obs.ID)
	if got.Status != ObservationFinalized {
		t.Fatalf("expected finalized, got %q", got.Status)
	}
	all, _ := store.Query(ctx, ObservationFilter{Tenant: "tenant-a"})
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 observation, got %d", len(all))
	}
}

func TestSecurity_DuplicateIDDifferentIdentityRejected(t *testing.T) {
	store := NewMemoryPerformanceStore()
	ctx := context.Background()
	obs, _ := NewObservation(baseInput())
	store.Save(ctx, obs)

	forged := obs
	forged.AgentID = "attacker-agent" // different identity, same ID
	err := store.Save(ctx, forged)
	var want *DuplicateObservation
	if !errors.As(err, &want) {
		t.Fatalf("expected DuplicateObservation for a colliding ID with different identity, got %v (%T)", err, err)
	}
}

func TestSecurity_HistoricRecordNotSilentlyRewrittenToPending(t *testing.T) {
	store := NewMemoryPerformanceStore()
	ctx := context.Background()
	obs, _ := NewObservation(baseInput())
	finalized := obs
	finalized.Status = ObservationFinalized
	finalized.QualityOutcome = QualityVerified
	store.Save(ctx, obs)
	store.Save(ctx, finalized)

	// A stale write (still Pending) races in after finalization.
	store.Save(ctx, obs)

	got, _ := store.Get(ctx, obs.ID)
	if got.Status != ObservationFinalized {
		t.Fatalf("a finalized record must never be silently reverted to pending by a stale write, got %q", got.Status)
	}
}

func TestNegative_CrossTenantQueryDenied(t *testing.T) {
	store := NewMemoryPerformanceStore()
	ctx := context.Background()
	_, err := store.Query(ctx, ObservationFilter{})
	var want *CrossTenantQueryDenied
	if !errors.As(err, &want) {
		t.Fatalf("expected CrossTenantQueryDenied for an unscoped query, got %v (%T)", err, err)
	}
}

func TestSecurity_CrossTenantStatisticsLeakagePrevented(t *testing.T) {
	store := NewMemoryPerformanceStore()
	ctx := context.Background()
	a := baseInput()
	a.Tenant = "tenant-a"
	obsA, _ := NewObservation(a)
	b := baseInput()
	b.Tenant = "tenant-b"
	b.TaskID = "T2"
	obsB, _ := NewObservation(b)
	store.Save(ctx, obsA)
	store.Save(ctx, obsB)

	onlyA, err := store.Query(ctx, ObservationFilter{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, o := range onlyA {
		if o.Tenant != "tenant-a" {
			t.Fatalf("cross-tenant leak: query for tenant-a returned tenant %q", o.Tenant)
		}
	}
	if len(onlyA) != 1 {
		t.Fatalf("expected exactly 1 observation for tenant-a, got %d", len(onlyA))
	}
}

func TestNegative_PendingRecordExcludedFromFinalizedOnlyAggregation(t *testing.T) {
	store := NewMemoryPerformanceStore()
	ctx := context.Background()
	obs, _ := NewObservation(baseInput())
	store.Save(ctx, obs) // still pending

	finalizedOnly, _ := store.Query(ctx, ObservationFilter{Tenant: "tenant-a", FinalizedOnly: true})
	if len(finalizedOnly) != 0 {
		t.Fatalf("expected 0 finalized observations while the only one is pending, got %d", len(finalizedOnly))
	}
}

func TestConcurrency_ConcurrentSaveNoLostUpdates(t *testing.T) {
	store := NewMemoryPerformanceStore()
	ctx := context.Background()
	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := baseInput()
			in.TaskID = supervisor.TaskID(taskIDFor(i))
			in.ExecutionID = taskIDFor(i) + "#1"
			obs, err := NewObservation(in)
			if err != nil {
				t.Errorf("NewObservation: %v", err)
				return
			}
			if err := store.Save(ctx, obs); err != nil {
				t.Errorf("Save: %v", err)
			}
		}()
	}
	wg.Wait()
	all, _ := store.Query(ctx, ObservationFilter{Tenant: "tenant-a"})
	if len(all) != n {
		t.Fatalf("expected %d observations (no lost updates), got %d", n, len(all))
	}
}

func taskIDFor(i int) string {
	return "T" + string(rune('A'+i))
}

// FinalizeWithReviewStub produces a finalized copy without importing the
// review package into this specific test file's Save-replay scenario -
// kept here deliberately simple (direct field mutation) since the test
// only needs "a finalized version of obs", not a real ReviewGroup.
func FinalizeWithReviewStub(obs PerformanceObservation) PerformanceObservation {
	out := obs
	out.Status = ObservationFinalized
	out.QualityOutcome = QualityVerified
	out.UpdatedAt = time.Now().UTC()
	return out
}
