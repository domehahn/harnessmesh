package review

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// TestConcurrency_ConcurrentIngestNoLostUpdates proves concurrent
// Ingest calls for the SAME claim (different observations) never lose
// an update: every observation ends up recorded, exactly once, even
// though all of them race to create/join the same ReviewGroup.
func TestConcurrency_ConcurrentIngestNoLostUpdates(t *testing.T) {
	state := NewReviewState(nil)
	stressLimits := DefaultReviewLimits()
	stressLimits.MaxIndependentReviews = 1000 // this test's purpose is no-lost-updates, not the agent-count limit
	state.SetLimits(stressLimits)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}

	const n = 30
	var wg sync.WaitGroup
	var firstID atomic.Value
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			agent := fmt.Sprintf("agent-%02d", i)
			obs := newObs(t, fmt.Sprintf("O%d", i), fmt.Sprintf("F%d", i), supervisor.AgentID(agent), "v1", StanceConfirms)
			id, err := state.Ingest(claim, "v1", "", obs)
			if err != nil {
				t.Errorf("Ingest: %v", err)
				return
			}
			firstID.Store(id)
		}()
	}
	wg.Wait()

	id := firstID.Load().(ReviewGroupID)
	group, ok := state.Group(id)
	if !ok {
		t.Fatalf("expected group to exist")
	}
	if len(group.Observations) != n {
		t.Fatalf("expected %d observations (no lost updates), got %d", n, len(group.Observations))
	}
	seen := map[TaskID]bool{}
	for _, o := range group.Observations {
		if seen[o.ID] {
			t.Fatalf("duplicate observation ID %q survived concurrent Ingest", o.ID)
		}
		seen[o.ID] = true
	}
	// All groups created across goroutines must actually be the SAME
	// group (same claim+version+tenant), never split into duplicates
	// under the race.
	all := state.Snapshot()
	count := 0
	for _, g := range all {
		if g.Claim == group.Claim && g.ArtifactVersion == group.ArtifactVersion {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 group for this claim+version, got %d", count)
	}
}

// TestConcurrency_ConcurrentApplyOnSameClaimRace proves concurrent
// ReviewApplier.Apply calls against the SAME group are serialized
// correctly: arbitration-round/evidence-request counters never lose an
// increment, and no partial state survives a rejected concurrent batch.
func TestConcurrency_ConcurrentApplyOnSameClaimRace(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	limits := DefaultReviewLimits()
	limits.MaxEvidenceRequests = 1000
	limits.MaxArbitrationRounds = 1000
	applier := NewReviewApplier(NewReviewValidator(limits), NewReviewAuthorizer(), state)

	const n = 50
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := applier.Apply(context.Background(), id, []ArbiterAction{
				RequestEvidenceAction{GroupID: id, RationaleSummary: "race"},
			})
			if err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	group, _ := state.Group(id)
	if int(successes.Load()) != n {
		t.Fatalf("expected all %d requests to succeed under generous limits, got %d successes", n, successes.Load())
	}
	if group.EvidenceRequests != n {
		t.Fatalf("expected EvidenceRequests=%d (no lost updates), got %d", n, group.EvidenceRequests)
	}
	if group.ArbitrationRounds != n {
		t.Fatalf("expected ArbitrationRounds=%d (no lost updates), got %d", n, group.ArbitrationRounds)
	}
}

// TestConcurrency_FindingChangesWhileArbitrationInProgress proves a new
// observation arriving (Ingest) concurrently with an in-flight
// ReviewApplier.Apply call on the same group never corrupts state - one
// of the two operations' lock acquisition always happens strictly
// before the other, never interleaved.
func TestConcurrency_FindingChangesWhileArbitrationInProgress(t *testing.T) {
	state := NewReviewState(nil)
	stressLimits := DefaultReviewLimits()
	stressLimits.MaxIndependentReviews = 1000
	state.SetLimits(stressLimits)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", newObs(t, "O1", "F1", "agent-a", "v1", StanceConfirms))
	limits := DefaultReviewLimits()
	limits.MaxEvidenceRequests = 1000
	limits.MaxArbitrationRounds = 1000
	applier := NewReviewApplier(NewReviewValidator(limits), NewReviewAuthorizer(), state)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			agent := supervisor.AgentID(fmt.Sprintf("agent-%02d", i))
			obs := newObs(t, fmt.Sprintf("O-new-%d", i), fmt.Sprintf("F-new-%d", i), agent, "v1", StanceConfirms)
			_, _ = state.Ingest(claim, "v1", "", obs)
		}()
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = applier.Apply(context.Background(), id, []ArbiterAction{
				RequestEvidenceAction{GroupID: id, RationaleSummary: "concurrent"},
			})
		}()
	}
	wg.Wait()

	group, ok := state.Group(id)
	if !ok {
		t.Fatalf("group must still exist")
	}
	seen := map[TaskID]bool{}
	for _, o := range group.Observations {
		if seen[o.ID] {
			t.Fatalf("duplicate observation after concurrent ingest+apply: %q", o.ID)
		}
		seen[o.ID] = true
	}
	if len(group.Observations) != 21 { // original + 20 new
		t.Fatalf("expected 21 observations (no lost updates), got %d", len(group.Observations))
	}
	if group.EvidenceRequests != 20 {
		t.Fatalf("expected 20 evidence requests (no lost updates), got %d", group.EvidenceRequests)
	}
}
