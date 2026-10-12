package performance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// --- shared test harness: a real Executor wired to a real Scheduler-
// free CoreState, with a SupervisorExecutionObserver attached. ---

func newWiredExecutor(t *testing.T, agents map[supervisor.AgentID][]string, limits supervisor.SupervisorLimits, provider supervisor.ExecutionProvider, pstore PerformanceStore) (*testDirectory, *supervisor.CoreState, *supervisor.Executor) {
	t.Helper()
	dir := newTestDirectory(agents)
	observer := NewSupervisorExecutionObserver(pstore, nil)
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	exec := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, limits, state).WithObserver(observer)
	return dir, state, exec
}

// testDirectory is a minimal supervisor.AgentDirectory for this
// package's wiring tests - internal/performance has no access to
// internal/supervisor's own unexported test helpers, so a small local
// one is defined here instead of duplicating a full fake.
type testDirectory struct {
	agents map[supervisor.AgentID][]string
}

func newTestDirectory(agents map[supervisor.AgentID][]string) *testDirectory {
	return &testDirectory{agents: agents}
}
func (d *testDirectory) AgentExists(id supervisor.AgentID) bool {
	_, ok := d.agents[id]
	return ok
}
func (d *testDirectory) Capabilities(id supervisor.AgentID) ([]string, bool) {
	c, ok := d.agents[id]
	return c, ok
}

// TenantOf makes testDirectory also satisfy supervisor.TenantDirectory -
// every known agent here belongs to "tenant-a", matching every wiring
// test's task tenant, so these tests exercise the real execution/
// observation path without separately re-proving ADR 0002 Phase 2B's
// tenant fail-closed behavior (already proven in internal/supervisor's
// own test suite).
func (d *testDirectory) TenantOf(id supervisor.AgentID) (string, bool) {
	if _, ok := d.agents[id]; !ok {
		return "", false
	}
	return "tenant-a", true
}

// ListAgents makes testDirectory also satisfy supervisor.AgentCatalog,
// needed by the Scheduler-integration tests in this package that
// exercise the real DeterministicAdvisor end-to-end.
func (d *testDirectory) ListAgents(ctx context.Context) ([]supervisor.AgentContextSummary, error) {
	out := make([]supervisor.AgentContextSummary, 0, len(d.agents))
	for id, caps := range d.agents {
		out = append(out, supervisor.AgentContextSummary{AgentID: id, Capabilities: caps})
	}
	return out, nil
}

func setupPendingTask(t *testing.T, state *supervisor.CoreState, taskID supervisor.TaskID, agentID supervisor.AgentID, taskClass string, tenant string) {
	t.Helper()
	dir := newTestDirectory(map[supervisor.AgentID][]string{agentID: {}})
	v := supervisor.NewValidator(dir, nil, supervisor.DefaultSupervisorLimits())
	applier := supervisor.NewApplier(v, supervisor.AllowAllAuthorizer{}, state)
	if _, err := applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: taskID, Type: taskClass, Tenant: tenant}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.AssignTaskProposal{TaskID: taskID, CandidateAgentID: agentID, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("assign task: %v", err)
	}
}

func TestWiring_RealExecutorCreatesPendingObservation(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	_, state, exec := newWiredExecutor(t, map[supervisor.AgentID][]string{"agent-a": {}}, supervisor.DefaultSupervisorLimits(), provider, pstore)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")

	if _, err := exec.Execute(context.Background(), "T1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	results, err := pstore.Query(context.Background(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 observation, got %d", len(results))
	}
	if results[0].Status != ObservationPending {
		t.Fatalf("expected pending (no review yet), got %q", results[0].Status)
	}
	if results[0].ExecutionOutcome != ExecutionCompleted {
		t.Fatalf("expected completed, got %q", results[0].ExecutionOutcome)
	}
}

func TestWiring_FailedExecutionObserved(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{}, errors.New("boom")
	}}
	_, state, exec := newWiredExecutor(t, map[supervisor.AgentID][]string{"agent-a": {}}, supervisor.DefaultSupervisorLimits(), provider, pstore)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")

	if _, err := exec.Execute(context.Background(), "T1"); err == nil {
		t.Fatalf("expected Execute to return an error")
	}

	results, _ := pstore.Query(context.Background(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if len(results) != 1 || results[0].ExecutionOutcome != ExecutionFailed {
		t.Fatalf("expected 1 failed observation, got %+v", results)
	}
}

func TestWiring_TimeoutObserved(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		<-ctx.Done()
		return supervisor.ExecutionResult{}, ctx.Err()
	}}
	limits := supervisor.DefaultSupervisorLimits()
	limits.MaxExecutionTime = 20 * time.Millisecond
	_, state, exec := newWiredExecutor(t, map[supervisor.AgentID][]string{"agent-a": {}}, limits, provider, pstore)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")

	if _, err := exec.Execute(context.Background(), "T1"); err == nil {
		t.Fatalf("expected a timeout error")
	}

	results, _ := pstore.Query(context.Background(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if len(results) != 1 || results[0].ExecutionOutcome != ExecutionTimedOut {
		t.Fatalf("expected 1 timed_out observation, got %+v", results)
	}
}

func TestWiring_CancelledExecutionObserved(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		<-ctx.Done()
		return supervisor.ExecutionResult{}, ctx.Err()
	}}
	limits := supervisor.DefaultSupervisorLimits()
	limits.MaxExecutionTime = 5 * time.Second
	_, state, exec := newWiredExecutor(t, map[supervisor.AgentID][]string{"agent-a": {}}, limits, provider, pstore)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if _, err := exec.Execute(ctx, "T1"); err == nil {
		t.Fatalf("expected a cancellation-shaped error")
	}

	results, _ := pstore.Query(context.Background(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if len(results) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(results))
	}
	// Executor wraps a non-deadline context error as AgentExecutionFailure
	// (not context.Canceled directly) - ClassifyExecutionOutcome's
	// default branch maps that to ExecutionFailed; this is documented
	// (not every cancellation is independently distinguishable from a
	// generic failure once Executor has already classified it).
	if results[0].ExecutionOutcome != ExecutionFailed && results[0].ExecutionOutcome != ExecutionCancelled {
		t.Fatalf("expected failed or cancelled, got %q", results[0].ExecutionOutcome)
	}
}

func TestWiring_ReviewVerifiedFinalizesObservation(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	_, state, exec := newWiredExecutor(t, map[supervisor.AgentID][]string{"agent-a": {}}, supervisor.DefaultSupervisorLimits(), provider, pstore)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")
	execResult, err := exec.Execute(context.Background(), "T1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	reviewState := review.NewReviewState(nil)
	claim := review.ClaimInput{Subject: review.SubjectRef{Path: "auth.go"}, Category: "security", Line: 1}
	groupID, _ := reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: string(execResult.ExecutionID), ArtifactVersion: "v1", Stance: review.StanceConfirms})
	reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", SourceExecutionID: "T2", ArtifactVersion: "v1", Stance: review.StanceConfirms})

	hook := NewReviewFinalizationHook(pstore, nil, nil)
	applier := review.NewReviewApplier(review.NewReviewValidator(review.DefaultReviewLimits()), review.NewReviewAuthorizer(), reviewState).WithObserver(hook)
	if _, err := applier.Apply(context.Background(), groupID, []review.ArbiterAction{
		review.RecommendVerificationAction{GroupID: groupID, RationaleSummary: "2 independent confirms"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	results, _ := pstore.Query(context.Background(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if len(results) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(results))
	}
	if results[0].Status != ObservationFinalized || results[0].QualityOutcome != QualityVerified {
		t.Fatalf("expected finalized+verified, got %+v", results[0])
	}
}

func TestWiring_ReviewRejectedFinalizesObservation(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	_, state, exec := newWiredExecutor(t, map[supervisor.AgentID][]string{"agent-a": {}}, supervisor.DefaultSupervisorLimits(), provider, pstore)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")
	execResult, _ := exec.Execute(context.Background(), "T1")

	reviewState := review.NewReviewState(nil)
	claim := review.ClaimInput{Subject: review.SubjectRef{Path: "auth.go"}, Category: "security", Line: 1}
	groupID, _ := reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: string(execResult.ExecutionID), ArtifactVersion: "v1", Stance: review.StanceRefutes})
	reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", SourceExecutionID: "T2", ArtifactVersion: "v1", Stance: review.StanceRefutes})

	hook := NewReviewFinalizationHook(pstore, nil, nil)
	applier := review.NewReviewApplier(review.NewReviewValidator(review.DefaultReviewLimits()), review.NewReviewAuthorizer(), reviewState).WithObserver(hook)
	if _, err := applier.Apply(ctx(), groupID, []review.ArbiterAction{
		review.RejectClaimAction{GroupID: groupID, RationaleSummary: "2 independent refutes"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	results, _ := pstore.Query(ctx(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if len(results) != 1 || results[0].Status != ObservationFinalized || results[0].QualityOutcome != QualityRejected {
		t.Fatalf("expected finalized+rejected, got %+v", results)
	}
}

func ctx() context.Context { return context.Background() }

func TestNegative_DuplicateExecutionCallbackIdempotent(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	observer := NewSupervisorExecutionObserver(pstore, nil)
	task := supervisor.Task{ID: "T1", Type: "security_review", Tenant: "tenant-a"}
	result := supervisor.ExecutionResult{TaskID: "T1", Status: supervisor.TaskStatusCompleted}
	started := time.Now().UTC()
	finished := started.Add(time.Second)

	// Simulate the SAME completed-execution event being delivered twice
	// (event redelivery) - identical executionID/startedAt/finishedAt,
	// exactly as a real redelivery of one Executor.Execute call's
	// completed-event notification would carry forward the SAME
	// Core-generated ExecutionID both times.
	executionID := supervisor.NewExecutionID("T1")
	observer.ObserveExecution(ctx(), task, "agent-a", executionID, result, nil, started, finished)
	observer.ObserveExecution(ctx(), task, "agent-a", executionID, result, nil, started, finished)

	results, _ := pstore.Query(ctx(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 observation after a duplicate callback, got %d", len(results))
	}
}

func TestNegative_RetryCreatesDistinctObservation(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	observer := NewSupervisorExecutionObserver(pstore, nil)
	task := supervisor.Task{ID: "T1", Type: "security_review", Tenant: "tenant-a"}

	// Two separate attempts get two distinct Core-generated ExecutionIDs
	// (as Executor.Execute would produce by calling NewExecutionID once
	// per real attempt) - this, not timing, is what distinguishes a
	// genuine retry from a redelivered duplicate.
	observer.ObserveExecution(ctx(), task, "agent-a", supervisor.NewExecutionID("T1"), supervisor.ExecutionResult{Status: supervisor.TaskStatusFailed}, nil, time.Now().UTC(), time.Now().UTC())
	observer.ObserveExecution(ctx(), task, "agent-a", supervisor.NewExecutionID("T1"), supervisor.ExecutionResult{Status: supervisor.TaskStatusCompleted}, nil, time.Now().UTC(), time.Now().UTC())

	results, _ := pstore.Query(ctx(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if len(results) != 2 {
		t.Fatalf("expected 2 distinct observations for 2 separate attempts (different ExecutionID), got %d", len(results))
	}
}

func TestNegative_MetricsStoreFailureDoesNotAffectExecutionResult(t *testing.T) {
	failing := &failingPerformanceStore{}
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted, Summary: "real result"}, nil
	}}
	_, state, exec := newWiredExecutor(t, map[supervisor.AgentID][]string{"agent-a": {}}, supervisor.DefaultSupervisorLimits(), provider, failing)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")

	result, err := exec.Execute(ctx(), "T1")
	if err != nil {
		t.Fatalf("execution truth must be unaffected by a metrics-store failure, got error: %v", err)
	}
	if result.Status != supervisor.TaskStatusCompleted || result.Summary != "real result" {
		t.Fatalf("unexpected execution result: %+v", result)
	}
	snap := state.Snapshot()
	finalTask, _ := snap.Tasks.ByID("T1")
	if finalTask.Status != supervisor.TaskStatusCompleted {
		t.Fatalf("expected the task itself to remain Completed despite the metrics failure, got %q", finalTask.Status)
	}
}

type failingPerformanceStore struct{}

func (f *failingPerformanceStore) Save(ctx context.Context, obs PerformanceObservation) error {
	return errors.New("store unavailable")
}
func (f *failingPerformanceStore) Get(ctx context.Context, id ObservationID) (PerformanceObservation, error) {
	return PerformanceObservation{}, errors.New("store unavailable")
}
func (f *failingPerformanceStore) Query(ctx context.Context, filter ObservationFilter) ([]PerformanceObservation, error) {
	return nil, errors.New("store unavailable")
}

var _ PerformanceStore = (*failingPerformanceStore)(nil)

func TestNegative_WrongTenantObservationStillCreatedButIsolated(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	_, state, exec := newWiredExecutor(t, map[supervisor.AgentID][]string{"agent-a": {}}, supervisor.DefaultSupervisorLimits(), provider, pstore)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")
	exec.Execute(ctx(), "T1")

	// Querying under the WRONG tenant must never see it.
	wrongTenant, err := pstore.Query(ctx(), ObservationFilter{Tenant: "tenant-b", TaskID: "T1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(wrongTenant) != 0 {
		t.Fatalf("expected 0 results under the wrong tenant, got %d", len(wrongTenant))
	}
}

func TestNegative_MissingTaskClassRecordsAttributionFailureNotFabrication(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	observer := NewSupervisorExecutionObserver(pstore, nil)
	task := supervisor.Task{ID: "T1", Type: "", Tenant: "tenant-a"} // no task class
	observer.ObserveExecution(ctx(), task, "agent-a", supervisor.NewExecutionID("T1"), supervisor.ExecutionResult{Status: supervisor.TaskStatusCompleted}, nil, time.Now(), time.Now())

	results, _ := pstore.Query(ctx(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if len(results) != 0 {
		t.Fatalf("expected no observation to be fabricated for a missing task class, got %d", len(results))
	}
}

func TestNegative_MissingExecutionAttributionRejected(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	observer := NewSupervisorExecutionObserver(pstore, nil)
	task := supervisor.Task{ID: "T1", Type: "security_review", Tenant: "tenant-a"}
	observer.ObserveExecution(ctx(), task, "", supervisor.NewExecutionID("T1"), supervisor.ExecutionResult{Status: supervisor.TaskStatusCompleted}, nil, time.Now(), time.Now())

	results, _ := pstore.Query(ctx(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if len(results) != 0 {
		t.Fatalf("expected no observation for missing agent attribution, got %d", len(results))
	}
}

func TestNegative_StaleReviewFinalizationDoesNotCorruptLatestObservation(t *testing.T) {
	// If an OLDER review transition is (re-)delivered after a newer one
	// already finalized the observation, re-finalizing with the SAME
	// terminal data is a safe, idempotent no-op - the anti-revert guard
	// in Save prevents a stale write from reverting a finalized record
	// to pending, and finalizing twice with the SAME verdict is harmless.
	pstore := NewMemoryPerformanceStore()
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	_, state, exec := newWiredExecutor(t, map[supervisor.AgentID][]string{"agent-a": {}}, supervisor.DefaultSupervisorLimits(), provider, pstore)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")
	execResult, _ := exec.Execute(ctx(), "T1")

	reviewState := review.NewReviewState(nil)
	claim := review.ClaimInput{Subject: review.SubjectRef{Path: "auth.go"}, Category: "security", Line: 1}
	groupID, _ := reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: "T1", SourceExecutionRef: string(execResult.ExecutionID), ArtifactVersion: "v1", Stance: review.StanceConfirms})
	reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", SourceExecutionID: "T2", ArtifactVersion: "v1", Stance: review.StanceConfirms})

	hook := NewReviewFinalizationHook(pstore, nil, nil)
	applier := review.NewReviewApplier(review.NewReviewValidator(review.DefaultReviewLimits()), review.NewReviewAuthorizer(), reviewState).WithObserver(hook)
	group, err := applier.Apply(ctx(), groupID, []review.ArbiterAction{review.RecommendVerificationAction{GroupID: groupID, RationaleSummary: "r"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Directly re-invoke the hook with the SAME (now stale, but
	// identical) group snapshot, simulating a replay.
	hook.ObserveReviewTransition(ctx(), group, "unconfirmed")

	results, _ := pstore.Query(ctx(), ObservationFilter{Tenant: "tenant-a", TaskID: "T1"})
	if len(results) != 1 || results[0].Status != ObservationFinalized || results[0].QualityOutcome != QualityVerified {
		t.Fatalf("expected the single observation to remain correctly finalized, got %+v", results)
	}
}
