package performance

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// TestConcurrency_ExecutionAndFinalizationSafe runs many concurrent
// executions (each creating a pending observation) and concurrent review
// finalizations (each finalizing a different task's observation)
// against the SAME MemoryPerformanceStore, under -race, proving no lost
// updates and no corruption.
func TestConcurrency_ExecutionAndFinalizationSafe(t *testing.T) {
	const n = 20
	pstore := NewMemoryPerformanceStore()
	agents := map[supervisor.AgentID][]string{"agent-a": {}}
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	_, state, exec := newWiredExecutor(t, agents, supervisor.DefaultSupervisorLimits(), provider, pstore)

	taskIDs := make([]supervisor.TaskID, n)
	for i := 0; i < n; i++ {
		taskIDs[i] = supervisor.TaskID(fmt.Sprintf("T%02d", i))
		setupPendingTask(t, state, taskIDs[i], "agent-a", "security_review", "tenant-a")
	}

	results := make([]supervisor.ExecutionResult, n)
	var wg sync.WaitGroup
	for i, id := range taskIDs {
		i := i
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := exec.Execute(context.Background(), id)
			if err != nil {
				t.Errorf("Execute(%s): %v", id, err)
			}
			results[i] = result // each goroutine writes only its own index - no race
		}()
	}
	wg.Wait()

	// Now concurrently finalize each task's observation via independent
	// review groups.
	hook := NewReviewFinalizationHook(pstore, nil, nil)
	var wg2 sync.WaitGroup
	for i, id := range taskIDs {
		i := i
		id := id
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			reviewState := review.NewReviewState(nil)
			claim := review.ClaimInput{Subject: review.SubjectRef{Path: fmt.Sprintf("file%d.go", i)}, Category: "security", Line: 1}
			groupID, err := reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: id, SourceExecutionRef: string(results[i].ExecutionID), ArtifactVersion: "v1", Stance: review.StanceConfirms})
			if err != nil {
				t.Errorf("Ingest: %v", err)
				return
			}
			reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", SourceExecutionID: supervisor.TaskID(fmt.Sprintf("other-%d", i)), ArtifactVersion: "v1", Stance: review.StanceConfirms})
			applier := review.NewReviewApplier(review.NewReviewValidator(review.DefaultReviewLimits()), review.NewReviewAuthorizer(), reviewState).WithObserver(hook)
			if _, err := applier.Apply(context.Background(), groupID, []review.ArbiterAction{
				review.RecommendVerificationAction{GroupID: groupID, RationaleSummary: "r"},
			}); err != nil {
				t.Errorf("Apply: %v", err)
			}
		}()
	}
	wg2.Wait()

	all, err := pstore.Query(context.Background(), ObservationFilter{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != n {
		t.Fatalf("expected %d observations (no lost updates), got %d", n, len(all))
	}
	for _, obs := range all {
		if obs.Status != ObservationFinalized || obs.QualityOutcome != QualityVerified {
			t.Fatalf("expected every observation finalized+verified, got %+v", obs)
		}
	}
}
