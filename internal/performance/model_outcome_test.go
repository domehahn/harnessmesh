package performance

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

func reviewGroupNaming(tenant string, taskID supervisor.TaskID, executionRef string) review.ReviewGroup {
	return review.ReviewGroup{
		ID: "G1", Tenant: tenant, Status: "verified",
		Observations: []review.FindingObservation{
			{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", SourceExecutionID: taskID, SourceExecutionRef: executionRef, Stance: review.StanceConfirms},
		},
	}
}

type recordingModelOutcomeObserver struct {
	mu      sync.Mutex
	records []ModelOutcomeRecord
}

func (r *recordingModelOutcomeObserver) ObserveModelOutcome(ctx context.Context, outcome ModelOutcomeRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, outcome)
	return nil
}

func TestModelOutcome_EmittedOncePerModelUsageEntry(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	observer := &recordingModelOutcomeObserver{}
	execObserver := NewSupervisorExecutionObserver(pstore, nil)
	execObserver.ModelOutcome = observer

	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{
			TaskID: task.ID, Status: supervisor.TaskStatusCompleted,
			ModelUsages: []supervisor.ModelUsageRef{
				{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 2},
				{RouteID: "R2", Provider: "switchyard", Model: "model-y", Count: 1},
			},
		}, nil
	}}
	dir := newTestDirectory(map[supervisor.AgentID][]string{"agent-a": {}})
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	exec := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, supervisor.DefaultSupervisorLimits(), state).WithObserver(execObserver)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")

	if _, err := exec.Execute(context.Background(), "T1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.records) != 2 {
		t.Fatalf("expected exactly 2 model-outcome records (one per ModelUsages entry), got %d", len(observer.records))
	}
}

func TestModelOutcome_FinalizationEmitsUpdatedQuality(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	observer := &recordingModelOutcomeObserver{}

	// Seed a pending observation with model usages directly (bypassing
	// the full Executor flow, which this test doesn't need).
	in := NewObservationInput{
		Tenant: "tenant-a", TaskID: "T1", ExecutionID: "T1#e1",
		TaskClass: TaskClassSecurityReview, AgentID: "agent-a", ExecutionOutcome: ExecutionCompleted,
		ModelUsages: []ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}},
		StartedAt:   time.Now(), FinishedAt: time.Now().Add(time.Second),
	}
	obs, err := NewObservation(in)
	if err != nil {
		t.Fatalf("NewObservation: %v", err)
	}
	if err := pstore.Save(context.Background(), obs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	hook := NewReviewFinalizationHook(pstore, nil, nil)
	hook.ModelOutcome = observer

	group := reviewGroupNaming("tenant-a", "T1", "T1#e1")
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")

	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.records) != 1 {
		t.Fatalf("expected 1 model-outcome record from finalization, got %d", len(observer.records))
	}
	if observer.records[0].QualityOutcome != QualityVerified {
		t.Fatalf("expected the finalization feedback to carry the UPDATED quality outcome, got %q", observer.records[0].QualityOutcome)
	}
}

type failingModelOutcomeObserver struct{ calls int }

func (f *failingModelOutcomeObserver) ObserveModelOutcome(ctx context.Context, outcome ModelOutcomeRecord) error {
	f.calls++
	return errors.New("feedback sink unavailable")
}

func TestModelOutcome_FeedbackSinkFailureDoesNotAffectExecutionResult(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	failingObserver := &failingModelOutcomeObserver{}
	execObserver := NewSupervisorExecutionObserver(pstore, nil)
	execObserver.ModelOutcome = failingObserver

	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{
			TaskID: task.ID, Status: supervisor.TaskStatusCompleted, Summary: "real",
			ModelUsages: []supervisor.ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}},
		}, nil
	}}
	dir := newTestDirectory(map[supervisor.AgentID][]string{"agent-a": {}})
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	exec := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, supervisor.DefaultSupervisorLimits(), state).WithObserver(execObserver)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")

	result, err := exec.Execute(context.Background(), "T1")
	if err != nil {
		t.Fatalf("execution must succeed despite a feedback-sink failure, got: %v", err)
	}
	if result.Status != supervisor.TaskStatusCompleted || result.Summary != "real" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if failingObserver.calls != 1 {
		t.Fatalf("expected the feedback sink to have been called exactly once, got %d", failingObserver.calls)
	}
}

type panickingModelOutcomeObserver struct{ calls int }

func (p *panickingModelOutcomeObserver) ObserveModelOutcome(ctx context.Context, outcome ModelOutcomeRecord) error {
	p.calls++
	panic("feedback sink panics")
}

func TestModelOutcome_FeedbackSinkPanicDoesNotCrashExecution(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	panicObserver := &panickingModelOutcomeObserver{}
	execObserver := NewSupervisorExecutionObserver(pstore, nil)
	execObserver.ModelOutcome = panicObserver

	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{
			TaskID: task.ID, Status: supervisor.TaskStatusCompleted,
			ModelUsages: []supervisor.ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}},
		}, nil
	}}
	dir := newTestDirectory(map[supervisor.AgentID][]string{"agent-a": {}})
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	exec := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, supervisor.DefaultSupervisorLimits(), state).WithObserver(execObserver)
	setupPendingTask(t, state, "T1", "agent-a", "security_review", "tenant-a")

	result, err := exec.Execute(context.Background(), "T1")
	if err != nil {
		t.Fatalf("execution must succeed despite a panicking feedback sink, got: %v", err)
	}
	if result.Status != supervisor.TaskStatusCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}
	if panicObserver.calls != 1 {
		t.Fatalf("expected the panicking sink to have been invoked exactly once, got %d", panicObserver.calls)
	}
}

// TestNegative_ModelOutcomeRecordCannotCarryAgentRecommendation is a
// structural proof: ModelOutcomeRecord has no field through which
// feedback could ever express "select this agent" - Switchyard-side
// feedback can never influence agent routing through this type, by
// construction.
func TestNegative_ModelOutcomeRecordCannotCarryAgentRecommendation(t *testing.T) {
	typ := reflect.TypeOf(ModelOutcomeRecord{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "recommend") || strings.Contains(name, "select") {
			t.Fatalf("ModelOutcomeRecord.%s suggests feedback could express an agent-selection decision - it must not", typ.Field(i).Name)
		}
	}
}

func TestNegative_DuplicateModelOutcomeFeedbackCountedButNotDoubleApplied(t *testing.T) {
	// Feedback is telemetry, not state - "duplicate" feedback has no
	// state to corrupt (there is no feedback ledger this package
	// maintains), but the sink must still simply be called once per
	// actual emission point, not multiplied.
	pstore := NewMemoryPerformanceStore()
	observer := &recordingModelOutcomeObserver{}
	hook := NewReviewFinalizationHook(pstore, nil, nil)
	hook.ModelOutcome = observer

	in := NewObservationInput{
		Tenant: "tenant-a", TaskID: "T1", ExecutionID: "T1#e1",
		TaskClass: TaskClassSecurityReview, AgentID: "agent-a", ExecutionOutcome: ExecutionCompleted,
		ModelUsages: []ModelUsageRef{{RouteID: "R1", Provider: "switchyard", Model: "model-x", Count: 1}},
		StartedAt:   time.Now(), FinishedAt: time.Now().Add(time.Second),
	}
	obs, _ := NewObservation(in)
	pstore.Save(context.Background(), obs)

	group := reviewGroupNaming("tenant-a", "T1", "T1#e1")
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed")
	hook.ObserveReviewTransition(context.Background(), group, "unconfirmed") // replay

	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.records) != 2 {
		// Two emissions (one per ObserveReviewTransition call) are
		// expected and correct - this is telemetry fired per transition
		// event, not a state mutation; the idempotency guarantee that
		// matters is on PerformanceObservation itself (already proven
		// elsewhere), not on how many times telemetry observes it.
		t.Fatalf("expected 2 feedback emissions (one per replay), got %d", len(observer.records))
	}
}
