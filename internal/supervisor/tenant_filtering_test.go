package supervisor

import (
	"context"
	"strings"
	"testing"
)

// --- ADR 0006 Phase 7C: Tenant Filtering Before Advice ---

func TestTenantFiltering_ForeignTenantAgentNeverPassedToAdvisor(t *testing.T) {
	dir := newFakeDirectoryWithTenants(
		map[AgentID][]string{"alpha": {}, "foreign": {}},
		map[AgentID]string{"alpha": "tenant-a", "foreign": "tenant-b"},
	)
	sched, _ := newTestScheduler(t, nil, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	sched.Catalog = dir
	sched.Applier.Validator.Agents = dir
	sched.Executor.Directory = dir

	var seen []AgentID
	advisor := &fakeAdvisor{AdviseFunc: func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
		for _, c := range candidates {
			seen = append(seen, c.AgentID)
		}
		return AgentAdvice{}, nil
	}}
	sched.WithAdvisor(advisor, AgentDecisionShadow, "v1")

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sched.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	for _, id := range seen {
		if id == "foreign" {
			t.Fatalf("foreign-tenant agent must never be passed to the advisor as a candidate, got candidates=%v", seen)
		}
	}
	if len(seen) == 0 {
		t.Fatalf("expected the advisor to have been called with at least the same-tenant candidate")
	}
}

func TestTenantFiltering_ForeignAgentAbsentFromShadowProvenance(t *testing.T) {
	dir := newFakeDirectoryWithTenants(
		map[AgentID][]string{"alpha": {}, "foreign": {}},
		map[AgentID]string{"alpha": "tenant-a", "foreign": "tenant-b"},
	)
	sched, _ := newTestScheduler(t, nil, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	sched.Catalog = dir
	sched.Applier.Validator.Agents = dir
	sched.Executor.Directory = dir

	advisor := &fakeAdvisor{AdviseFunc: func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
		// A hostile/buggy advisor tries to recommend the foreign-tenant
		// agent anyway, even though it was never in `candidates`.
		return AgentAdvice{Ranked: []AgentRecommendation{{AgentID: "foreign", Confidence: ConfidenceStrong}}}, nil
	}}
	sched.WithAdvisor(advisor, AgentDecisionShadow, "v1")

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}

	for _, e := range events {
		if strings.Contains(e.Detail, "foreign") {
			t.Fatalf("decision provenance must never mention a foreign-tenant agent ID, got event %+v", e)
		}
	}
}

func TestTenantFiltering_AdvisorCannotReintroduceFilteredAgentInAdvisoryMode(t *testing.T) {
	dir := newFakeDirectoryWithTenants(
		map[AgentID][]string{"alpha": {}, "foreign": {}},
		map[AgentID]string{"alpha": "tenant-a", "foreign": "tenant-b"},
	)
	sched, state := newTestScheduler(t, nil, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	sched.Catalog = dir
	sched.Applier.Validator.Agents = dir
	sched.Executor.Directory = dir

	advisor := &fakeAdvisor{AdviseFunc: func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
		return AgentAdvice{Ranked: []AgentRecommendation{{AgentID: "foreign", Confidence: ConfidenceStrong}}}, nil
	}}
	sched.WithAdvisor(advisor, AgentDecisionAdvisory, "v1")

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}

	final := state.Snapshot()
	if final.Assignments["T1"] == "foreign" {
		t.Fatalf("the advisor must never be able to reintroduce a tenant-filtered agent by ID")
	}
	if final.Assignments["T1"] != "alpha" {
		t.Fatalf("expected fallback to the only real same-tenant agent (alpha), got %q", final.Assignments["T1"])
	}
	found := false
	for _, e := range events {
		if e.Kind == EventAdvisorFallback {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an explicit EventAdvisorFallback event")
	}
}

func TestTenantFiltering_MissingTenantDirectoryForScopedTaskFailsClosed(t *testing.T) {
	dir := &catalogWithoutTenantDirectory{agents: map[AgentID][]string{"alpha": {}}}
	_, err := eligibleCandidates(context.Background(), dir, Task{ID: "T1", Tenant: "tenant-a"})
	if err == nil {
		t.Fatalf("expected a fail-closed error for a tenant-scoped task with no TenantDirectory authority")
	}
	if _, ok := err.(*AuthorizationFailure); !ok {
		t.Fatalf("expected *AuthorizationFailure, got %T: %v", err, err)
	}
}

func TestTenantFiltering_UnscopedTaskUnaffected(t *testing.T) {
	dir := newFakeDirectoryWithTenants(
		map[AgentID][]string{"alpha": {}, "beta": {}},
		map[AgentID]string{"alpha": "tenant-a", "beta": "tenant-b"},
	)
	candidates, err := eligibleCandidates(context.Background(), dir, Task{ID: "T1"}) // no Tenant set
	if err != nil {
		t.Fatalf("eligibleCandidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected an unscoped task to see every capability-eligible agent regardless of tenant, got %v", candidates)
	}
}

func TestTenantFiltering_ScopedTaskSeesOnlySameTenantCandidates(t *testing.T) {
	dir := newFakeDirectoryWithTenants(
		map[AgentID][]string{"alpha": {}, "foreign": {}},
		map[AgentID]string{"alpha": "tenant-a", "foreign": "tenant-b"},
	)
	candidates, err := eligibleCandidates(context.Background(), dir, Task{ID: "T1", Tenant: "tenant-a"})
	if err != nil {
		t.Fatalf("eligibleCandidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].AgentID != "alpha" {
		t.Fatalf("expected only the same-tenant agent as a candidate, got %v", candidates)
	}
}

// catalogWithoutTenantDirectory implements AgentCatalog but deliberately
// NOT TenantDirectory, to exercise the fail-closed "no tenant authority"
// path.
type catalogWithoutTenantDirectory struct {
	agents map[AgentID][]string
}

func (c *catalogWithoutTenantDirectory) AgentExists(id AgentID) bool {
	_, ok := c.agents[id]
	return ok
}
func (c *catalogWithoutTenantDirectory) Capabilities(id AgentID) ([]string, bool) {
	caps, ok := c.agents[id]
	return caps, ok
}
func (c *catalogWithoutTenantDirectory) ListAgents(ctx context.Context) ([]AgentContextSummary, error) {
	out := make([]AgentContextSummary, 0, len(c.agents))
	for id, caps := range c.agents {
		out = append(out, AgentContextSummary{AgentID: id, Capabilities: caps})
	}
	return out, nil
}

func TestTenantFiltering_TenantChangeAfterAdviceRejectedAtExecution(t *testing.T) {
	dir := newFakeDirectoryWithTenants(
		map[AgentID][]string{"alpha": {}},
		map[AgentID]string{"alpha": "tenant-a"},
	)
	sched, state := newTestScheduler(t, nil, DefaultSupervisorLimits(), &FakeExecutionProvider{})
	sched.Catalog = dir
	sched.Applier.Validator.Agents = dir
	sched.Executor.Directory = dir

	advisor := &fakeAdvisor{AdviseFunc: func(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error) {
		// Simulate the agent's tenant changing in the live directory
		// AFTER the advisor saw it as an eligible, same-tenant candidate
		// but BEFORE Executor's own TOCTOU re-check runs.
		dir.mu.Lock()
		dir.tenants["alpha"] = "tenant-b"
		dir.mu.Unlock()
		return AgentAdvice{Ranked: []AgentRecommendation{{AgentID: "alpha", Confidence: ConfidenceStrong}}}, nil
	}}
	sched.WithAdvisor(advisor, AgentDecisionAdvisory, "v1")

	if _, err := sched.Applier.Apply(context.Background(), []AgentProposal{
		CreateTaskProposal{Task: Task{ID: "T1", Tenant: "tenant-a"}, RationaleSummary: "r"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	events, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}

	final := state.Snapshot()
	if final.Assignments["T1"] == "alpha" {
		// If it WAS assigned, Executor's live TOCTOU re-check must still
		// have rejected the actual execution.
		found := false
		for _, e := range events {
			if e.Kind == EventAuthorizationRejected {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected Executor's live tenant re-check to reject execution after the tenant changed, got events=%+v", events)
		}
	}
}
