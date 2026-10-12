package performance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

func phase10Experiment() Experiment {
	return Experiment{Definition: ExperimentDefinition{ID: "exp-1", Tenant: "tenant-a", TaskClass: "review", ChallengerPolicy: RoutingPolicyDescriptor{ID: "challenger", Version: "1", FingerprintScheme: FingerprintSchemeCanonicalV2}, ChallengerAgentID: "agent-b", MaxExecutions: 1, MaxConcurrent: 1, AllocationPercent: 100, AllocationSemantics: AllocationSemanticsV1}, State: ExperimentEnabled, UpdatedAt: time.Now().UTC()}
}

func TestExperimentAdmissionIsIdempotentAndBudgeted(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryExperimentStore()
	e := phase10Experiment()
	if err := st.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	c := NewExperimentController(st)
	snap := CandidateSnapshot{DecisionID: "d1", Tenant: "tenant-a", TaskID: "task-1", AgentIDs: []string{"agent-a", "agent-b"}}
	r := AdmissionRequest{ExperimentID: e.Definition.ID, DecisionID: "d1", Tenant: "tenant-a", Task: supervisor.Task{ID: "task-1", Tenant: "tenant-a", Type: "review"}, Snapshot: snap, ChampionAgentID: "agent-a", ChallengerAgentID: "agent-b", ChallengerPolicy: e.Definition.ChallengerPolicy, Now: time.Now().UTC()}
	a, err := c.Admit(ctx, r)
	if err != nil || !a.IsChallenger {
		t.Fatalf("admit: %+v %v", a, err)
	}
	a2, err := c.Admit(ctx, r)
	if err != nil || !a2.IsChallenger {
		t.Fatalf("idempotent admit: %+v %v", a2, err)
	}
	if err := c.Complete(ctx, a, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	e2, _ := st.Get(ctx, e.Definition.ID)
	if e2.Consumed != 1 || e2.State != ExperimentExhausted {
		t.Fatalf("unexpected budget state: %+v", e2)
	}
}

func TestExperimentBudgetReservationIsRaceSafe(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryExperimentStore()
	e := phase10Experiment()
	e.Definition.MaxExecutions = 1
	if err := st.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := st.Reserve(ctx, e.Definition.ID, supervisor.DecisionID("d"+string(rune('a'+i))), time.Now().UTC())
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("expected one reservation, got %d", wins)
	}
}

func TestExperimentAdmissionFailsClosedOnScopeAndCanonicalPolicy(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryExperimentStore()
	e := phase10Experiment()
	if err := st.Create(ctx, e); err != nil {
		t.Fatal(err)
	}
	c := NewExperimentController(st)
	snap := CandidateSnapshot{DecisionID: "d", Tenant: "tenant-a", AgentIDs: []string{"agent-b"}}
	r := AdmissionRequest{ExperimentID: e.Definition.ID, DecisionID: "d", Tenant: "tenant-b", Task: supervisor.Task{Tenant: "tenant-b", Type: "review"}, Snapshot: snap, ChampionAgentID: "agent-a", ChallengerAgentID: "agent-b", ChallengerPolicy: e.Definition.ChallengerPolicy, Now: time.Now().UTC()}
	a, err := c.Admit(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if a.IsChallenger || a.Selected != "agent-a" {
		t.Fatalf("scope was not denied: %+v", a)
	}
}
