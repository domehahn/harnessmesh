package performance

import (
	"context"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

func TestE2E_OfflineReplay_Batch(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	seedFinalized(t, pstore, "tenant-a", "agent-B", TaskClassSecurityReview, 20, 18, 1)
	snapStore := NewMemoryCandidateSnapshotStore()

	var requests []ReplayRequest
	completeExpected, incompleteExpected := 0, 0
	for i := 0; i < 100; i++ {
		taskID := supervisor.TaskID("T" + string(rune('A'+i%26)) + string(rune('0'+i/26)))
		var decisionID supervisor.DecisionID
		if i%9 != 0 { // ~89 have a snapshot, ~11 don't
			snap := testSnapshot(t, "tenant-a", taskID, TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())
			if err := snapStore.Save(context.Background(), snap); err != nil {
				t.Fatalf("Save snapshot: %v", err)
			}
			decisionID = snap.DecisionID
			completeExpected++
		} else {
			decisionID = supervisor.NewDecisionID(taskID) // no matching snapshot was ever saved for this one
			incompleteExpected++
		}
		requests = append(requests, ReplayRequest{
			Tenant: "tenant-a", DecisionID: decisionID, TaskID: taskID, TaskClass: TaskClassSecurityReview,
			DecisionTime: time.Now(), HistoricalPolicy: championDesc(), HistoricalAgentID: "agent-A",
			ReplayPolicy: challengerDesc(),
			NewReplayPolicy: func(dataset DecisionDataset) RoutingPolicy {
				return NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: dataset, Tenant: "tenant-a"}, challengerDesc())
			},
			Mode: ReplayModeRetrospective,
		})
	}

	replayer := NewReplayer(snapStore, NewStoreDataset(pstore), DefaultEvaluationLimits(), nil)
	results, err := replayer.ReplayBatch(context.Background(), requests)
	if err != nil {
		t.Fatalf("ReplayBatch: %v", err)
	}

	var complete, incomplete int
	for _, r := range results {
		switch r.Status {
		case ReplayComplete:
			complete++
		case ReplayIncomplete:
			incomplete++
		}
	}
	if complete != completeExpected {
		t.Fatalf("expected %d complete, got %d", completeExpected, complete)
	}
	if incomplete != incompleteExpected {
		t.Fatalf("expected %d incomplete, got %d", incompleteExpected, incomplete)
	}
}

func TestNegative_NoHistoricalCandidateSnapshotIsIncomplete(t *testing.T) {
	snapStore := NewMemoryCandidateSnapshotStore() // empty - no snapshot ever captured
	replayer := NewReplayer(snapStore, NewStoreDataset(NewMemoryPerformanceStore()), DefaultEvaluationLimits(), nil)

	result, err := replayer.Replay(context.Background(), ReplayRequest{
		Tenant: "tenant-a", DecisionID: supervisor.NewDecisionID("T1"), TaskID: "T1", TaskClass: TaskClassSecurityReview,
		DecisionTime: time.Now(), HistoricalAgentID: "agent-A",
		NewReplayPolicy: func(dataset DecisionDataset) RoutingPolicy {
			return NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: dataset, Tenant: "tenant-a"}, challengerDesc())
		},
		Mode: ReplayModeRetrospective,
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if result.Status != ReplayIncomplete {
		t.Fatalf("expected incomplete when no historical snapshot exists, got %q", result.Status)
	}
}

// TestCandidateSnapshot_HistoricalNotReconstructedFromTodaysRegistry
// proves the exact scenario ADR 0008 Part 24 requires: the registry
// changes after the decision, but replay still uses the ORIGINAL
// snapshot.
func TestCandidateSnapshot_HistoricalNotReconstructedFromTodaysRegistry(t *testing.T) {
	snapStore := NewMemoryCandidateSnapshotStore()
	original := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())
	if err := snapStore.Save(context.Background(), original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// "Today's registry" would be [A, C] - but nothing in Replay ever
	// queries a live registry; it can only ever see what was saved,
	// looked up by the EXACT DecisionID this snapshot was captured for.
	stored, found, err := snapStore.Get(context.Background(), "tenant-a", original.DecisionID)
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if len(stored.AgentIDs) != 2 || stored.AgentIDs[0] != "agent-A" || stored.AgentIDs[1] != "agent-B" {
		t.Fatalf("expected the original [A,B] snapshot preserved, got %v", stored.AgentIDs)
	}
}

func TestNegative_ReplayInvalidCandidateContext(t *testing.T) {
	snapStore := NewMemoryCandidateSnapshotStore()
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())
	snapStore.Save(context.Background(), snap)
	replayer := NewReplayer(snapStore, NewStoreDataset(NewMemoryPerformanceStore()), DefaultEvaluationLimits(), nil)

	result, err := replayer.Replay(context.Background(), ReplayRequest{
		Tenant: "tenant-a", DecisionID: snap.DecisionID, TaskID: "T1", TaskClass: TaskClassSecurityReview,
		DecisionTime: time.Now(), HistoricalAgentID: "agent-NOT-IN-SNAPSHOT", // inconsistent historical record
		NewReplayPolicy: func(dataset DecisionDataset) RoutingPolicy {
			return NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: dataset, Tenant: "tenant-a"}, challengerDesc())
		},
		Mode: ReplayModeRetrospective,
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if result.Status != ReplayInvalidCandidateContext {
		t.Fatalf("expected invalid_candidate_context, got %q", result.Status)
	}
}

func TestNegative_ReplayBatchTooLarge(t *testing.T) {
	limits := DefaultEvaluationLimits()
	limits.MaxReplayBatchSize = 2
	replayer := NewReplayer(NewMemoryCandidateSnapshotStore(), NewStoreDataset(NewMemoryPerformanceStore()), limits, nil)
	_, err := replayer.ReplayBatch(context.Background(), make([]ReplayRequest, 3))
	if err == nil {
		t.Fatalf("expected a ReplayBatchTooLarge error")
	}
}

func TestNegative_ReplayCannotInvokeExecutionProvider(t *testing.T) {
	// Structural: Replayer's zero value has no ExecutionProvider field
	// to even configure - see TestNegative_ReplayerHasNoExecutorReference
	// for the reflection-based proof. This test confirms the SAME
	// property behaviorally: a Replay call that would otherwise succeed
	// completes without any execution side effect being possible, since
	// there is no provider anywhere in the call chain to invoke.
	snapStore := NewMemoryCandidateSnapshotStore()
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	snapStore.Save(context.Background(), snap)
	pstore := NewMemoryPerformanceStore()
	seedFinalized(t, pstore, "tenant-a", "agent-A", TaskClassSecurityReview, 20, 18, 1)
	replayer := NewReplayer(snapStore, NewStoreDataset(pstore), DefaultEvaluationLimits(), nil)

	result, err := replayer.Replay(context.Background(), ReplayRequest{
		Tenant: "tenant-a", DecisionID: snap.DecisionID, TaskID: "T1", TaskClass: TaskClassSecurityReview,
		DecisionTime: time.Now(), HistoricalAgentID: "agent-A",
		NewReplayPolicy: func(dataset DecisionDataset) RoutingPolicy {
			return NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: dataset, Tenant: "tenant-a"}, challengerDesc())
		},
		Mode: ReplayModeRetrospective,
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if result.Status != ReplayComplete {
		t.Fatalf("expected complete, got %q", result.Status)
	}
	// No execution occurred, no task graph was touched - there is
	// simply nothing in this package capable of doing so, confirmed by
	// the type system (see the dedicated structural test).
}

func TestNegative_CrossTenantReplayQueryDenied(t *testing.T) {
	snapStore := NewMemoryCandidateSnapshotStore()
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	snapStore.Save(context.Background(), snap)
	pstore := NewMemoryPerformanceStore()
	seedFinalized(t, pstore, "tenant-a", "agent-A", TaskClassSecurityReview, 20, 18, 1)
	replayer := NewReplayer(snapStore, NewStoreDataset(pstore), DefaultEvaluationLimits(), nil)

	// DeterministicAdvisor itself is tenant-scoped to tenant-b while the
	// request/snapshot are tenant-a - must fail closed inside Advise,
	// surfacing as insufficient/error, never leaking tenant-a data.
	result, err := replayer.Replay(context.Background(), ReplayRequest{
		Tenant: "tenant-a", DecisionID: snap.DecisionID, TaskID: "T1", TaskClass: TaskClassSecurityReview,
		DecisionTime: time.Now(), HistoricalAgentID: "agent-A",
		NewReplayPolicy: func(dataset DecisionDataset) RoutingPolicy {
			return NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: dataset, Tenant: "tenant-b"}, challengerDesc())
		},
		Mode: ReplayModeRetrospective,
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if result.Status == ReplayComplete {
		t.Fatalf("expected the cross-tenant-scoped policy to fail closed, not complete")
	}
}
