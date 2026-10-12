package performance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

var baseTestTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func fixedTime(offsetSeconds int) time.Time {
	return baseTestTime.Add(time.Duration(offsetSeconds) * time.Second)
}

// --- ADR 0006 Phase 7C: Review -> Execution Attribution tests ---
//
// These tests exercise ReviewFinalizationHook's EXACT correlation logic
// directly: two pending observations are created for the SAME TaskID
// (simulating two execution attempts, E1/E2 - exactly like a retry would
// produce, each with its own Core-generated ExecutionID), then various
// review-arrival orderings/replays are driven through the hook, proving
// a review can only ever finalize the EXACT execution its
// FindingObservation.SourceExecutionRef names - never a "most recently
// started pending" guess.

func seedTwoPendingObservations(t *testing.T, pstore PerformanceStore, taskID supervisor.TaskID, tenant string) (e1, e2 supervisor.ExecutionID) {
	t.Helper()
	observer := NewSupervisorExecutionObserver(pstore, nil)
	task := supervisor.Task{ID: taskID, Type: "security_review", Tenant: tenant}

	e1 = supervisor.NewExecutionID(taskID)
	observer.ObserveExecution(context.Background(), task, "agent-a", e1, supervisor.ExecutionResult{Status: supervisor.TaskStatusCompleted}, nil, fixedTime(0), fixedTime(1))

	e2 = supervisor.NewExecutionID(taskID)
	observer.ObserveExecution(context.Background(), task, "agent-a", e2, supervisor.ExecutionResult{Status: supervisor.TaskStatusCompleted}, nil, fixedTime(2), fixedTime(3))

	return e1, e2
}

func TestAttribution_ReviewFinalizesExactExecutionNotNewest(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	e1, e2 := seedTwoPendingObservations(t, pstore, "T1", "tenant-a")

	hook := NewReviewFinalizationHook(pstore, nil, nil)
	group := review.ReviewGroup{
		ID: "G1", Tenant: "tenant-a", Status: "verified",
		Observations: []review.FindingObservation{
			{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: string(e1), Stance: review.StanceConfirms},
		},
	}
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")

	o1, err := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e1), ObservationKindExecution))
	if err != nil {
		t.Fatalf("Get e1: %v", err)
	}
	if o1.Status != ObservationFinalized || o1.QualityOutcome != QualityVerified {
		t.Fatalf("expected e1 (the execution the review actually names) to be finalized+verified, got %+v", o1)
	}

	o2, err := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e2), ObservationKindExecution))
	if err != nil {
		t.Fatalf("Get e2: %v", err)
	}
	if o2.Status != ObservationPending {
		t.Fatalf("expected e2 (newer, but NOT what the review named) to remain untouched/pending, got %+v", o2)
	}
}

func TestAttribution_LateReviewOfOlderExecutionNeverTouchesNewer(t *testing.T) {
	// E2 starts AND completes before the (late-arriving) review of E1's
	// finding is applied - the stale-review protection case explicitly
	// called out in the mandate.
	pstore := NewMemoryPerformanceStore()
	e1, e2 := seedTwoPendingObservations(t, pstore, "T1", "tenant-a")

	hook := NewReviewFinalizationHook(pstore, nil, nil)
	group := review.ReviewGroup{
		ID: "G1", Tenant: "tenant-a", Status: "verified",
		Observations: []review.FindingObservation{
			{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: string(e1), Stance: review.StanceConfirms},
		},
	}
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")

	o2, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e2), ObservationKindExecution))
	if o2.Status != ObservationPending {
		t.Fatalf("a stale review of E1 must never finalize E2, got %+v", o2)
	}
}

func TestAttribution_OutOfOrderReviewsEachFinalizeTheirOwnExecution(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	e1, e2 := seedTwoPendingObservations(t, pstore, "T1", "tenant-a")
	hook := NewReviewFinalizationHook(pstore, nil, nil)

	// Review of E2 arrives FIRST (out of order), then review of E1.
	hook.ObserveReviewTransition(context.Background(), review.ReviewGroup{
		ID: "G2", Tenant: "tenant-a", Status: "rejected",
		Observations: []review.FindingObservation{
			{ID: "O2", FindingID: "F2", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: string(e2), Stance: review.StanceRefutes},
		},
	}, "unconfirmed")
	hook.ObserveReviewTransition(context.Background(), review.ReviewGroup{
		ID: "G1", Tenant: "tenant-a", Status: "verified",
		Observations: []review.FindingObservation{
			{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: string(e1), Stance: review.StanceConfirms},
		},
	}, "unconfirmed")

	o1, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e1), ObservationKindExecution))
	o2, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e2), ObservationKindExecution))
	if o1.QualityOutcome != QualityVerified {
		t.Fatalf("expected e1 verified regardless of arrival order, got %+v", o1)
	}
	if o2.QualityOutcome != QualityRejected {
		t.Fatalf("expected e2 rejected regardless of arrival order, got %+v", o2)
	}
}

func TestAttribution_FinalizedTwiceIsIdempotent(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	e1, _ := seedTwoPendingObservations(t, pstore, "T1", "tenant-a")
	hook := NewReviewFinalizationHook(pstore, nil, nil)
	group := review.ReviewGroup{
		ID: "G1", Tenant: "tenant-a", Status: "verified",
		Observations: []review.FindingObservation{
			{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: string(e1), Stance: review.StanceConfirms},
		},
	}
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed") // replay

	o1, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e1), ObservationKindExecution))
	if o1.Status != ObservationFinalized || o1.QualityOutcome != QualityVerified {
		t.Fatalf("expected idempotent re-finalization, got %+v", o1)
	}
}

func TestAttribution_E1ReviewReplayCannotMutateE2(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	e1, e2 := seedTwoPendingObservations(t, pstore, "T1", "tenant-a")
	hook := NewReviewFinalizationHook(pstore, nil, nil)
	group := review.ReviewGroup{
		ID: "G1", Tenant: "tenant-a", Status: "verified",
		Observations: []review.FindingObservation{
			{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: string(e1), Stance: review.StanceConfirms},
		},
	}
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")
	// Replay the SAME group snapshot again.
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")

	o2, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e2), ObservationKindExecution))
	if o2.Status != ObservationPending {
		t.Fatalf("replaying E1's review must never touch E2, got %+v", o2)
	}
}

func TestNegative_AmbiguousProvenanceFailsClosed(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	e1, e2 := seedTwoPendingObservations(t, pstore, "T1", "tenant-a")
	evidence := review.NewMemoryEvidenceStore()
	// Two evidence refs for the SAME observation resolve to two
	// DIFFERENT ExecutionIDs for the same task - genuinely ambiguous
	// provenance. Must never be resolved by picking either one.
	evidence.Put(context.Background(), review.EvidenceRecord{ID: "EV1", Tenant: "tenant-a", TaskID: "T1", ExecutionID: string(e1), Digest: "sha256:a"})
	evidence.Put(context.Background(), review.EvidenceRecord{ID: "EV2", Tenant: "tenant-a", TaskID: "T1", ExecutionID: string(e2), Digest: "sha256:b"})

	hook := NewReviewFinalizationHook(pstore, evidence, nil)
	group := review.ReviewGroup{
		ID: "G1", Tenant: "tenant-a", Status: "verified",
		Observations: []review.FindingObservation{
			{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", EvidenceRefs: []string{"EV1", "EV2"}, Stance: review.StanceConfirms},
		},
	}
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")

	o1, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e1), ObservationKindExecution))
	o2, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e2), ObservationKindExecution))
	if o1.Status != ObservationPending || o2.Status != ObservationPending {
		t.Fatalf("ambiguous provenance must fail closed - neither candidate finalized, got e1=%+v e2=%+v", o1, o2)
	}
}

func TestNegative_UnknownExecutionIDFailsClosed(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	hook := NewReviewFinalizationHook(pstore, nil, nil)
	group := review.ReviewGroup{
		ID: "G1", Tenant: "tenant-a", Status: "verified",
		Observations: []review.FindingObservation{
			{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: "no-such-execution", Stance: review.StanceConfirms},
		},
	}
	// Must not panic, must not fabricate an observation - there is
	// simply nothing to finalize for an ExecutionID no observation was
	// ever saved under.
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")

	all, _ := pstore.Query(context.Background(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if len(all) != 0 {
		t.Fatalf("expected no observation to exist/be fabricated, got %+v", all)
	}
}

func TestAttribution_EvidenceChainResolvesWhenSourceExecutionRefAbsent(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	e1, e2 := seedTwoPendingObservations(t, pstore, "T1", "tenant-a")
	evidence := review.NewMemoryEvidenceStore()
	evidence.Put(context.Background(), review.EvidenceRecord{ID: "EV1", Tenant: "tenant-a", TaskID: "T1", ExecutionID: string(e1), Digest: "sha256:a"})

	hook := NewReviewFinalizationHook(pstore, evidence, nil)
	group := review.ReviewGroup{
		ID: "G1", Tenant: "tenant-a", Status: "verified",
		Observations: []review.FindingObservation{
			// No SourceExecutionRef - must resolve via EvidenceRefs instead.
			{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", EvidenceRefs: []string{"EV1"}, Stance: review.StanceConfirms},
		},
	}
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")

	o1, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e1), ObservationKindExecution))
	o2, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e2), ObservationKindExecution))
	if o1.Status != ObservationFinalized {
		t.Fatalf("expected e1 finalized via evidence-resolved ExecutionID, got %+v", o1)
	}
	if o2.Status != ObservationPending {
		t.Fatalf("expected e2 untouched, got %+v", o2)
	}
}

func TestNegative_EvidenceFromWrongTenantNeverResolves(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	e1, _ := seedTwoPendingObservations(t, pstore, "T1", "tenant-a")
	evidence := review.NewMemoryEvidenceStore()
	// Evidence record claims a DIFFERENT tenant than the review group -
	// must never be used to resolve attribution.
	evidence.Put(context.Background(), review.EvidenceRecord{ID: "EV1", Tenant: "tenant-b", TaskID: "T1", ExecutionID: string(e1), Digest: "sha256:a"})

	hook := NewReviewFinalizationHook(pstore, evidence, nil)
	group := review.ReviewGroup{
		ID: "G1", Tenant: "tenant-a", Status: "verified",
		Observations: []review.FindingObservation{
			{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", EvidenceRefs: []string{"EV1"}, Stance: review.StanceConfirms},
		},
	}
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")

	o1, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e1), ObservationKindExecution))
	if o1.Status != ObservationPending {
		t.Fatalf("expected cross-tenant evidence to never resolve attribution, got %+v", o1)
	}
}

// --- ADR 0006 Phase 7C: Observer Failure Visibility (auto-wiring) ---

func TestObserverFailure_WithObserverAutoWiresFailureSink(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	observer := NewSupervisorExecutionObserver(pstore, nil)
	exec := supervisor.NewExecutor(newTestDirectory(nil), supervisor.AllowAllAuthorizer{}, &supervisor.FakeExecutionProvider{}, supervisor.DefaultSupervisorLimits(), supervisor.NewCoreState(supervisor.TaskGraph{}, nil))
	exec = exec.WithObserver(observer)
	if exec.FailureSink == nil {
		t.Fatalf("expected WithObserver to auto-wire SupervisorExecutionObserver as the FailureSink too, since it implements supervisor.ExecutionObserverFailureSink")
	}
}

func TestConcurrency_TwoReviewsFinalizeTheirOwnExecutionsIndependently(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	e1, e2 := seedTwoPendingObservations(t, pstore, "T1", "tenant-a")
	hook := NewReviewFinalizationHook(pstore, nil, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		hook.ObserveReviewTransition(context.Background(), review.ReviewGroup{
			ID: "G1", Tenant: "tenant-a", Status: "verified",
			Observations: []review.FindingObservation{{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: string(e1), Stance: review.StanceConfirms}},
		}, "unconfirmed")
	}()
	go func() {
		defer wg.Done()
		hook.ObserveReviewTransition(context.Background(), review.ReviewGroup{
			ID: "G2", Tenant: "tenant-a", Status: "rejected",
			Observations: []review.FindingObservation{{ID: "O2", FindingID: "F2", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: string(e2), Stance: review.StanceRefutes}},
		}, "unconfirmed")
	}()
	wg.Wait()

	o1, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e1), ObservationKindExecution))
	o2, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", string(e2), ObservationKindExecution))
	if o1.QualityOutcome != QualityVerified {
		t.Fatalf("expected e1 verified, got %+v", o1)
	}
	if o2.QualityOutcome != QualityRejected {
		t.Fatalf("expected e2 rejected, got %+v", o2)
	}
}
