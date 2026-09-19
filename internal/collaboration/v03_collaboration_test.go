package collaboration

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

func setupTestCollaborationEngine(t *testing.T) (*Engine, *mockHarness, *mockHarness) {
	tmpDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "test_engine.db"))
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}

	codexHarness := &mockHarness{
		name: "codex",
	}
	copilotHarness := &mockHarness{
		name: "copilot",
	}

	cfg := &config.Config{
		Agents: map[string]config.AgentConfig{
			"antigravity": {
				Command:  "echo",
				Writable: true,
			},
			"codex": {
				Command:  "echo",
				Writable: false,
			},
			"copilot": {
				Command:  "echo",
				Writable: false,
			},
		},
	}

	harnesses := map[string]agent.Harness{
		"codex":   codexHarness,
		"copilot": copilotHarness,
	}

	eng := NewEngine(EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Harnesses: harnesses,
	})

	return eng, codexHarness, copilotHarness
}

func TestV03_SpaceService_LifecycleAndDefaults(t *testing.T) {
	eng, _, _ := setupTestCollaborationEngine(t)
	defer eng.Store().Close()

	ctx := context.Background()
	spaceID := "space_lifecycle_01"

	participants := map[string]protocol.SpaceParticipant{
		"antigravity": {
			ID:       "antigravity",
			Adapter:  "antigravity",
			Roles:    []string{"executor"},
			Mode:     protocol.ParticipantModeActive,
			Writable: true,
		},
		"codex": {
			ID:       "codex",
			Adapter:  "codex",
			Roles:    []string{"reviewer"},
			Mode:     protocol.ParticipantModeOnDemand,
			Writable: false,
		},
	}

	sp, err := eng.SpaceService().CreateSpace(ctx, spaceID, "/tmp/repo", "Lifecycle Space", "Testing space lifecycle", "antigravity", participants)
	if err != nil {
		t.Fatalf("CreateSpace failed: %v", err)
	}

	// Verify default channels
	for _, expectedCh := range []string{"general", "architecture", "security", "testing", "findings", "decisions"} {
		if _, exists := sp.Channels[expectedCh]; !exists {
			t.Errorf("expected default channel %q to be present", expectedCh)
		}
	}

	// Verify pause and resume
	if err := eng.SpaceService().PauseSpace(ctx, spaceID); err != nil {
		t.Fatalf("PauseSpace failed: %v", err)
	}
	retSpace, _ := eng.SpaceService().GetSpace(ctx, spaceID)
	if retSpace.LifecycleState != protocol.SpaceStatePaused {
		t.Fatalf("expected paused state, got %v", retSpace.LifecycleState)
	}

	if err := eng.SpaceService().ResumeSpace(ctx, spaceID); err != nil {
		t.Fatalf("ResumeSpace failed: %v", err)
	}
	retSpace, _ = eng.SpaceService().GetSpace(ctx, spaceID)
	if retSpace.LifecycleState != protocol.SpaceStateActive {
		t.Fatalf("expected active state, got %v", retSpace.LifecycleState)
	}
}

func TestV03_EventBus_Coalescing(t *testing.T) {
	eng, _, _ := setupTestCollaborationEngine(t)
	defer eng.Store().Close()

	ctx := context.Background()
	spaceID := "space_coalesce_01"

	var coalescedEvt *protocol.CollaborationEvent
	var wg sync.WaitGroup
	wg.Add(1)

	eng.EventBus().AddListener(spaceID, func(ctx context.Context, evt *protocol.CollaborationEvent) {
		if evt.Type == protocol.EventRepoChanged {
			coalescedEvt = evt
			wg.Done()
		}
	})

	// Burst 20 rapid file change notifications
	for i := 1; i <= 20; i++ {
		file := filepath.Join("internal", "store", "store.go")
		if i%2 == 0 {
			file = filepath.Join("internal", "protocol", "types.go")
		}
		eng.EventBus().PublishRepoChange(ctx, spaceID, "test", file, map[string]any{"iteration": i})
	}

	// Manually flush to avoid waiting for timer in unit test
	eng.EventBus().FlushCoalesce(spaceID, "test")

	wg.Wait()

	if coalescedEvt == nil {
		t.Fatal("expected coalesced event to be emitted")
	}
	if len(coalescedEvt.Scope) != 2 {
		t.Fatalf("expected 2 unique coalesced files, got %v", coalescedEvt.Scope)
	}
}

func TestV03_EventBus_Coalescing_TimerRace(t *testing.T) {
	eng, _, _ := setupTestCollaborationEngine(t)
	defer eng.Store().Close()

	ctx := context.Background()
	spaceID := "space_race_01"

	var eventCount int
	var mu sync.Mutex

	eng.EventBus().AddListener(spaceID, func(ctx context.Context, evt *protocol.CollaborationEvent) {
		if evt.Type == protocol.EventRepoChanged {
			mu.Lock()
			eventCount++
			mu.Unlock()
		}
	})

	// Concurrent writes and timer expiration without explicit FlushCoalesce
	var wg sync.WaitGroup
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				file := fmt.Sprintf("file_%d_%d.go", workerID, i%3)
				eng.EventBus().PublishRepoChange(ctx, spaceID, fmt.Sprintf("src_%d", workerID), file, map[string]any{"i": i})
				time.Sleep(10 * time.Millisecond)
			}
		}(w)
	}
	wg.Wait()

	// Wait for all coalesce timers to fire naturally
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	count := eventCount
	mu.Unlock()

	if count == 0 {
		t.Fatal("expected at least one coalesced event fired by timer")
	}
}

func TestV03_ActivationController_CycleDetection(t *testing.T) {
	eng, _, _ := setupTestCollaborationEngine(t)
	defer eng.Store().Close()

	ctx := context.Background()
	spaceID := "space_cycle_01"

	participants := map[string]protocol.SpaceParticipant{
		"antigravity": {
			ID:       "antigravity",
			Adapter:  "antigravity",
			Mode:     protocol.ParticipantModeActive,
			Writable: true,
		},
		"codex": {
			ID:       "codex",
			Adapter:  "codex",
			Mode:     protocol.ParticipantModeActive,
			Writable: false,
		},
	}
	sp, _ := eng.SpaceService().CreateSpace(ctx, spaceID, ".", "Cycle test", "Detecting cycles", "antigravity", participants)

	codexPart := sp.Participants["codex"]

	// 1st message
	env1 := &protocol.PeerEnvelope{
		From:     "antigravity",
		To:       "codex",
		ThreadID: "th_cycle_1",
		Depth:    1,
		Payload:  []byte(`{"text":"What do you think?"}`),
	}
	ok, _, err := eng.ActivationController().ShouldActivate(ctx, sp, &codexPart, env1, nil)
	if !ok || err != nil {
		t.Fatalf("expected activation ok, got ok=%v, err=%v", ok, err)
	}

	// 2nd identical message in same thread
	ok, _, err = eng.ActivationController().ShouldActivate(ctx, sp, &codexPart, env1, nil)
	if !ok || err != nil {
		t.Fatalf("expected activation ok on second time, got ok=%v, err=%v", ok, err)
	}

	// 3rd identical message in same thread -> should trigger CollaborationCycleDetectedError
	_, reason, err := eng.ActivationController().ShouldActivate(ctx, sp, &codexPart, env1, nil)
	if err == nil {
		t.Fatalf("expected cycle error on 3rd identical message, got reason=%s", reason)
	}
	var cycleErr *protocol.CollaborationCycleDetectedError
	if _, isCycle := err.(*protocol.CollaborationCycleDetectedError); !isCycle {
		t.Fatalf("expected CollaborationCycleDetectedError, got %T: %v", err, err)
	}
	_ = cycleErr
}

func TestV03_Publish_And_Inbox(t *testing.T) {
	eng, codexHarness, _ := setupTestCollaborationEngine(t)
	defer eng.Store().Close()

	ctx := context.Background()
	spaceID := "space_pub_01"

	participants := map[string]protocol.SpaceParticipant{
		"antigravity": {
			ID:       "antigravity",
			Adapter:  "antigravity",
			Mode:     protocol.ParticipantModeActive,
			Writable: true,
		},
		"codex": {
			ID:       "codex",
			Adapter:  "codex",
			Mode:     protocol.ParticipantModeOnDemand, // on demand: activates when mentioned
			Writable: false,
		},
		"copilot": {
			ID:       "copilot",
			Adapter:  "copilot",
			Mode:     protocol.ParticipantModePassive, // passive: never auto-activated, goes to inbox
			Writable: false,
		},
	}
	_, err := eng.SpaceService().CreateSpace(ctx, spaceID, ".", "Publish Space", "Testing publishing", "antigravity", participants)
	if err != nil {
		t.Fatalf("CreateSpace failed: %v", err)
	}

	// Antigravity publishes message mentioning codex
	rawPayload, _ := json.Marshal(map[string]string{"text": "Please inspect this architecture"})
	pubReq := &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "architecture",
		Subject:   "Architecture Review",
		From:      "antigravity",
		Mentions:  []string{"codex"},
		Scope:     []string{"internal/store/store.go"},
		Payload:   rawPayload,
	}

	pubResp, err := eng.Publish(ctx, pubReq)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	if pubResp.MessageID == "" || pubResp.ThreadID == "" {
		t.Fatalf("invalid PublishResponse: %+v", pubResp)
	}

	// Verify codex was invoked because it was mentioned
	if codexHarness.invocations == 0 {
		t.Fatal("expected codex to be invoked upon mention")
	}

	// Verify copilot has the message in its inbox
	inbox, err := eng.GetInbox(ctx, spaceID, "copilot", nil, false)
	if err != nil {
		t.Fatalf("GetInbox failed: %v", err)
	}
	if len(inbox.Items) == 0 {
		t.Fatal("expected copilot inbox to contain items")
	}

	// Verify decision proposal and acceptance
	dec, err := eng.CreateDecision(ctx, spaceID, "Adopt persistent collaboration spaces", "Use SQLite collaboration spaces for all multi-agent coordination", "Ensures zero message loss and auditability", "antigravity", nil)
	if err != nil {
		t.Fatalf("CreateDecision failed: %v", err)
	}
	if dec.Status != protocol.DecisionProposed {
		t.Fatalf("expected proposed status, got %v", dec.Status)
	}

	acceptedDec, err := eng.AcceptDecision(ctx, spaceID, dec.ID, "codex")
	if err != nil {
		t.Fatalf("AcceptDecision failed: %v", err)
	}
	if acceptedDec.Status != protocol.DecisionAccepted {
		t.Fatalf("expected accepted status, got %v", acceptedDec.Status)
	}

	// Check SpaceStatus
	status, err := eng.SpaceStatus(ctx, spaceID)
	if err != nil {
		t.Fatalf("SpaceStatus failed: %v", err)
	}
	if status.SpaceID != spaceID || status.DecisionsCount != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
}
