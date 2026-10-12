package performance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// --- Persistence identity (ADR 0009D Part 21) ---

func TestDecisionScoped_TwoDecisionsSameTaskDifferentSnapshotsCoexist(t *testing.T) {
	store := NewMemoryCandidateSnapshotStore()
	d1 := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())
	d2 := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-C"}, time.Now())
	if d1.DecisionID == d2.DecisionID {
		t.Fatalf("expected two independently-captured decisions to have distinct DecisionIDs")
	}
	if err := store.Save(context.Background(), d1); err != nil {
		t.Fatalf("Save D1: %v", err)
	}
	if err := store.Save(context.Background(), d2); err != nil {
		t.Fatalf("Save D2: %v", err)
	}

	got1, found, err := store.Get(context.Background(), "tenant-a", d1.DecisionID)
	if err != nil || !found {
		t.Fatalf("Get D1: found=%v err=%v", found, err)
	}
	if len(got1.AgentIDs) != 2 || got1.AgentIDs[1] != "agent-B" {
		t.Fatalf("expected D1's own snapshot [A,B], got %v", got1.AgentIDs)
	}
	got2, found, err := store.Get(context.Background(), "tenant-a", d2.DecisionID)
	if err != nil || !found {
		t.Fatalf("Get D2: found=%v err=%v", found, err)
	}
	if len(got2.AgentIDs) != 2 || got2.AgentIDs[1] != "agent-C" {
		t.Fatalf("expected D2's own snapshot [A,C], got %v", got2.AgentIDs)
	}
}

func TestDecisionScoped_TwoDecisionsSameTaskSameSnapshotCoexist(t *testing.T) {
	store := NewMemoryCandidateSnapshotStore()
	capturedAt := time.Now()
	d1 := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, capturedAt)
	d2 := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, capturedAt)
	// Same TaskID, same candidate universe, same capture moment - but
	// two SEPARATE decision events (e.g. two retries that both saw an
	// unchanged registry) - distinct DecisionIDs, same fingerprint.
	if d1.DecisionID == d2.DecisionID {
		t.Fatalf("expected distinct DecisionIDs even with identical snapshot content")
	}
	if err := store.Save(context.Background(), d1); err != nil {
		t.Fatalf("Save D1: %v", err)
	}
	if err := store.Save(context.Background(), d2); err != nil {
		t.Fatalf("Save D2 (same content, different decision): %v", err)
	}
	all, err := store.ListByTaskID(context.Background(), "tenant-a", "T1")
	if err != nil {
		t.Fatalf("ListByTaskID: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected both decisions to persist independently despite identical snapshot content, got %d", len(all))
	}
}

func TestDecisionScoped_SameDecisionReplayIdempotent(t *testing.T) {
	store := NewMemoryCandidateSnapshotStore()
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	if err := store.Save(context.Background(), snap); err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	if err := store.Save(context.Background(), snap); err != nil { // exact replay
		t.Fatalf("Save 2 (replay): %v", err)
	}
	all, _ := store.ListByTaskID(context.Background(), "tenant-a", "T1")
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 snapshot after an idempotent replay, got %d", len(all))
	}
}

func TestNegative_SameDecisionDifferentSnapshotConflicts(t *testing.T) {
	store := NewMemoryCandidateSnapshotStore()
	decisionID := supervisor.NewDecisionID("T1")
	snap1 := testSnapshotForDecision(t, decisionID, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	snap2 := testSnapshotForDecision(t, decisionID, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())

	if err := store.Save(context.Background(), snap1); err != nil {
		t.Fatalf("Save 1: %v", err)
	}
	err := store.Save(context.Background(), snap2)
	if err == nil {
		t.Fatalf("expected a conflict when the SAME DecisionID is saved with DIFFERENT snapshot content")
	}
	if _, ok := err.(*DecisionSnapshotConflict); !ok {
		t.Fatalf("expected *DecisionSnapshotConflict, got %T: %v", err, err)
	}
}

// --- Lookup (ADR 0009D Part 21) ---

func TestDecisionScoped_ListTaskReturnsAllDecisions(t *testing.T) {
	store := NewMemoryCandidateSnapshotStore()
	d1 := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	d2 := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-B"}, time.Now())
	d3 := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-C"}, time.Now())
	for _, d := range []CandidateSnapshot{d1, d2, d3} {
		if err := store.Save(context.Background(), d); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	all, err := store.ListByTaskID(context.Background(), "tenant-a", "T1")
	if err != nil {
		t.Fatalf("ListByTaskID: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected all 3 decisions for Task T1, got %d", len(all))
	}
}

func TestNegative_CrossTenantDecisionLookupFailsClosed(t *testing.T) {
	store := NewMemoryCandidateSnapshotStore()
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	store.Save(context.Background(), snap)

	_, found, err := store.Get(context.Background(), "tenant-b", snap.DecisionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatalf("expected a cross-tenant DecisionID lookup to never find tenant-a's decision under tenant-b")
	}
}

// --- Evaluation binding (ADR 0009D Part 21) ---

func TestDecisionScoped_EvaluationsRemainBoundToOwnSnapshot(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	seedFinalized(t, pstore, "tenant-a", "agent-B", TaskClassSecurityReview, 20, 18, 1)
	seedFinalized(t, pstore, "tenant-a", "agent-C", TaskClassSecurityReview, 20, 2, 17)
	challenger := NewAdvisorRoutingPolicy(&DeterministicAdvisor{Dataset: NewStoreDataset(pstore), Tenant: "tenant-a"}, challengerDesc())

	evalStore := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)

	d1 := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())
	d2 := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-C"}, time.Now())

	rec1, err := evaluator.Evaluate(context.Background(), d1, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
	if err != nil {
		t.Fatalf("Evaluate D1: %v", err)
	}
	rec2, err := evaluator.Evaluate(context.Background(), d2, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
	if err != nil {
		t.Fatalf("Evaluate D2: %v", err)
	}

	if rec1[0].DecisionID != d1.DecisionID {
		t.Fatalf("expected D1's evaluation bound to D1, got %q", rec1[0].DecisionID)
	}
	if rec2[0].DecisionID != d2.DecisionID {
		t.Fatalf("expected D2's evaluation bound to D2, got %q", rec2[0].DecisionID)
	}
	if rec1[0].ChallengerAgentID != "agent-B" {
		t.Fatalf("expected D1's challenger recommendation to reflect D1's own candidate set (agent-B), got %q", rec1[0].ChallengerAgentID)
	}
	if rec2[0].ChallengerAgentID != "agent-C" {
		t.Fatalf("expected D2's challenger recommendation to reflect D2's own candidate set (agent-C), got %q", rec2[0].ChallengerAgentID)
	}
	if rec1[0].EvaluationID == rec2[0].EvaluationID {
		t.Fatalf("expected EvaluationIDs to differ across decisions")
	}
}

func TestNegative_EvaluationIDsDoNotCollideAcrossDecisionsWithSameChampionAndChallenger(t *testing.T) {
	store := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(store, DefaultEvaluationLimits(), nil)
	challenger := StaticRoutingPolicy{Descriptor_: challengerDesc(), AgentID: "agent-A"}

	// Two decisions, SAME TaskID, IDENTICAL candidate set and capture
	// time (so snapshot.fingerprint() is identical) - EvaluationIDs
	// must still differ because DecisionID differs.
	capturedAt := time.Now()
	d1 := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, capturedAt)
	d2 := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, capturedAt)

	rec1, err := evaluator.Evaluate(context.Background(), d1, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
	if err != nil {
		t.Fatalf("Evaluate D1: %v", err)
	}
	rec2, err := evaluator.Evaluate(context.Background(), d2, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
	if err != nil {
		t.Fatalf("Evaluate D2: %v", err)
	}
	if rec1[0].EvaluationID == rec2[0].EvaluationID {
		t.Fatalf("expected EvaluationIDs to differ across decisions with identical snapshot fingerprints")
	}
	all, _ := store.Query(context.Background(), EvaluationFilter{Tenant: "tenant-a"})
	if len(all) != 2 {
		t.Fatalf("expected both evaluation records to persist independently, got %d", len(all))
	}
}

// --- Execution binding (ADR 0009D Part 21) ---

func TestDecisionScoped_E1ReviewCannotChangeD2Evaluation(t *testing.T) {
	pstore := NewMemoryPerformanceStore()
	evalStore := NewMemoryEvaluationStore()
	snapStore := NewMemoryCandidateSnapshotStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	challenger := StaticRoutingPolicy{Descriptor_: challengerDesc(), AgentID: "agent-A"}

	dir := newTestDirectory(map[supervisor.AgentID][]string{"agent-A": {}})
	limits := supervisor.DefaultSupervisorLimits()
	v := supervisor.NewValidator(dir, nil, limits)
	state := supervisor.NewCoreState(supervisor.TaskGraph{}, nil)
	applier := supervisor.NewApplier(v, supervisor.AllowAllAuthorizer{}, state)
	provider := &supervisor.FakeExecutionProvider{ExecuteFunc: func(ctx context.Context, agentID supervisor.AgentID, task supervisor.Task) (supervisor.ExecutionResult, error) {
		return supervisor.ExecutionResult{TaskID: task.ID, Status: supervisor.TaskStatusCompleted}, nil
	}}
	coordinator := NewProductionEvaluationCoordinator(NewSupervisorExecutionObserver(pstore, nil), NewReviewFinalizationHook(pstore, nil, nil), snapStore, evaluator, []RoutingPolicy{challenger}, DefaultEvaluationLimits(), nil)
	executor := supervisor.NewExecutor(dir, supervisor.AllowAllAuthorizer{}, provider, limits, state).WithObserver(coordinator)
	sched := supervisor.NewScheduler(applier, executor, dir, limits).WithEvaluation(coordinator, supervisor.EvaluationShadow)

	// D1 -> E1 (task T1)
	if _, err := applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: "T1", Type: "security_review", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create T1: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick T1: %v", err)
	}
	// D2 -> E2 (task T2, a DIFFERENT task, so both decisions coexist
	// concurrently in the stores - the point is that their EvaluationIDs/
	// outcomes must never cross-bind regardless).
	if _, err := applier.Apply(context.Background(), []supervisor.AgentProposal{
		supervisor.CreateTaskProposal{Task: supervisor.Task{ID: "T2", Type: "security_review", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create T2: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick T2: %v", err)
	}

	allT1, _ := evalStore.Query(context.Background(), EvaluationFilter{Tenant: "tenant-a"})
	var recT1, recT2 RoutingEvaluationRecord
	for _, r := range allT1 {
		if r.TaskID == "T1" {
			recT1 = r
		}
		if r.TaskID == "T2" {
			recT2 = r
		}
	}
	if recT1.ExecutionID == "" || recT2.ExecutionID == "" {
		t.Fatalf("expected both decisions' outcomes auto-attached")
	}
	if recT1.ExecutionID == recT2.ExecutionID {
		t.Fatalf("expected distinct ExecutionIDs for distinct decisions")
	}

	// A review resolving to E1 (T1's execution) must never touch D2/T2's record.
	reviewState := review.NewReviewState(nil)
	claim := review.ClaimInput{Subject: review.SubjectRef{Path: "x.go"}, Category: "security", Line: 1}
	groupID, err := reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{
		ID: "O1", FindingID: "F1", SourceAgentID: "agent-A", SourceExecutionID: "T1",
		SourceExecutionRef: recT1.ExecutionID, ArtifactVersion: "v1", Stance: review.StanceConfirms,
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	reviewState.Ingest(claim, "v1", "tenant-a", review.FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-B", SourceExecutionID: "T-other", ArtifactVersion: "v1", Stance: review.StanceConfirms})
	reviewApplier := review.NewReviewApplier(review.NewReviewValidator(review.DefaultReviewLimits()), review.NewReviewAuthorizer(), reviewState).WithObserver(coordinator)
	if _, err := reviewApplier.Apply(context.Background(), groupID, []review.ArbiterAction{
		review.RecommendVerificationAction{GroupID: groupID, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	finalT2, err := evalStore.Get(context.Background(), recT2.EvaluationID)
	if err != nil {
		t.Fatalf("Get T2: %v", err)
	}
	if finalT2.ActualQualityOutcome == QualityVerified {
		t.Fatalf("expected T1's review to never alter T2's (D2's) evaluation quality, got %q", finalT2.ActualQualityOutcome)
	}
	finalT1, err := evalStore.Get(context.Background(), recT1.EvaluationID)
	if err != nil {
		t.Fatalf("Get T1: %v", err)
	}
	if finalT1.ActualQualityOutcome != QualityVerified {
		t.Fatalf("expected T1's own review to correctly update T1's (D1's) evaluation quality, got %q", finalT1.ActualQualityOutcome)
	}
}

// --- Coverage (ADR 0009D Part 13/21) ---

func TestDecisionScoped_CoverageCountsDecisionsNotTasks(t *testing.T) {
	// Task T1 has two decisions: D1 fully captured, D2 missing.
	coverage := ComputeEvaluationCoverage([]DecisionCoverageInput{
		{DecisionID: "D1", ExpectedChallengers: 1, CapturedChallengers: 1},
		{DecisionID: "D2", ExpectedChallengers: 1, CapturedChallengers: 0},
	})
	if coverage.EligibleDecisionCount != 2 {
		t.Fatalf("expected coverage to count 2 DECISIONS (not 1 task), got %d", coverage.EligibleDecisionCount)
	}
	if coverage.Complete() {
		t.Fatalf("expected incomplete coverage (D2 missing)")
	}
}

// --- Policy readiness (ADR 0009D Part 11-12/21) ---

func TestPolicyReadiness_CanonicalV2Accepted(t *testing.T) {
	d := NewRoutingPolicyDescriptor("performance", "v1", "semantics-1", nil)
	if err := ValidateExperimentPolicyIdentity(d); err != nil {
		t.Fatalf("expected a canonical descriptor to be experiment-eligible, got %v", err)
	}
}

func TestNegative_ManualV1RejectedForExperimentReadiness(t *testing.T) {
	d := RoutingPolicyDescriptor{ID: "performance", Version: "v1", Fingerprint: "manually-supplied"}
	err := ValidateExperimentPolicyIdentity(d)
	if err == nil {
		t.Fatalf("expected a manual/non-canonical descriptor to be rejected for experiment readiness")
	}
	if _, ok := err.(*PolicyNotExperimentEligible); !ok {
		t.Fatalf("expected *PolicyNotExperimentEligible, got %T", err)
	}
}

func TestNegative_MissingFingerprintRejectedForExperimentReadiness(t *testing.T) {
	d := RoutingPolicyDescriptor{ID: "legacy-deterministic", Version: "v1"} // no fingerprint at all
	if err := ValidateExperimentPolicyIdentity(d); err == nil {
		t.Fatalf("expected a descriptor with no fingerprint to be rejected for experiment readiness")
	}
}

func TestNegative_PolicyConflictRejectsExperimentContext(t *testing.T) {
	snapStore := NewMemoryCandidateSnapshotStore()
	evalStore := NewMemoryEvaluationStore()
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	snapStore.Save(context.Background(), snap)
	// No evaluation was ever captured for this decision - context not ready.
	err := ValidateExperimentContext(context.Background(), snapStore, evalStore, "tenant-a", snap.DecisionID, NewRoutingPolicyDescriptor("performance", "v1", "s1", nil), "agent-A")
	if err == nil {
		t.Fatalf("expected context-not-ready when no evaluation exists for this decision")
	}
	if _, ok := err.(*ExperimentContextNotReady); !ok {
		t.Fatalf("expected *ExperimentContextNotReady, got %T: %v", err, err)
	}
}

func TestExperimentReadiness_FullContextPasses(t *testing.T) {
	snapStore := NewMemoryCandidateSnapshotStore()
	evalStore := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A", "agent-B"}, time.Now())
	snapStore.Save(context.Background(), snap)

	canonicalPolicy := NewRoutingPolicyDescriptor("performance", "v1", "s1", nil)
	challenger := StaticRoutingPolicy{Descriptor_: canonicalPolicy, AgentID: "agent-B"}
	if _, err := evaluator.Evaluate(context.Background(), snap, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger}); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	err := ValidateExperimentContext(context.Background(), snapStore, evalStore, "tenant-a", snap.DecisionID, canonicalPolicy, "agent-B")
	if err != nil {
		t.Fatalf("expected a complete decision/evaluation/canonical-policy context to pass readiness, got %v", err)
	}
}

func TestNegative_ChallengerAgentOutsideSnapshotFailsExperimentReadiness(t *testing.T) {
	snapStore := NewMemoryCandidateSnapshotStore()
	evalStore := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	snap := testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	snapStore.Save(context.Background(), snap)
	canonicalPolicy := NewRoutingPolicyDescriptor("performance", "v1", "s1", nil)
	challenger := StaticRoutingPolicy{Descriptor_: canonicalPolicy, AgentID: "agent-A"}
	evaluator.Evaluate(context.Background(), snap, championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})

	err := ValidateExperimentContext(context.Background(), snapStore, evalStore, "tenant-a", snap.DecisionID, canonicalPolicy, "agent-NOT-IN-SNAPSHOT")
	if err == nil {
		t.Fatalf("expected readiness to fail when the named challenger agent is not in the snapshot")
	}
}

// --- Concurrency (ADR 0009D Part 15/21) ---

func TestConcurrency_SimultaneousDecisionsAndOutcomeAttachment(t *testing.T) {
	snapStore := NewMemoryCandidateSnapshotStore()
	evalStore := NewMemoryEvaluationStore()
	evaluator := NewShadowEvaluator(evalStore, DefaultEvaluationLimits(), nil)
	challenger := StaticRoutingPolicy{Descriptor_: challengerDesc(), AgentID: "agent-A"}

	const n = 20
	var wg sync.WaitGroup
	snaps := make([]CandidateSnapshot, n)
	for i := 0; i < n; i++ {
		snaps[i] = testSnapshot(t, "tenant-a", "T1", TaskClassSecurityReview, []string{"agent-A"}, time.Now())
	}
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			if err := snapStore.Save(context.Background(), snaps[i]); err != nil {
				t.Errorf("Save: %v", err)
			}
			recs, err := evaluator.Evaluate(context.Background(), snaps[i], championDesc(), "agent-A", time.Now(), []RoutingPolicy{challenger})
			if err != nil {
				t.Errorf("Evaluate: %v", err)
				return
			}
			if err := evaluator.AttachChampionOutcome(context.Background(), recs[0].EvaluationID, string(snaps[i].DecisionID)+"@exec", ExecutionCompleted, QualityUnreviewed); err != nil {
				t.Errorf("AttachChampionOutcome: %v", err)
			}
		}()
	}
	wg.Wait()

	all, err := snapStore.ListByTaskID(context.Background(), "tenant-a", "T1")
	if err != nil {
		t.Fatalf("ListByTaskID: %v", err)
	}
	if len(all) != n {
		t.Fatalf("expected all %d concurrently-created decisions to persist independently, got %d", n, len(all))
	}

	evals, err := evalStore.Query(context.Background(), EvaluationFilter{Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(evals) != n {
		t.Fatalf("expected %d distinct evaluation records, got %d", n, len(evals))
	}
	seen := map[string]bool{}
	for _, e := range evals {
		if seen[e.EvaluationID] {
			t.Fatalf("duplicate EvaluationID: %s", e.EvaluationID)
		}
		seen[e.EvaluationID] = true
		if !e.OutcomeAttached {
			t.Fatalf("expected every concurrent decision's outcome to be attached")
		}
	}
}
