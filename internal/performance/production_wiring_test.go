package performance

import (
	"context"
	"testing"

	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// newProductionSystem wires the real Scheduler/Executor/ReviewApplier
// through ONE ProductionEvaluationCoordinator plugged into all three
// hook points - the exact "one authoritative production shadow-
// evaluation path" ADR 0009 requires.
func newProductionSystem(t *testing.T, agents map[supervisor.AgentID][]string, challengers []RoutingPolicy) (*supervisor.Scheduler, *supervisor.CoreState, *ProductionEvaluationCoordinator, PerformanceStore, EvaluationStore, CandidateSnapshotStore) {
	t.Helper()
	dir := newTestDirectory(agents)
	limits := supervisor.DefaultSupervisorLimits()
	v := supervisor.NewValidator(dir, nil, limits)
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	applier := supervisor.NewApplier(v, supervisor.AllowAllAuthorizer{}, state)
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}

	pstore := NewMemoryPerformanceStore()
	evalStore := NewMemoryEvaluationStore()
	snapStore := NewMemoryCandidateSnapshotStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	execObserver := NewSupervisorExecutionObserver(pstore, nil)
	reviewHook := NewReviewFinalizationHook(pstore, nil, nil)
	coordinator := NewProductionEvaluationCoordinator(execObserver, reviewHook, snapStore, evaluator, challengers, DefaultEvaluationLimits(), nil)

	executor := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, limits, state).WithObserver(coordinator)
	sched := supervisor.NewScheduler(applier, executor, dir, limits).WithEvaluation(coordinator, supervisor.EvaluationShadow)

	return sched, state, coordinator, pstore, evalStore, snapStore
}

func TestProductionWiring_AutomaticSnapshotNoManualCall(t *testing.T) {
	sched, state, _, _, _, snapStore := newProductionSystem(t, map[supervisor.AgentID][]string{"agent-A": {}, "agent-B": {}}, nil)

	if _, err := sched.Applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: "T1", Type: "security_review", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	_ = state

	snaps, err := snapStore.ListByTaskID(context.Background(), "tenant-a", "T1")
	if err != nil {
		t.Fatalf("ListByTaskID: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected the real Scheduler selection to automatically create exactly 1 CandidateSnapshot, with no manual snapshot call anywhere in this test, got %d", len(snaps))
	}
	snap := snaps[0]
	if snap.DecisionID == "" {
		t.Fatalf("expected the automatically-created snapshot to carry a Core-owned DecisionID")
	}
	if len(snap.AgentIDs) != 2 {
		t.Fatalf("expected both eligible agents in the snapshot, got %v", snap.AgentIDs)
	}
}

// mixedTenantDirectory is a minimal supervisor.AgentCatalog with
// per-agent tenant assignment, needed to exercise real cross-tenant
// exclusion at the production-snapshot level (testDirectory's TenantOf
// always returns a single fixed tenant for every known agent).
type mixedTenantDirectory struct {
	agents  map[supervisor.AgentID][]string
	tenants map[supervisor.AgentID]string
}

func (d *mixedTenantDirectory) AgentExists(id supervisor.AgentID) bool {
	_, ok := d.agents[id]
	return ok
}
func (d *mixedTenantDirectory) Capabilities(id supervisor.AgentID) ([]string, bool) {
	c, ok := d.agents[id]
	return c, ok
}
func (d *mixedTenantDirectory) TenantOf(id supervisor.AgentID) (string, bool) {
	t, ok := d.tenants[id]
	return t, ok
}
func (d *mixedTenantDirectory) ListAgents(ctx context.Context) ([]supervisor.AgentContextSummary, error) {
	out := make([]supervisor.AgentContextSummary, 0, len(d.agents))
	for id, caps := range d.agents {
		out = append(out, supervisor.AgentContextSummary{AgentID: id, Capabilities: caps})
	}
	return out, nil
}

func TestProductionWiring_ForeignTenantCandidateAbsentFromSnapshot(t *testing.T) {
	dir := &mixedTenantDirectory{
		agents:  map[supervisor.AgentID][]string{"agent-A": {}, "agent-foreign": {}},
		tenants: map[supervisor.AgentID]string{"agent-A": "tenant-a", "agent-foreign": "tenant-b"},
	}
	limits := supervisor.DefaultSupervisorLimits()
	v := supervisor.NewValidator(dir, nil, limits)
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	applier := supervisor.NewApplier(v, supervisor.AllowAllAuthorizer{}, state)
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	pstore := NewMemoryPerformanceStore()
	evalStore := NewMemoryEvaluationStore()
	snapStore := NewMemoryCandidateSnapshotStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	coordinator := NewProductionEvaluationCoordinator(NewSupervisorExecutionObserver(pstore, nil), NewReviewFinalizationHook(pstore, nil, nil), snapStore, evaluator, nil, DefaultEvaluationLimits(), nil)
	executor := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, limits, state).WithObserver(coordinator)
	sched := supervisor.NewScheduler(applier, executor, dir, limits).WithEvaluation(coordinator, supervisor.EvaluationShadow)

	if _, err := applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: "T1", Type: "security_review", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	snaps, _ := snapStore.ListByTaskID(context.Background(), "tenant-a", "T1")
	if len(snaps) != 1 {
		t.Fatalf("expected exactly 1 snapshot, got %d", len(snaps))
	}
	snap := snaps[0]
	for _, id := range snap.AgentIDs {
		if id == "agent-foreign" {
			t.Fatalf("a foreign-tenant agent must never appear in the production snapshot")
		}
	}
}

func TestProductionWiring_SnapshotStableDespiteLaterRegistryChange(t *testing.T) {
	agents := map[supervisor.AgentID][]string{"agent-A": {}, "agent-B": {}}
	sched, _, _, _, _, snapStore := newProductionSystem(t, agents, nil)

	if _, err := sched.Applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: "T1", Type: "security_review", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Mutate the live registry AFTER the decision - this must not
	// affect the already-persisted snapshot.
	agents["agent-C"] = []string{}
	delete(agents, "agent-B")

	snaps, _ := snapStore.ListByTaskID(context.Background(), "tenant-a", "T1")
	if len(snaps) != 1 {
		t.Fatalf("expected exactly 1 snapshot, got %d", len(snaps))
	}
	snap := snaps[0]
	if len(snap.AgentIDs) != 2 || snap.AgentIDs[0] != "agent-A" || snap.AgentIDs[1] != "agent-B" {
		t.Fatalf("expected the ORIGINAL [agent-A, agent-B] snapshot preserved despite the later registry change, got %v", snap.AgentIDs)
	}
}

func TestProductionWiring_ChampionAndChallengerSeeIdenticalSnapshot(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	seedFinalized(t, pstore, "tenant-a", "agent-B", TaskClassSecurityReview, 20, 18, 1)
	challenger := NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: NewStoreDataset(pstore), Tenant: "tenant-a"}, challengerDesc())

	sched, _, _, _, evalStore, snapStore := newProductionSystem(t, map[supervisor.AgentID][]string{"agent-A": {}, "agent-B": {}}, []RoutingPolicy{challenger})

	if _, err := sched.Applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: "T1", Type: "security_review", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	all, err := evalStore.Query(context.Background(), EvaluationFilter{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 automatic evaluation record, got %d", len(all))
	}
	rec := all[0]
	if rec.ChampionPolicy.ID != supervisor.LegacyDeterministicPolicyID {
		t.Fatalf("expected the legacy deterministic champion policy captured, got %+v", rec.ChampionPolicy)
	}
	if rec.ChampionAgentID != "agent-A" {
		t.Fatalf("expected champion agent-A, got %q", rec.ChampionAgentID)
	}

	snaps, _ := snapStore.ListByTaskID(context.Background(), "tenant-a", "T1")
	if len(snaps) != 1 {
		t.Fatalf("expected exactly 1 snapshot, got %d", len(snaps))
	}
	snap := snaps[0]
	if len(rec.CandidateAgentIDs) != len(snap.AgentIDs) {
		t.Fatalf("expected the evaluation record's candidate list to match the snapshot exactly, got %v vs %v", rec.CandidateAgentIDs, snap.AgentIDs)
	}
}

func TestProductionWiring_AutomaticOutcomeAttachment_ExecutionAndReview(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	seedFinalized(t, pstore, "tenant-a", "agent-B", TaskClassSecurityReview, 20, 18, 1)
	challenger := NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: NewStoreDataset(pstore), Tenant: "tenant-a"}, challengerDesc())

	dir := newTestDirectory(map[supervisor.AgentID][]string{"agent-A": {}, "agent-B": {}})
	limits := supervisor.DefaultSupervisorLimits()
	v := supervisor.NewValidator(dir, nil, limits)
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	applier := supervisor.NewApplier(v, supervisor.AllowAllAuthorizer{}, state)
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	evalStore := NewMemoryEvaluationStore()
	snapStore := NewMemoryCandidateSnapshotStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	execObserver := NewSupervisorExecutionObserver(pstore, nil)
	reviewHook := NewReviewFinalizationHook(pstore, nil, nil)
	coordinator := NewProductionEvaluationCoordinator(execObserver, reviewHook, snapStore, evaluator, []RoutingPolicy{challenger}, DefaultEvaluationLimits(), nil)
	executor := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, limits, state).WithObserver(coordinator)
	sched := supervisor.NewScheduler(applier, executor, dir, limits).WithEvaluation(coordinator, supervisor.EvaluationShadow)

	if _, err := applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: "T1", Type: "security_review", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	all, _ := evalStore.Query(context.Background(), EvaluationFilter{Tenant: "tenant-a"})
	if len(all) != 1 {
		t.Fatalf("expected 1 record, got %d", len(all))
	}
	afterExecution, err := evalStore.Get(context.Background(), all[0].EvaluationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !afterExecution.OutcomeAttached || afterExecution.ActualExecutionOutcome != ExecutionCompleted {
		t.Fatalf("expected the execution outcome automatically attached, got %+v", afterExecution)
	}
	if afterExecution.ActualQualityOutcome != QualityUnreviewed {
		t.Fatalf("expected quality still unreviewed immediately after execution, got %q", afterExecution.ActualQualityOutcome)
	}

	// Now drive review finalization for the SAME execution, via the
	// REAL ExecutionID the coordinator captured automatically.
	reviewState := review.NewReviewState(nil)
	claim := review.ClaimInput{Subject: review.SubjectRef{Path: "auth.go"}, Category: "security", Line: 1}
	groupID, err := reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{
		ID: "O1", FindingID: "F1", SourceAgentID: "agent-A", SourceExecutionID: "T1",
		SourceExecutionRef: afterExecution.ExecutionID, ArtifactVersion: "v1", Stance: review.StanceConfirms,
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-B", SourceExecutionID: "T2", ArtifactVersion: "v1", Stance: review.StanceConfirms})

	reviewApplier := review.NewReviewApplier(review.NewReviewValidator(review.DefaultReviewLimits()), review.NewReviewAuthorizer(), reviewState).WithObserver(coordinator)
	if _, err := reviewApplier.Apply(context.Background(), groupID, []review.ArbiterAction{
		review.RecommendVerificationAction{GroupID: groupID, RationaleSummary: "2 independent confirms"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	final, err := evalStore.Get(context.Background(), all[0].EvaluationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if final.ActualQualityOutcome != QualityVerified {
		t.Fatalf("expected the quality outcome automatically attached after review finalization, got %q", final.ActualQualityOutcome)
	}
	if final.ActualExecutionOutcome != ExecutionCompleted {
		t.Fatalf("expected the execution outcome preserved (not overwritten) by the quality update, got %q", final.ActualExecutionOutcome)
	}
}

func TestProductionWiring_IdempotentRepeatedHooks(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	seedFinalized(t, pstore, "tenant-a", "agent-A", TaskClassSecurityReview, 20, 18, 1)
	challenger := NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: NewStoreDataset(pstore), Tenant: "tenant-a"}, challengerDesc())
	sched, _, _, _, evalStore, _ := newProductionSystem(t, map[supervisor.AgentID][]string{"agent-A": {}}, []RoutingPolicy{challenger})

	if _, err := sched.Applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: "T1", Type: "security_review", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	// A second Tick with nothing newly ready to dispatch (the task is
	// already Completed) must not produce a second decision, and
	// therefore not a second/duplicate evaluation record.
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	all, err := evalStore.Query(context.Background(), EvaluationFilter{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 evaluation record after 2 ticks (no re-dispatch of a completed task), got %d", len(all))
	}
}
