package workflow

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
)

// Helper to set up a full collaboration engine for integration tests
func setupV03TestEngine(t *testing.T, harnesses map[string]agent.Harness) (*collaboration.Engine, store.Store, string) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "v03_test.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}

	cfg := &config.Config{
		Version: 2,
		Agents: map[string]config.AgentConfig{
			"antigravity": {Kind: "antigravity", Role: "executor", Writable: true},
			"codex":       {Kind: "codex", Role: "reviewer", Writable: false},
			"copilot":     {Kind: "copilot", Role: "security_reviewer", Writable: false},
		},
		Workflow: config.WorkflowConfig{
			Executor: "antigravity",
			Reviewer: "codex",
		},
	}

	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Projector: contextpack.New(tmpDir, cfg.Context),
		Harnesses: harnesses,
	})

	return eng, st, tmpDir
}

// Test A: Interactive publish-and-reply loop between Antigravity and Codex
func TestV03_Integration_A_InteractivePublishAndReply(t *testing.T) {
	codexInvoked := 0
	codexHarness := &testHarness{
		name: "codex",
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			codexInvoked++
			return agent.InvokeResult{
				AgentName: "codex",
				Text:      "Codex architecture advice: Decouple the event dispatcher from database persistence.",
			}, nil
		},
	}

	eng, st, _ := setupV03TestEngine(t, map[string]agent.Harness{"codex": codexHarness})
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_test_a"

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
			Mode:     protocol.ParticipantModeOnDemand,
			Writable: false,
		},
	}

	_, err := eng.SpaceService().CreateSpace(ctx, spaceID, ".", "Test A Space", "Interactive publish and reply", "antigravity", participants)
	if err != nil {
		t.Fatalf("CreateSpace failed: %v", err)
	}

	// 1. Antigravity publishes a message mentioning codex
	pubReq := &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "architecture",
		Subject:   "Event Dispatcher Decoupling",
		From:      "antigravity",
		Mentions:  []string{"codex"},
		Scope:     []string{"internal/collaboration/eventbus.go"},
		Payload:   []byte(`{"text":"Should we decouple the event dispatcher from SQLite store?"}`),
	}
	pubResp, err := eng.Publish(ctx, pubReq)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	if codexInvoked != 1 {
		t.Fatalf("expected codex to be invoked exactly once, got %d", codexInvoked)
	}
	if len(pubResp.DeliveredTo) != 1 || pubResp.DeliveredTo[0] != "codex" {
		t.Fatalf("expected delivered to codex, got %v", pubResp.DeliveredTo)
	}

	// 2. Antigravity sends a follow-up reply in the thread
	replyResp, err := eng.PublishReply(ctx, spaceID, "architecture", pubResp.ThreadID, "antigravity", "Agreed, I will introduce an in-memory ring buffer.", []string{"codex"}, nil)
	if err != nil {
		t.Fatalf("PublishReply failed: %v", err)
	}
	if replyResp.MessageID == "" {
		t.Fatal("expected reply message ID")
	}

	// 3. Verify messages in thread
	msgs, err := st.GetSpaceMessages(ctx, spaceID, "architecture", pubResp.ThreadID, 10)
	if err != nil {
		t.Fatalf("GetSpaceMessages failed: %v", err)
	}
	// Initial message + Codex response + Antigravity follow-up reply + Codex reply
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages in thread, got %d", len(msgs))
	}
}

// Test B: Repository event broadcast with multi-agent fanout
func TestV03_Integration_B_RepoEventBroadcastFanout(t *testing.T) {
	var codexRan, copilotRan bool
	var mu sync.Mutex

	codexHarness := &testHarness{
		name: "codex",
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			mu.Lock()
			codexRan = true
			mu.Unlock()
			return agent.InvokeResult{
				AgentName: "codex",
				Text:      `{"verdict":"COMMENT","summary":"Architectural check passed","findings":[]}`,
			}, nil
		},
	}
	copilotHarness := &testHarness{
		name: "copilot",
		invokeFunc: func(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
			mu.Lock()
			copilotRan = true
			mu.Unlock()
			return agent.InvokeResult{
				AgentName: "copilot",
				Text:      `{"verdict":"COMMENT","summary":"Security audit clean","findings":[]}`,
			}, nil
		},
	}

	eng, st, _ := setupV03TestEngine(t, map[string]agent.Harness{
		"codex":   codexHarness,
		"copilot": copilotHarness,
	})
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_test_b"

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
		"copilot": {
			ID:       "copilot",
			Adapter:  "copilot",
			Mode:     protocol.ParticipantModeActive,
			Writable: false,
		},
	}

	_, err := eng.SpaceService().CreateSpace(ctx, spaceID, ".", "Test B Space", "Repo fanout", "antigravity", participants)
	if err != nil {
		t.Fatalf("CreateSpace failed: %v", err)
	}

	// Register subscriptions
	_ = eng.Subscribe(ctx, &protocol.Subscription{
		ID:            "sub_b_codex",
		SpaceID:       spaceID,
		ParticipantID: "codex",
		EventTypes:    []string{protocol.EventRepoChanged},
		Mode:          protocol.ParticipantModeActive,
	})
	_ = eng.Subscribe(ctx, &protocol.Subscription{
		ID:            "sub_b_copilot",
		SpaceID:       spaceID,
		ParticipantID: "copilot",
		EventTypes:    []string{protocol.EventRepoChanged},
		Mode:          protocol.ParticipantModeActive,
	})

	// Publish repository.changed event
	err = eng.EventBus().Publish(ctx, &protocol.CollaborationEvent{
		ID:        "evt_repo_b",
		SpaceID:   spaceID,
		Type:      protocol.EventRepoChanged,
		Source:    "antigravity",
		Timestamp: time.Now().UTC(),
		Scope:     []string{"internal/auth/auth.go"},
		Payload:   map[string]any{"commit": "c12345"},
	})
	if err != nil {
		t.Fatalf("Publish event failed: %v", err)
	}

	// Allow goroutines to finish
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if !codexRan || !copilotRan {
		t.Fatalf("expected both codex and copilot to activate upon repo event: codex=%v, copilot=%v", codexRan, copilotRan)
	}
}

// Test C: Autonomous cross-agent finding challenge and evidence resolution
func TestV03_Integration_C_AutonomousFindingChallengeAndEvidence(t *testing.T) {
	eng, st, _ := setupV03TestEngine(t, nil)
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_test_c"

	participants := map[string]protocol.SpaceParticipant{
		"codex":   {ID: "codex", Adapter: "codex", Mode: protocol.ParticipantModeActive},
		"copilot": {ID: "copilot", Adapter: "copilot", Mode: protocol.ParticipantModeActive},
	}
	_, err := eng.SpaceService().CreateSpace(ctx, spaceID, ".", "Dispute Space", "Finding challenge", "codex", participants)
	if err != nil {
		t.Fatalf("CreateSpace failed: %v", err)
	}

	// 1. Codex creates finding
	f := &protocol.FindingPayload{
		ID:                "HM-RACE-002",
		SessionID:         spaceID,
		SourceParticipant: "codex",
		SourceAgent:       "codex",
		Severity:          "high",
		Category:          "concurrency",
		File:              "internal/store/store.go",
		Line:              150,
		Claim:             "Unsynchronized map access in GetSession",
		Evidence:          "s.sessions accessed without lock",
		Recommendation:    "Acquire s.mu.RLock()",
		Status:            protocol.FindingOpen,
	}
	if err := st.SaveFinding(ctx, spaceID, f); err != nil {
		t.Fatalf("SaveFinding failed: %v", err)
	}

	// 2. Copilot challenges finding (marks as disputed)
	ch := &protocol.ChallengePayload{
		ID:                    "ch_002",
		FindingID:             f.ID,
		Challenger:            "copilot",
		Claim:                 "Caller function already holds mutex",
		Evidence:              "internal/store/store.go:142",
		RequestedVerification: "Run race detector",
	}
	if err := st.SaveChallenge(ctx, spaceID, ch); err != nil {
		t.Fatalf("SaveChallenge failed: %v", err)
	}

	fAfterCh, _ := st.GetFinding(ctx, spaceID, f.ID)
	if fAfterCh.Status != protocol.FindingDisputed {
		t.Fatalf("expected disputed status, got %s", fAfterCh.Status)
	}

	// 3. Codex runs verification and attaches concrete evidence
	exitCode := 0
	ev := &protocol.EvidencePayload{
		ID:          "ev_002",
		FindingID:   f.ID,
		SourceAgent: "codex",
		Type:        protocol.EvidenceTestResult,
		Command:     "go test -race ./internal/store/...",
		Result:      "PASS",
		ExitCode:    &exitCode,
	}
	if err := st.SaveEvidence(ctx, spaceID, ev); err != nil {
		t.Fatalf("SaveEvidence failed: %v", err)
	}

	// 4. Copilot reviews evidence and confirms resolution (dismisses false positive)
	res := &protocol.ResolutionPayload{
		ID:                 "res_002",
		FindingID:          f.ID,
		Status:             protocol.ResolutionRejected, // Reject finding based on race test PASS
		Rationale:          "Race detector passed; caller indeed holds lock.",
		ResolvingAgent:     "copilot",
		EvidenceReferences: []string{"ev_002"},
	}
	if err := st.SaveResolution(ctx, spaceID, res); err != nil {
		t.Fatalf("SaveResolution failed: %v", err)
	}

	fFinal, _ := st.GetFinding(ctx, spaceID, f.ID)
	if fFinal.Status != protocol.FindingDismissed {
		t.Fatalf("expected dismissed status after rejected finding resolution, got %s", fFinal.Status)
	}
}

// Test D: Offline/passive participant inbox queueing and catchup
func TestV03_Integration_D_OfflinePassiveParticipantCatchup(t *testing.T) {
	eng, st, _ := setupV03TestEngine(t, nil)
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_test_d"

	participants := map[string]protocol.SpaceParticipant{
		"antigravity": {
			ID:       "antigravity",
			Adapter:  "antigravity",
			Mode:     protocol.ParticipantModeActive,
			Writable: true,
		},
		"passive_agent": {
			ID:       "passive_agent",
			Adapter:  "codex",
			Mode:     protocol.ParticipantModePassive, // passive mode
			Writable: false,
		},
	}
	_, err := eng.SpaceService().CreateSpace(ctx, spaceID, ".", "Passive Space", "Passive catchup", "antigravity", participants)
	if err != nil {
		t.Fatalf("CreateSpace failed: %v", err)
	}

	// Publish messages addressed to passive_agent
	for i := 1; i <= 3; i++ {
		payload, _ := json.Marshal(map[string]string{"text": "Task notification"})
		_, err := eng.Publish(ctx, &protocol.PublishRequest{
			SpaceID:   spaceID,
			ChannelID: "general",
			From:      "antigravity",
			Mentions:  []string{"passive_agent"},
			Payload:   payload,
		})
		if err != nil {
			t.Fatalf("Publish failed: %v", err)
		}
	}

	// Passive agent wakes up and checks inbox
	inbox, err := eng.GetInbox(ctx, spaceID, "passive_agent", nil, true)
	if err != nil {
		t.Fatalf("GetInbox failed: %v", err)
	}
	if len(inbox.Items) != 3 {
		t.Fatalf("expected 3 items in passive agent inbox, got %d", len(inbox.Items))
	}
	if !inbox.Items[0].MentionsMe {
		t.Fatal("expected mentions_me to be true")
	}

	// Cursor should now be updated to the last message
	cursor, err := st.GetParticipantCursor(ctx, spaceID, "passive_agent")
	if err != nil || cursor.LastSeenMessageID == "" {
		t.Fatalf("expected cursor to be updated, got %+v", cursor)
	}
}

// Test E: Causal loop detection and cycle prevention
func TestV03_Integration_E_CausalLoopDetection(t *testing.T) {
	eng, st, _ := setupV03TestEngine(t, nil)
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_test_e"

	participants := map[string]protocol.SpaceParticipant{
		"agent_a": {ID: "agent_a", Adapter: "claude", Mode: protocol.ParticipantModeActive, Writable: true},
		"agent_b": {ID: "agent_b", Adapter: "codex", Mode: protocol.ParticipantModeActive, Writable: false},
	}
	sp, _ := eng.SpaceService().CreateSpace(ctx, spaceID, ".", "Loop Space", "Loop prevention", "agent_a", participants)
	partB := sp.Participants["agent_b"]

	env := &protocol.PeerEnvelope{
		From:     "agent_a",
		To:       "agent_b",
		ThreadID: "th_loop",
		Depth:    1,
		Payload:  []byte(`{"msg":"loop check"}`),
	}

	// First two activations are permitted
	ok1, _, _ := eng.ActivationController().ShouldActivate(ctx, sp, &partB, env, nil)
	ok2, _, _ := eng.ActivationController().ShouldActivate(ctx, sp, &partB, env, nil)
	if !ok1 || !ok2 {
		t.Fatalf("expected first two activations to pass")
	}

	// 3rd activation with same content in same thread should be blocked as cycle
	ok3, reason, err := eng.ActivationController().ShouldActivate(ctx, sp, &partB, env, nil)
	if ok3 || err == nil {
		t.Fatalf("expected cycle detection error on 3rd identical trigger, got ok=%v, err=%v", ok3, err)
	}
	if reason != "cycle_detected" {
		t.Fatalf("expected reason cycle_detected, got %s", reason)
	}
}

// Test F: Human control and pause/resume
func TestV03_Integration_F_HumanControlPauseResume(t *testing.T) {
	eng, st, _ := setupV03TestEngine(t, nil)
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_test_f"

	participants := map[string]protocol.SpaceParticipant{
		"antigravity": {ID: "antigravity", Adapter: "antigravity", Mode: protocol.ParticipantModeActive, Writable: true},
		"codex":       {ID: "codex", Adapter: "codex", Mode: protocol.ParticipantModeActive, Writable: false},
	}
	_, err := eng.SpaceService().CreateSpace(ctx, spaceID, ".", "Pause Space", "Pause/Resume", "antigravity", participants)
	if err != nil {
		t.Fatalf("CreateSpace failed: %v", err)
	}

	// Human pauses space
	if err := eng.SpaceService().PauseSpace(ctx, spaceID); err != nil {
		t.Fatalf("PauseSpace failed: %v", err)
	}

	// Attempting to publish while paused returns ParticipantPausedError
	payload, _ := json.Marshal(map[string]string{"text": "should be blocked"})
	_, err = eng.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "general",
		From:      "antigravity",
		Payload:   payload,
	})
	if err == nil {
		t.Fatal("expected error publishing to paused space")
	}
	var pausedErr *protocol.ParticipantPausedError
	if !bool(err != nil) {
		t.Fatal("expected error")
	}
	_ = pausedErr

	// Human resumes space
	if err := eng.SpaceService().ResumeSpace(ctx, spaceID); err != nil {
		t.Fatalf("ResumeSpace failed: %v", err)
	}

	// Publishing now succeeds
	pubResp, err := eng.Publish(ctx, &protocol.PublishRequest{
		SpaceID:   spaceID,
		ChannelID: "general",
		From:      "antigravity",
		Payload:   payload,
	})
	if err != nil || pubResp.MessageID == "" {
		t.Fatalf("Publish after resume failed: %v", err)
	}
}

// Test G: Sliding-window event coalescing
func TestV03_Integration_G_SlidingWindowCoalescing(t *testing.T) {
	eng, st, _ := setupV03TestEngine(t, nil)
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_test_g"

	var coalescedEvt *protocol.CollaborationEvent
	var wg sync.WaitGroup
	wg.Add(1)

	eng.EventBus().AddListener(spaceID, func(ctx context.Context, evt *protocol.CollaborationEvent) {
		if evt.Type == protocol.EventRepoChanged {
			coalescedEvt = evt
			wg.Done()
		}
	})

	// Burst 20 rapid file changes across 3 files
	for i := 1; i <= 20; i++ {
		targetFile := "internal/file1.go"
		if i%3 == 1 {
			targetFile = "internal/file2.go"
		} else if i%3 == 2 {
			targetFile = "internal/file3.go"
		}
		eng.EventBus().PublishRepoChange(ctx, spaceID, "fs_watcher", targetFile, nil)
	}

	eng.EventBus().FlushCoalesce(spaceID, "fs_watcher")
	wg.Wait()

	if coalescedEvt == nil {
		t.Fatal("expected coalesced event")
	}
	if len(coalescedEvt.Scope) != 3 {
		t.Fatalf("expected 3 coalesced files, got %d: %v", len(coalescedEvt.Scope), coalescedEvt.Scope)
	}
}

// Test H: Sensitive file privacy and policy enforcement
func TestV03_Integration_H_SensitiveFilePolicy(t *testing.T) {
	eng, st, _ := setupV03TestEngine(t, nil)
	defer st.Close()

	cloudPart := &protocol.SpaceParticipant{
		ID:      "codex_cloud",
		Adapter: "codex", // cloud adapter
	}
	localPart := &protocol.SpaceParticipant{
		ID:      "antigravity_local",
		Adapter: "antigravity",
	}

	sensitiveScope := []string{"configs/keys/private.key", "src/main.go"}

	// Cloud participant must be denied
	ok, reason := eng.ActivationController().CheckPrivacyPolicy(cloudPart, sensitiveScope)
	if ok || reason == "" {
		t.Fatal("expected cloud participant to be denied access to sensitive key file")
	}

	// Local participant must be allowed
	okLocal, _ := eng.ActivationController().CheckPrivacyPolicy(localPart, sensitiveScope)
	if !okLocal {
		t.Fatal("expected local participant to be allowed")
	}
}

// Test I: Model routing and Switchyard boundary
func TestV03_Integration_I_ModelRoutingSwitchyardBoundary(t *testing.T) {
	eng, st, _ := setupV03TestEngine(t, nil)
	defer st.Close()

	ctx := context.Background()
	spaceID := "space_test_i"

	// Create space first to satisfy foreign key constraint
	_, err := eng.SpaceService().CreateSpace(ctx, spaceID, ".", "Test I Space", "Switchyard test", "codex", nil)
	if err != nil {
		t.Fatalf("CreateSpace failed: %v", err)
	}

	// Record routing decision
	rec := &store.RoutingDecisionRecord{
		ID:                  "rd_switchyard_01",
		SessionID:           spaceID,
		RequestedCapability: "security",
		TaskTier:            "critical",
		Eligible:            []string{"codex", "copilot"},
		Selected:            "codex",
		Reason:              []string{"switchyard_delegation", "tier_critical"},
		Timestamp:           time.Now().UTC(),
	}
	if err := st.SaveRoutingDecision(ctx, rec); err != nil {
		t.Fatalf("SaveRoutingDecision failed: %v", err)
	}

	// Record usage with model_routing_backend = "switchyard"
	u := &store.UsageRecord{
		ID:                  "usg_sw_01",
		SessionID:           spaceID,
		Participant:         "codex",
		Adapter:             "codex",
		Model:               "openai/gpt-4o",
		ModelRoutingBackend: "switchyard",
		CostSource:          "switchyard",
		InputTokens:         1500,
		OutputTokens:        400,
		MonetaryCostUSD:     0.015,
		Timestamp:           time.Now().UTC(),
	}
	if err := st.SaveUsage(ctx, u); err != nil {
		t.Fatalf("SaveUsage failed: %v", err)
	}

	usages, err := st.GetUsage(ctx, spaceID)
	if err != nil || len(usages) != 1 {
		t.Fatalf("GetUsage failed: %v", err)
	}
	if usages[0].ModelRoutingBackend != "switchyard" {
		t.Fatalf("expected switchyard backend, got %s", usages[0].ModelRoutingBackend)
	}
}

// Test: Crash Recovery and Idempotency
func TestV03_Integration_CrashRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "crash_recovery.db")

	// 1. Initial process: create space and record an in-flight delivery
	st1, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	spaceID := "space_crash"

	eng1 := collaboration.NewEngine(collaboration.EngineConfig{Store: st1})
	_, _ = eng1.SpaceService().CreateSpace(ctx, spaceID, tmpDir, "Crash Space", "Recovery test", "antigravity", nil)

	started := time.Now().UTC()
	del := &protocol.EventDelivery{
		ID:            "del_crash_1",
		EventID:       "evt_crash_1",
		SpaceID:       spaceID,
		ParticipantID: "codex",
		Status:        protocol.DeliveryProcessing,
		Attempt:       1,
		StartedAt:     &started,
	}
	if err := st1.RecordEventDelivery(ctx, del); err != nil {
		t.Fatalf("RecordEventDelivery failed: %v", err)
	}

	// 2. Simulate process crash by closing st1
	_ = st1.Close()

	// 3. Process restart: re-open store
	st2, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("re-opening SQLite failed: %v", err)
	}
	defer st2.Close()

	recoveredDel, err := st2.GetEventDelivery(ctx, "evt_crash_1", "codex")
	if err != nil || recoveredDel == nil {
		t.Fatalf("failed to recover delivery record: %v", err)
	}
	if recoveredDel.Status != protocol.DeliveryProcessing {
		t.Fatalf("expected processing status, got %s", recoveredDel.Status)
	}

	// Complete the delivery idempotently
	if err := st2.UpdateEventDeliveryStatus(ctx, "del_crash_1", protocol.DeliveryDelivered, "msg_recovered", ""); err != nil {
		t.Fatalf("UpdateEventDeliveryStatus failed: %v", err)
	}

	finalDel, _ := st2.GetEventDelivery(ctx, "evt_crash_1", "codex")
	if finalDel.Status != protocol.DeliveryDelivered || finalDel.ResultMessageID != "msg_recovered" {
		t.Fatalf("unexpected delivery state after recovery: %+v", finalDel)
	}
}
