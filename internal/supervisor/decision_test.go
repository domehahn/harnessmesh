package supervisor

import (
	"context"
	"testing"
)

type recordingDecisionObserver struct {
	decisions []RoutingDecisionContext
}

func (r *recordingDecisionObserver) ObserveRoutingDecision(ctx context.Context, decision RoutingDecisionContext) {
	r.decisions = append(r.decisions, decision)
}

func TestDecisionCapture_DisabledModeNeverCapturesDecision(t *testing.T) {
	sched, _ := newTestScheduler(t, map[AgentID][]string{"alpha": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	observer := &recordingDecisionObserver{}
	sched.WithEvaluation(observer, EvaluationDisabled)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(observer.decisions) != 0 {
		t.Fatalf("expected no decision captured in disabled mode, got %d", len(observer.decisions))
	}
}

func TestDecisionCapture_ShadowModeCapturesLegacyChampion(t *testing.T) {
	sched, _ := newTestScheduler(t, map[AgentID][]string{"alpha": {}, "beta": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	observer := &recordingDecisionObserver{}
	sched.WithEvaluation(observer, EvaluationShadow)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(observer.decisions) != 1 {
		t.Fatalf("expected exactly 1 decision captured, got %d", len(observer.decisions))
	}
	d := observer.decisions[0]
	if d.ChampionPolicy.PolicyID != LegacyDeterministicPolicyID || d.ChampionPolicy.PolicyVersion != LegacyDeterministicPolicyVersion {
		t.Fatalf("expected legacy-deterministic champion policy, got %+v", d.ChampionPolicy)
	}
	if d.ChampionAgentID != "alpha" {
		t.Fatalf("expected champion agent alpha, got %q", d.ChampionAgentID)
	}
	if len(d.CandidateAgentIDs) != 2 {
		t.Fatalf("expected 2 candidates captured, got %d", len(d.CandidateAgentIDs))
	}
	if d.DecisionID == "" {
		t.Fatalf("expected a non-empty DecisionID")
	}
}

type identifiableAdvisor struct {
	advise func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error)
}

func (a *identifiableAdvisor) Advise(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
	return a.advise(ctx, task, candidates)
}
func (a *identifiableAdvisor) PolicyID() string      { return "performance" }
func (a *identifiableAdvisor) PolicyVersion() string { return "v1" }

var _ PolicyIdentifiable = (*identifiableAdvisor)(nil)

func TestDecisionCapture_AdvisoryModeCapturesAdvisorChampion(t *testing.T) {
	sched, _ := newTestScheduler(t, map[AgentID][]string{"alpha": {}, "beta": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	observer := &recordingDecisionObserver{}
	advisor := &identifiableAdvisor{advise: func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
		return AgentAdvice{Ranked: []AgentRecommendation{{AgentID: "beta", Confidence: ConfidenceStrong}}}, nil
	}}
	sched.WithAdvisor(advisor, AgentDecisionAdvisory, "performance-v1")
	sched.WithEvaluation(observer, EvaluationShadow)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(observer.decisions) != 1 {
		t.Fatalf("expected exactly 1 decision, got %d", len(observer.decisions))
	}
	d := observer.decisions[0]
	if d.ChampionPolicy.PolicyID != "performance" || d.ChampionPolicy.PolicyVersion != "v1" {
		t.Fatalf("expected the advisor's own identity captured, got %+v", d.ChampionPolicy)
	}
	if d.ChampionAgentID != "beta" {
		t.Fatalf("expected champion agent beta, got %q", d.ChampionAgentID)
	}
	if d.ChampionPolicy.FallbackUsed {
		t.Fatalf("expected FallbackUsed=false when the advisor's pick was actually used")
	}
}

func TestDecisionCapture_AdvisorFailureCapturesFallbackAsChampionNotAdvisor(t *testing.T) {
	sched, _ := newTestScheduler(t, map[AgentID][]string{"alpha": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	observer := &recordingDecisionObserver{}
	advisor := &identifiableAdvisor{advise: func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
		return AgentAdvice{}, errUnavailable
	}}
	sched.WithAdvisor(advisor, AgentDecisionAdvisory, "performance-v1")
	sched.WithEvaluation(observer, EvaluationShadow)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	d := observer.decisions[0]
	if d.ChampionPolicy.PolicyID != LegacyDeterministicPolicyID {
		t.Fatalf("expected the FAILED advisor to never be captured as champion; expected legacy-deterministic, got %+v", d.ChampionPolicy)
	}
	if !d.ChampionPolicy.FallbackUsed {
		t.Fatalf("expected FallbackUsed=true")
	}
	if d.ChampionPolicy.AttemptedPolicyID != "performance" {
		t.Fatalf("expected the attempted advisor policy preserved as provenance, got %+v", d.ChampionPolicy)
	}
}

func TestDecisionCapture_ShadowRecommendationNeverBecomesChampionPolicy(t *testing.T) {
	sched, _ := newTestScheduler(t, map[AgentID][]string{"alpha": {}, "beta": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	observer := &recordingDecisionObserver{}
	advisor := &identifiableAdvisor{advise: func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
		return AgentAdvice{Ranked: []AgentRecommendation{{AgentID: "beta", Confidence: ConfidenceStrong}}}, nil
	}}
	sched.WithAdvisor(advisor, AgentDecisionShadow, "performance-v1")
	sched.WithEvaluation(observer, EvaluationShadow)

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	d := observer.decisions[0]
	if d.ChampionAgentID != "alpha" {
		t.Fatalf("expected the legacy fallback (alpha), not the shadow recommendation (beta), got %q", d.ChampionAgentID)
	}
	if d.ChampionPolicy.PolicyID != LegacyDeterministicPolicyID {
		t.Fatalf("expected legacy champion policy in shadow mode, got %+v", d.ChampionPolicy)
	}
}

type panickingDecisionObserver struct{}

func (p panickingDecisionObserver) ObserveRoutingDecision(ctx context.Context, decision RoutingDecisionContext) {
	panic("decision observer boom")
}

type countingDecisionFailureSink struct{ count int }

func (s *countingDecisionFailureSink) RecordRoutingDecisionObserverPanic() { s.count++ }

func TestDecisionCapture_ObserverPanicDoesNotAffectSelection(t *testing.T) {
	sched, state := newTestScheduler(t, map[AgentID][]string{"alpha": {}}, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	sched.WithEvaluation(panickingDecisionObserver{}, EvaluationShadow)
	sink := &countingDecisionFailureSink{}
	sched.EvaluationFailureSink = sink

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick must succeed despite a panicking decision observer: %v", err)
	}
	final := state.Snapshot()
	task, _ := final.Tasks.ByID("T1")
	if task.Status != TaskStatusCompleted {
		t.Fatalf("expected the task to still complete, got %q", task.Status)
	}
	if sink.count != 1 {
		t.Fatalf("expected the panic recorded exactly once, got %d", sink.count)
	}
}

func TestDecisionCapture_RetryProducesNewDecisionID(t *testing.T) {
	id1 := NewDecisionID("T1")
	id2 := NewDecisionID("T1")
	if id1 == id2 {
		t.Fatalf("expected two separate decisions for the same TaskID to get distinct DecisionIDs")
	}
}
