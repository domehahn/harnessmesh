package performance

import (
	"context"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// TestE2E_TimeFaithfulReplay_CannotUseFutureData is ADR 0008 Part 23's
// exact required scenario: a decision at T1, an observation favoring
// Agent B observed at T2 > T1 - TimeFaithful replay must not see it;
// Retrospective replay may.
func TestE2E_TimeFaithfulReplay_CannotUseFutureData(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	t1 := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) // well after t1

	// Agent A has weaker history, all created/finalized BEFORE t1.
	for i := 0; i < 12; i++ {
		quality := QualityVerified
		if i < 4 {
			quality = QualityRejected
		}
		obs := mustObservationAt(t, "tenant-a", "agent-A", TaskClassSecurityReview, i, quality, t1.Add(-time.Duration(i+1)*time.Hour))
		if err := pstore.Save(context.Background(), obs); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	// Agent B's STRONG history is only observed/finalized at t2 - AFTER
	// the decision time. A time-faithful replay at t1 must not see it.
	for i := 0; i < 12; i++ {
		obs := mustObservationAt(t, "tenant-a", "agent-B", TaskClassSecurityReview, i, QualityVerified, t2.Add(-time.Duration(i+1)*time.Hour))
		if err := pstore.Save(context.Background(), obs); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	snapStore := NewMemoryCandidateSnapshotStore()
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, t1)
	snapStore.Save(context.Background(), snap)

	replayer := NewReplayer(snapStore, NewStoreDataset(pstore), DefaultEvaluationLimits(), nil)

	timeFaithful, err := replayer.Replay(context.Background(), ReplayRequest{
		Tenant: "tenant-a", DecisionID: snap.DecisionID, TaskID: "T1", TaskClass: TaskClassSecurityReview,
		DecisionTime: t1, HistoricalAgentID: "agent-A",
		NewReplayPolicy: func(dataset DecisionDataset) RoutingPolicy {
			return NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: dataset, Tenant: "tenant-a"}, challengerDesc())
		},
		Mode: ReplayModeTimeFaithful,
	})
	if err != nil {
		t.Fatalf("time-faithful Replay: %v", err)
	}
	// Agent B has NO knowable history at t1 (its observations were all
	// created after t1) - so it must be cold-start, and since Agent A
	// alone is sufficient, time-faithful replay must recommend A, never
	// the future-informed B.
	if timeFaithful.Status == ReplayComplete && timeFaithful.ReplayAgentID == "agent-B" {
		t.Fatalf("time-faithful replay must never use future data to recommend agent-B, got %+v", timeFaithful)
	}

	retrospective, err := replayer.Replay(context.Background(), ReplayRequest{
		Tenant: "tenant-a", DecisionID: snap.DecisionID, TaskID: "T1", TaskClass: TaskClassSecurityReview,
		DecisionTime: t1, HistoricalAgentID: "agent-A",
		NewReplayPolicy: func(dataset DecisionDataset) RoutingPolicy {
			return NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: dataset, Tenant: "tenant-a"}, challengerDesc())
		},
		Mode: ReplayModeRetrospective,
	})
	if err != nil {
		t.Fatalf("retrospective Replay: %v", err)
	}
	if retrospective.Status != ReplayComplete || retrospective.ReplayAgentID != "agent-B" {
		t.Fatalf("expected retrospective replay (today's full data) to see Agent B's strong history, got %+v", retrospective)
	}
}

func mustObservationAt(t *testing.T, tenant string, agent supervisor.AgentID, taskClass TaskClass, k int, quality QualityOutcome, observedAt time.Time) PerformanceObservation {
	t.Helper()
	in := NewObservationInput{
		Tenant: tenant, TaskID: supervisor.TaskID(string(agent) + "-tf-" + taskIDSuffix(k)),
		ExecutionID: string(agent) + "-tf-" + taskIDSuffix(k) + "#1",
		TaskClass:   taskClass, AgentID: agent, ExecutionOutcome: ExecutionCompleted,
		StartedAt: observedAt, FinishedAt: observedAt.Add(time.Second),
		Metrics: PerformanceMetrics{Duration: time.Second},
	}
	obs, err := NewObservation(in)
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	obs.CreatedAt = observedAt
	obs.UpdatedAt = observedAt
	obs.Status = ObservationFinalized
	obs.QualityOutcome = quality
	return obs
}

func TestNegative_FutureDataLeak_ObservationCreatedAfterCutoffExcluded(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	obs := mustObservationAt(t, "tenant-a", "agent-A", TaskClassSecurityReview, 0, QualityVerified, cutoff.Add(time.Hour))
	_, ok := ObservationAsOf(obs, cutoff)
	if ok {
		t.Fatalf("expected an observation created AFTER the cutoff to be excluded entirely")
	}
}

func TestNegative_FutureFinalizationExcludedButExecutionKept(t *testing.T) {
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	obs := mustObservationAt(t, "tenant-a", "agent-A", TaskClassSecurityReview, 0, QualityVerified, cutoff.Add(-time.Hour))
	// Execution happened before cutoff, but finalization (UpdatedAt) is
	// AFTER cutoff - simulating a review that concluded later.
	obs.UpdatedAt = cutoff.Add(time.Hour)

	view, ok := ObservationAsOf(obs, cutoff)
	if !ok {
		t.Fatalf("expected the observation itself (created before cutoff) to remain visible")
	}
	if view.QualityOutcome != QualityUnreviewed || view.Status != ObservationPending {
		t.Fatalf("expected quality rolled back to unreviewed/pending since finalization happened after cutoff, got %+v", view)
	}
	if view.ExecutionOutcome != ExecutionCompleted {
		t.Fatalf("expected execution outcome (known before cutoff) to remain visible, got %q", view.ExecutionOutcome)
	}
}

func TestNegative_ConcurrentFinalizationWhileSummaryRuns(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	for i := 0; i < 15; i++ {
		seedObservationWithUsages(t, pstore, "tenant-a", "agent-A", TaskClassSecurityReview, "cfs"+string(rune('a'+i)), ExecutionCompleted, QualityVerified, nil)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			if _, err := Aggregate(context.Background(), NewStoreDataset(pstore), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview); err != nil {
				t.Errorf("Aggregate: %v", err)
			}
		}
	}()
	for i := 0; i < 20; i++ {
		seedObservationWithUsages(t, pstore, "tenant-a", "agent-A", TaskClassSecurityReview, "extra"+string(rune('a'+i%26))+string(rune('0'+i/26)), ExecutionCompleted, QualityVerified, nil)
	}
	<-done
}
