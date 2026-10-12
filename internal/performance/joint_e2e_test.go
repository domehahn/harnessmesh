package performance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// TestE2E_SingleModelJointFlow exercises the exact scenario ADR 0007
// specifies: Tenant A, Task T1 (security_review), Agent Advisor selects
// Agent A, Executor generates ExecutionID E1, the agent runtime's model
// call is routed via R1 to Model X, execution completes, Core
// constructs Finding F1 with SourceExecutionRef=E1 (via
// NewFindingObservationFromExecution), Evidence EVID1 references E1,
// independent review verifies the claim, and the resulting
// PerformanceObservation + joint queries are all consistent.
func TestE2E_SingleModelJointFlow(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	execObserver := NewSupervisorExecutionObserver(pstore, nil)

	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{
			TaskID: task.ID, Status: supervisor.TaskStatusCompleted,
			ModelUsages: []supervisor.ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "Model X", Count: 1}},
		}, nil
	}}
	dir := newTestDirectory(map[supervisor.AgentID][]string{"agent-A": {}})
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	exec := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, supervisor.DefaultSupervisorLimits(), state).WithObserver(execObserver)
	setupPendingTask(t, state, "T1", "agent-A", "security_review", "tenant-a")

	execResult, err := exec.Execute(context.Background(), "T1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	executionID := string(execResult.ExecutionID)

	// Core constructs the Finding's provenance from the real execution
	// context - never from provider/agent output.
	findingObs, err := review.NewFindingObservationFromExecution(
		review.ExecutionProvenance{Tenant: "tenant-a", TaskID: "T1", ExecutionID: executionID, AgentID: "agent-A"},
		review.FindingClaim{ID: "O1", FindingID: "F1", ArtifactVersion: "v1", Stance: review.StanceConfirms},
	)
	if err != nil {
		t.Fatalf("NewFindingObservationFromExecution: %v", err)
	}
	if findingObs.SourceExecutionRef != executionID {
		t.Fatalf("expected Finding's SourceExecutionRef to equal the real ExecutionID, got %q vs %q", findingObs.SourceExecutionRef, executionID)
	}

	reviewState := review.NewReviewState(nil)
	claim := review.ClaimInput{Subject: review.SubjectRef{Path: "auth.go"}, Category: "security", Line: 1}
	groupID, err := reviewState.Ingest(claim, "v1", "tenant-a", findingObs)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-B", SourceExecutionID: "T2", ArtifactVersion: "v1", Stance: review.StanceConfirms})

	hook := NewReviewFinalizationHook(pstore, nil, nil)
	applier := review.NewReviewApplier(review.NewReviewValidator(review.DefaultReviewLimits()), review.NewReviewAuthorizer(), reviewState).WithObserver(hook)
	if _, err := applier.Apply(context.Background(), groupID, []review.ArbiterAction{
		review.RecommendVerificationAction{GroupID: groupID, RationaleSummary: "2 independent confirms"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Joint queries must all be consistent.
	o1, err := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", executionID, ObservationKindExecution))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if o1.Status != ObservationFinalized || o1.QualityOutcome != QualityVerified {
		t.Fatalf("expected the observation finalized+verified, got %+v", o1)
	}

	agentLevel, err := Aggregate(context.Background(), NewStoreDataset(pstore), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview)
	if err != nil || agentLevel.VerifiedCount != 1 {
		t.Fatalf("Agent A / security_review: expected 1 verified, got %+v err=%v", agentLevel, err)
	}

	modelLevel, err := AggregatePair(context.Background(), NewStoreDataset(pstore), ObservationFilter{Tenant: "tenant-a"}, "", TaskClassSecurityReview, "", "", "Model X")
	if err != nil || modelLevel.VerifiedCount != 1 {
		t.Fatalf("Model X / security_review: expected 1 verified, got %+v err=%v", modelLevel, err)
	}

	routeLevel, err := AggregatePair(context.Background(), NewStoreDataset(pstore), ObservationFilter{Tenant: "tenant-a"}, "", TaskClassSecurityReview, "R1", "", "")
	if err != nil || routeLevel.VerifiedCount != 1 {
		t.Fatalf("Route R1 / security_review: expected 1 verified, got %+v err=%v", routeLevel, err)
	}

	agentModelPair, err := AggregatePair(context.Background(), NewStoreDataset(pstore), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "Model X")
	if err != nil || agentModelPair.VerifiedCount != 1 {
		t.Fatalf("Agent A + Model X / security_review: expected 1 verified, got %+v err=%v", agentModelPair, err)
	}

	agentRoutePair, err := AggregatePair(context.Background(), NewStoreDataset(pstore), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "R1", "", "")
	if err != nil || agentRoutePair.VerifiedCount != 1 {
		t.Fatalf("Agent A + Route R1 / security_review: expected 1 verified, got %+v err=%v", agentRoutePair, err)
	}
}

// TestE2E_MultiModelJointFlow proves the multi-model execution (E2: two
// calls to Model X via R1, one call to Model Y via R2) is never
// collapsed into a false single-model identity, and its quality is
// counted exactly once regardless of call count.
func TestE2E_MultiModelJointFlow(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	execObserver := NewSupervisorExecutionObserver(pstore, nil)

	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{
			TaskID: task.ID, Status: supervisor.TaskStatusCompleted,
			ModelUsages: []supervisor.ModelUsageRef{
				{RouteID: "R1", Provider: "switchyard", Model: "Model X", Count: 1},
				{RouteID: "R1", Provider: "switchyard", Model: "Model X", Count: 1}, // second call, same route/model - must merge
				{RouteID: "R2", Provider: "switchyard", Model: "Model Y", Count: 1},
			},
		}, nil
	}}
	dir := newTestDirectory(map[supervisor.AgentID][]string{"agent-A": {}})
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	exec := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, supervisor.DefaultSupervisorLimits(), state).WithObserver(execObserver)
	setupPendingTask(t, state, "E2", "agent-A", "security_review", "tenant-a")

	if _, err := exec.Execute(context.Background(), "E2"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	all, err := pstore.Query(context.Background(), ObservationFilter{Tenant: "tenant-a", TaskID: "E2"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 observation for the multi-model execution, got %d", len(all))
	}

	involvingX, usagesX := CountModelUsage(all, "", "", "Model X")
	involvingY, usagesY := CountModelUsage(all, "", "", "Model Y")
	if involvingX != 1 {
		t.Fatalf("execution involves Model X: expected true (1), got %d", involvingX)
	}
	if involvingY != 1 {
		t.Fatalf("execution involves Model Y: expected true (1), got %d", involvingY)
	}
	if usagesX != 2 {
		t.Fatalf("Model X usage count: expected 2, got %d", usagesX)
	}
	if usagesY != 1 {
		t.Fatalf("Model Y usage count: expected 1, got %d", usagesY)
	}

	// Execution-level quality must be counted once, not three times.
	pairX, err := AggregatePair(context.Background(), NewStoreDataset(pstore), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "Model X")
	if err != nil {
		t.Fatalf("AggregatePair: %v", err)
	}
	if pairX.ObservationCount != 1 {
		t.Fatalf("expected execution-level quality counted once, got ObservationCount=%d", pairX.ObservationCount)
	}
}

// TestNegative_ProviderSuppliedFakeExecutionIDOverwritten proves
// ExecutionResult.ExecutionID is Core-owned end to end: even if a
// (malicious or buggy) ExecutionProvider sets its own ExecutionID value
// on the result it returns, Executor overwrites it unconditionally with
// the ID it generated itself before ever calling the provider - exactly
// the same discipline ADR 0006 Phase 7C already proved for the result
// value itself, re-confirmed here at the joint-attribution layer.
func TestNegative_ProviderSuppliedFakeExecutionIDOverwritten(t *testing.T) {
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{
			TaskID: task.ID, Status: supervisor.TaskStatusCompleted,
			ExecutionID: "forged-execution-id", // the provider tries to claim its own ID
		}, nil
	}}
	dir := newTestDirectory(map[supervisor.AgentID][]string{"agent-A": {}})
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	exec := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, supervisor.DefaultSupervisorLimits(), state)
	setupPendingTask(t, state, "T1", "agent-A", "security_review", "tenant-a")

	result, err := exec.Execute(context.Background(), "T1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.ExecutionID == "forged-execution-id" {
		t.Fatalf("expected Executor to overwrite the provider-supplied ExecutionID, got it unchanged: %q", result.ExecutionID)
	}
	if result.ExecutionID == "" {
		t.Fatalf("expected a real Core-generated ExecutionID")
	}
}

// TestNegative_FindingCannotBindToAnotherTasksExecution proves that a
// FindingObservation whose SourceExecutionRef names a real
// ExecutionID - but under the WRONG TaskID - never finalizes that
// execution's observation: ComputeObservationID is derived from
// (Tenant, TaskID, ExecutionID, Kind), so a mismatched TaskID simply
// produces a different, nonexistent ID; ReviewFinalizationHook finds
// nothing to finalize rather than reaching into another task's record.
func TestNegative_FindingCannotBindToAnotherTasksExecution(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	in := NewObservationInput{
		Tenant: "tenant-a", TaskID: "T1", ExecutionID: "T1#real",
		TaskClass: TaskClassSecurityReview, AgentID: "agent-a", ExecutionOutcome: ExecutionCompleted,
		StartedAt: time.Now(), FinishedAt: time.Now(),
	}
	obs, _ := NewObservation(in)
	pstore.Save(context.Background(), obs)

	hook := NewReviewFinalizationHook(pstore, nil, nil)
	// The FindingObservation claims T1's real ExecutionID, but under
	// TaskID "T-OTHER" instead of "T1".
	group := review.ReviewGroup{
		ID: "G1", Tenant: "tenant-a", Status: "verified",
		Observations: []review.FindingObservation{
			{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T-OTHER", SourceExecutionRef: "T1#real", Stance: review.StanceConfirms},
		},
	}
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")

	// T1's real observation must remain untouched.
	real, _ := pstore.Get(context.Background(), ComputeObservationID("tenant-a", "T1", "T1#real", ObservationKindExecution))
	if real.Status != ObservationPending {
		t.Fatalf("expected T1's real observation to remain pending (never finalized by a mismatched-TaskID claim), got %+v", real)
	}
}

// TestNegative_EvidenceReferencingDifferentTaskNeverResolves proves
// resolveExecutionID's existing TaskID match check also protects against
// an EvidenceRecord that names the right tenant but a DIFFERENT task.
func TestNegative_EvidenceReferencingDifferentTaskNeverResolves(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	e1, _ := seedTwoPendingObservations(t, pstore, "T1", "tenant-a")
	evidence := review.NewMemoryEvidenceStore()
	// Evidence claims the RIGHT tenant and the RIGHT ExecutionID, but
	// the WRONG TaskID.
	evidence.Put(context.Background(), review.EvidenceRecord{ID: "EV1", Tenant: "tenant-a", TaskID: "T-DIFFERENT", ExecutionID: string(e1), Digest: "sha256:a"})

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
		t.Fatalf("expected evidence naming a different TaskID to never resolve attribution, got %+v", o1)
	}
}

// TestNegative_RejectedExecutionsNotOmittedFromPairMetrics proves
// rejected quality outcomes are counted, not survivorship-biased away,
// at the pair level too.
func TestNegative_RejectedExecutionsNotOmittedFromPairMetrics(t *testing.T) {
	store := NewMemoryPerformanceStore()
	seedObservationWithUsages(t, store, "tenant-a", "agent-A", TaskClassSecurityReview, "rej1", ExecutionCompleted, QualityRejected,
		[]ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}})

	summary, err := AggregatePair(context.Background(), NewStoreDataset(store), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "model-x")
	if err != nil {
		t.Fatalf("AggregatePair: %v", err)
	}
	if summary.RejectedCount != 1 {
		t.Fatalf("expected the rejected outcome counted, not omitted, got %+v", summary)
	}
}

// TestConcurrency_JointIntelligenceUnderConcurrentAccess exercises
// finalization, pair queries, and advisor reads happening concurrently
// against a shared store, under -race.
func TestConcurrency_JointIntelligenceUnderConcurrentAccess(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	for i := 0; i < 15; i++ {
		seedObservationWithUsages(t, pstore, "tenant-a", "agent-A", TaskClassSecurityReview, "seed"+string(rune('a'+i)), ExecutionCompleted, QualityVerified,
			[]ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "Model X", Count: 1}})
	}
	e1, e2 := seedTwoPendingObservations(t, pstore, "T1", "tenant-a")
	hook := NewReviewFinalizationHook(pstore, nil, nil)
	advisor := &DeterministicAdvisor{Dataset: NewStoreDataset(pstore), Tenant: "tenant-a", IncludePairEvidence: true}

	var wg sync.WaitGroup
	wg.Add(3)
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
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			if _, err := advisor.Advise(context.Background(), supervisor.Task{Type: string(TaskClassSecurityReview)}, []supervisor.AgentCandidate{{AgentID: "agent-A"}}); err != nil {
				t.Errorf("Advise: %v", err)
			}
			if _, err := AggregatePair(context.Background(), NewStoreDataset(pstore), ObservationFilter{Tenant: "tenant-a"}, "agent-A", TaskClassSecurityReview, "", "", "Model X"); err != nil {
				t.Errorf("AggregatePair: %v", err)
			}
		}
	}()
	wg.Wait()
}
