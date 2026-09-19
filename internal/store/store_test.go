package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

func TestSQLiteStoreLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_harnessmesh.db")

	store, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	sessionID := "hm_test_001"

	// 1. Save Session
	sess := &Session{
		ID:        sessionID,
		RepoRoot:  "/tmp/repo",
		Task:      "Test task",
		Status:    "active",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Budget:    protocol.BudgetStatus{Known: true, MaxCostUSD: 10.0},
		Participants: map[string]ParticipantInfo{
			"claude": {
				AgentName: "claude",
				Role:      "executor",
				Writable:  true,
			},
			"codex": {
				AgentName: "codex",
				Role:      "reviewer",
				Writable:  false,
			},
		},
	}

	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// 2. Retrieve Session
	retrieved, err := store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if retrieved.ID != sessionID || len(retrieved.Participants) != 2 {
		t.Fatalf("unexpected session data: %+v", retrieved)
	}

	// 3. Save Message
	msg := &protocol.PeerEnvelope{
		ID:        "msg_001",
		SessionID: sessionID,
		From:      "claude",
		To:        "codex",
		Type:      protocol.MsgReviewRequest,
		Depth:     1,
		CreatedAt: time.Now().UTC(),
		Payload:   []byte(`{"scope": ["internal/"]}`),
	}
	if err := store.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	messages, err := store.GetMessages(ctx, sessionID)
	if err != nil || len(messages) != 1 {
		t.Fatalf("GetMessages failed: err=%v, count=%d", err, len(messages))
	}

	// 4. Save Finding
	finding := &protocol.FindingPayload{
		ID:             "HM-RACE-001",
		SourceAgent:    "codex",
		Severity:       "high",
		Category:       "concurrency",
		File:           "internal/auth/token.go",
		Line:           87,
		Claim:          "Race condition on token refresh",
		Evidence:       "Unprotected map access",
		Recommendation: "Use sync.RWMutex",
		Status:         protocol.FindingOpen,
		Timestamp:      time.Now().UTC(),
	}
	if err := store.SaveFinding(ctx, sessionID, finding); err != nil {
		t.Fatalf("SaveFinding failed: %v", err)
	}

	findings, err := store.GetFindings(ctx, sessionID)
	if err != nil || len(findings) != 1 || findings[0].Status != protocol.FindingOpen {
		t.Fatalf("GetFindings failed: err=%v, count=%d", err, len(findings))
	}

	// 5. Submit Evidence
	exitCode := 1
	evidence := &protocol.EvidencePayload{
		ID:          "ev_001",
		FindingID:   finding.ID,
		MessageID:   msg.ID,
		SourceAgent: "codex",
		Type:        protocol.EvidenceTestResult,
		Command:     "go test -race ./...",
		Result:      "FAIL",
		Excerpt:     "DATA RACE Write at 0x00c0001",
		ExitCode:    &exitCode,
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.SaveEvidence(ctx, sessionID, evidence); err != nil {
		t.Fatalf("SaveEvidence failed: %v", err)
	}

	evList, err := store.GetEvidence(ctx, sessionID)
	if err != nil || len(evList) != 1 {
		t.Fatalf("GetEvidence failed: err=%v, count=%d", err, len(evList))
	}

	// 6. Challenge Finding (marks as disputed)
	challenge := &protocol.ChallengePayload{
		ID:                    "ch_001",
		FindingID:             finding.ID,
		Challenger:            "claude",
		Claim:                 "session.mu is already held by caller",
		Evidence:              "internal/auth/token.go:75",
		RequestedVerification: "Inspect caller call site",
		CreatedAt:             time.Now().UTC(),
	}
	if err := store.SaveChallenge(ctx, sessionID, challenge); err != nil {
		t.Fatalf("SaveChallenge failed: %v", err)
	}

	fAfterChallenge, err := store.GetFinding(ctx, sessionID, finding.ID)
	if err != nil || fAfterChallenge.Status != protocol.FindingDisputed {
		t.Fatalf("expected finding to be disputed, got status=%v", fAfterChallenge.Status)
	}

	// 7. Resolve Finding
	resolution := &protocol.ResolutionPayload{
		ID:                 "res_001",
		FindingID:          finding.ID,
		Status:             protocol.ResolutionRejected,
		Rationale:          "Caller indeed holds session.mu as shown in token.go:75",
		EvidenceReferences: []string{"ev_001"},
		ResolvingAgent:     "codex",
		Timestamp:          time.Now().UTC(),
	}
	if err := store.SaveResolution(ctx, sessionID, resolution); err != nil {
		t.Fatalf("SaveResolution failed: %v", err)
	}

	fAfterResolution, err := store.GetFinding(ctx, sessionID, finding.ID)
	if err != nil || fAfterResolution.Status != protocol.FindingDismissed {
		t.Fatalf("expected finding to be dismissed after rejected resolution, got status=%v", fAfterResolution.Status)
	}

	// 8. Idempotency test (session-scoped)
	if err := store.SaveIdempotency(ctx, "idem_123", sessionID, `{"status": "ok"}`); err != nil {
		t.Fatalf("SaveIdempotency failed: %v", err)
	}
	resJSON, err := store.GetIdempotency(ctx, sessionID, "idem_123")
	if err != nil || resJSON != `{"status": "ok"}` {
		t.Fatalf("GetIdempotency failed: res=%s, err=%v", resJSON, err)
	}

	// Same key in another session should be isolated
	otherSession := "session_other_999"
	if err := store.SaveIdempotency(ctx, "idem_123", otherSession, `{"status": "other"}`); err != nil {
		t.Fatalf("SaveIdempotency other failed: %v", err)
	}
	resOther, err := store.GetIdempotency(ctx, otherSession, "idem_123")
	if err != nil || resOther != `{"status": "other"}` {
		t.Fatalf("GetIdempotency other failed: res=%s, err=%v", resOther, err)
	}
	resOriginal, err := store.GetIdempotency(ctx, sessionID, "idem_123")
	if err != nil || resOriginal != `{"status": "ok"}` {
		t.Fatalf("original session idempotency corrupted by other session: %s", resOriginal)
	}
}

func TestSQLiteStoreMigrationV1ToV2(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "v1_to_v2.db")

	// 1. Manually initialize v1 database
	db, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite failed: %v", err)
	}

	ctx := context.Background()
	// Save session first to satisfy foreign key constraint
	if err := db.SaveSession(ctx, &Session{
		ID:        "hm_mig_1",
		RepoRoot:  "/tmp",
		Task:      "migration test",
		Status:    "active",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Insert a finding with v2 fields
	f := &protocol.FindingPayload{
		ID:                "FINDING-001",
		SourceAgent:       "reviewer",
		SourceParticipant: "reviewer-participant",
		SourceAdapter:     "codex",
		Severity:          "high",
		Category:          "security",
		Claim:             "SQL injection",
		Evidence:          "query formatting",
		Recommendation:    "use parameterized queries",
		DuplicateOf:       "FINDING-ORIG",
		RelatedFindings:   []string{"FINDING-002"},
		EvidenceRefs:      []string{"ev_1"},
		Status:            protocol.FindingOpen,
	}

	if err := db.SaveFinding(ctx, "hm_mig_1", f); err != nil {
		t.Fatalf("SaveFinding failed: %v", err)
	}

	ret, err := db.GetFinding(ctx, "hm_mig_1", "FINDING-001")
	if err != nil {
		t.Fatalf("GetFinding failed: %v", err)
	}
	if ret.DuplicateOf != "FINDING-ORIG" {
		t.Errorf("expected DuplicateOf 'FINDING-ORIG', got %q", ret.DuplicateOf)
	}
	if len(ret.RelatedFindings) != 1 || ret.RelatedFindings[0] != "FINDING-002" {
		t.Errorf("unexpected RelatedFindings: %+v", ret.RelatedFindings)
	}
	if ret.SourceParticipant != "reviewer-participant" {
		t.Errorf("expected SourceParticipant 'reviewer-participant', got %q", ret.SourceParticipant)
	}

	// Also verify GetEvidenceByFinding
	ev := &protocol.EvidencePayload{
		ID:          "ev_mig_1",
		FindingID:   "FINDING-001",
		SourceAgent: "reviewer",
		Type:        protocol.EvidenceCodeLocation,
		Result:      "line 42",
		CreatedAt:   time.Now().UTC(),
	}
	if err := db.SaveEvidence(ctx, "hm_mig_1", ev); err != nil {
		t.Fatalf("SaveEvidence failed: %v", err)
	}

	evList, err := db.GetEvidenceByFinding(ctx, "hm_mig_1", "FINDING-001")
	if err != nil {
		t.Fatalf("GetEvidenceByFinding failed: %v", err)
	}
	if len(evList) != 1 || evList[0].ID != "ev_mig_1" {
		t.Fatalf("unexpected evidence returned: %+v", evList)
	}

	db.Close()
}

func TestSQLiteStore_RoutingDecisionsAndUsage(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenSQLite(filepath.Join(tmpDir, "decisions_usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	sessID := "hm_dec_1"
	if err := db.SaveSession(ctx, &Session{
		ID:        sessID,
		RepoRoot:  tmpDir,
		Task:      "Test decisions and usage",
		Status:    "active",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Save and Get RoutingDecision
	dec := &RoutingDecisionRecord{
		ID:                  "rd_001",
		SessionID:           sessID,
		RequestedCapability: "security",
		TaskTier:            "critical",
		Eligible:            []string{"sec-peer", "arch-peer"},
		Selected:            "sec-peer",
		Reason:              []string{"capability_match", "lowest_relative_cost"},
		Timestamp:           time.Now().UTC(),
	}
	if err := db.SaveRoutingDecision(ctx, dec); err != nil {
		t.Fatalf("SaveRoutingDecision failed: %v", err)
	}

	decs, err := db.GetRoutingDecisions(ctx, sessID)
	if err != nil || len(decs) != 1 {
		t.Fatalf("GetRoutingDecisions failed: err=%v, len=%d", err, len(decs))
	}
	if decs[0].Selected != "sec-peer" || decs[0].TaskTier != "critical" {
		t.Fatalf("unexpected decision: %+v", decs[0])
	}

	// 2. Save and Get Usage
	usage := &UsageRecord{
		ID:                  "usg_001",
		SessionID:           sessID,
		Participant:         "sec-peer",
		Adapter:             "antigravity",
		Model:               "gemini-1.5-pro",
		ModelRoutingBackend: "switchyard",
		Route:               "security",
		InputTokens:         1200,
		OutputTokens:        350,
		DurationMS:          1500,
		MonetaryCostUSD:     0.005,
		EscalationCount:     1,
		CostSource:          "provider",
		Timestamp:           time.Now().UTC(),
	}
	if err := db.SaveUsage(ctx, usage); err != nil {
		t.Fatalf("SaveUsage failed: %v", err)
	}

	usages, err := db.GetUsage(ctx, sessID)
	if err != nil || len(usages) != 1 {
		t.Fatalf("GetUsage failed: err=%v, len=%d", err, len(usages))
	}
	if usages[0].InputTokens != 1200 || usages[0].ModelRoutingBackend != "switchyard" {
		t.Fatalf("unexpected usage: %+v", usages[0])
	}

	// 3. Emit and Get Events
	if err := db.EmitEvent(ctx, sessID, "session.started", map[string]any{"task": "test"}); err != nil {
		t.Fatalf("EmitEvent failed: %v", err)
	}
	if err := db.EmitEvent(ctx, sessID, "routing.escalated", map[string]any{"capability": "security"}); err != nil {
		t.Fatalf("EmitEvent failed: %v", err)
	}

	events, err := db.GetEvents(ctx, sessID)
	if err != nil || len(events) != 2 {
		t.Fatalf("GetEvents failed: err=%v, count=%d", err, len(events))
	}
	if events[0].EventType != "session.started" || events[1].EventType != "routing.escalated" {
		t.Fatalf("unexpected event types: %s, %s", events[0].EventType, events[1].EventType)
	}
}

func TestSQLiteStore_V03Collaboration(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenSQLite(filepath.Join(tmpDir, "v03_collab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	spaceID := "space_test_01"

	// 1. Space CRUD
	space := &protocol.CollaborationSpace{
		ID:                spaceID,
		WorkspaceID:       "/path/to/repo",
		Title:             "V03 Collab Space",
		Purpose:           "Test collaboration engine persistence",
		LifecycleState:    protocol.SpaceStateActive,
		WriterParticipant: "antigravity",
		Participants: map[string]protocol.SpaceParticipant{
			"antigravity": {
				ID:           "antigravity",
				Adapter:      "antigravity",
				Roles:        []string{"executor", "primary"},
				Capabilities: []string{"code_edit", "test_runner"},
				Mode:         protocol.ParticipantModeActive,
				Writable:     true,
			},
			"codex": {
				ID:           "codex",
				Adapter:      "codex",
				Roles:        []string{"reviewer"},
				Capabilities: []string{"review", "architecture"},
				Mode:         protocol.ParticipantModeOnDemand,
				Writable:     false,
			},
		},
		Channels: map[string]protocol.Channel{
			"general": {
				ID:          "general",
				SpaceID:     spaceID,
				Name:        "general",
				Description: "General discussion",
				Visibility:  protocol.ChannelVisibilityAll,
				CreatedBy:   "system",
			},
		},
	}

	if err := db.SaveSpace(ctx, space); err != nil {
		t.Fatalf("SaveSpace failed: %v", err)
	}

	retSpace, err := db.GetSpace(ctx, spaceID)
	if err != nil {
		t.Fatalf("GetSpace failed: %v", err)
	}
	if retSpace.ID != spaceID || len(retSpace.Participants) != 2 || len(retSpace.Channels) != 1 {
		t.Fatalf("unexpected space loaded: %+v", retSpace)
	}

	spaces, err := db.ListSpaces(ctx)
	if err != nil || len(spaces) != 1 {
		t.Fatalf("ListSpaces failed: err=%v, count=%d", err, len(spaces))
	}

	if err := db.UpdateSpaceLifecycle(ctx, spaceID, protocol.SpaceStatePaused); err != nil {
		t.Fatalf("UpdateSpaceLifecycle failed: %v", err)
	}
	retSpace, _ = db.GetSpace(ctx, spaceID)
	if retSpace.LifecycleState != protocol.SpaceStatePaused {
		t.Fatalf("expected lifecycle paused, got %v", retSpace.LifecycleState)
	}

	// 2. Channel CRUD
	ch2 := &protocol.Channel{
		ID:          "security",
		SpaceID:     spaceID,
		Name:        "security",
		Description: "Security findings and audit trail",
		Visibility:  protocol.ChannelVisibilityAll,
		CreatedBy:   "antigravity",
	}
	if err := db.CreateChannel(ctx, ch2); err != nil {
		t.Fatalf("CreateChannel failed: %v", err)
	}

	channels, err := db.ListChannels(ctx, spaceID)
	if err != nil || len(channels) != 2 {
		t.Fatalf("ListChannels failed: err=%v, count=%d", err, len(channels))
	}

	retCh, err := db.GetChannel(ctx, spaceID, "security")
	if err != nil || retCh.Name != "security" {
		t.Fatalf("GetChannel failed: err=%v, ch=%+v", err, retCh)
	}

	// 3. Thread CRUD
	th := &protocol.Thread{
		ID:            "th_001",
		SpaceID:       spaceID,
		ChannelID:     "security",
		RootMessageID: "msg_root_1",
		Title:         "Review SQL injection risk",
		Status:        protocol.ThreadStatusOpen,
	}
	if err := db.CreateThread(ctx, th); err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	retTh, err := db.GetThread(ctx, "th_001")
	if err != nil || retTh.Title != "Review SQL injection risk" {
		t.Fatalf("GetThread failed: err=%v, th=%+v", err, retTh)
	}

	threads, err := db.ListThreads(ctx, spaceID, "security")
	if err != nil || len(threads) != 1 {
		t.Fatalf("ListThreads failed: err=%v, count=%d", err, len(threads))
	}

	// 4. Messages in space/channel/thread
	msg := &protocol.PeerEnvelope{
		ID:        "msg_collab_1",
		SpaceID:   spaceID,
		ChannelID: "security",
		ThreadID:  "th_001",
		From:      "antigravity",
		To:        "channel:security",
		Type:      protocol.MsgMessage,
		Depth:     1,
		Mentions:  []string{"codex"},
		Scope:     []string{"internal/store/store.go"},
		Payload:   []byte(`{"text":"Check parameter sanitization"}`),
	}
	if err := db.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}

	spaceMsgs, err := db.GetSpaceMessages(ctx, spaceID, "security", "th_001", 10)
	if err != nil || len(spaceMsgs) != 1 {
		t.Fatalf("GetSpaceMessages failed: err=%v, count=%d", err, len(spaceMsgs))
	}
	if spaceMsgs[0].Mentions[0] != "codex" || spaceMsgs[0].Scope[0] != "internal/store/store.go" {
		t.Fatalf("unexpected message content: %+v", spaceMsgs[0])
	}

	// 5. Subscription CRUD
	sub := &protocol.Subscription{
		ID:            "sub_codex_sec",
		SpaceID:       spaceID,
		ParticipantID: "codex",
		Channels:      []string{"security"},
		EventTypes:    []string{protocol.EventFindingCreated, protocol.EventMessageCreated},
		ScopePatterns: []string{"internal/**"},
		Mode:          protocol.ParticipantModeActive,
	}
	if err := db.SaveSubscription(ctx, sub); err != nil {
		t.Fatalf("SaveSubscription failed: %v", err)
	}

	subs, err := db.GetSubscriptions(ctx, spaceID)
	if err != nil || len(subs) != 1 {
		t.Fatalf("GetSubscriptions failed: err=%v, count=%d", err, len(subs))
	}
	pSubs, err := db.GetParticipantSubscriptions(ctx, spaceID, "codex")
	if err != nil || len(pSubs) != 1 {
		t.Fatalf("GetParticipantSubscriptions failed: err=%v, count=%d", err, len(pSubs))
	}

	if err := db.DeleteSubscription(ctx, spaceID, "sub_codex_sec"); err != nil {
		t.Fatalf("DeleteSubscription failed: %v", err)
	}
	subsAfter, _ := db.GetSubscriptions(ctx, spaceID)
	if len(subsAfter) != 0 {
		t.Fatalf("expected 0 subs after delete, got %d", len(subsAfter))
	}

	// 6. Event Delivery idempotency
	started := time.Now().UTC()
	delivery := &protocol.EventDelivery{
		ID:            "del_001",
		EventID:       "evt_001",
		SpaceID:       spaceID,
		ParticipantID: "codex",
		Status:        protocol.DeliveryProcessing,
		Attempt:       1,
		StartedAt:     &started,
	}
	if err := db.RecordEventDelivery(ctx, delivery); err != nil {
		t.Fatalf("RecordEventDelivery failed: %v", err)
	}

	retDel, err := db.GetEventDelivery(ctx, "evt_001", "codex")
	if err != nil || retDel == nil || retDel.Status != protocol.DeliveryProcessing {
		t.Fatalf("GetEventDelivery failed: err=%v, del=%+v", err, retDel)
	}

	if err := db.UpdateEventDeliveryStatus(ctx, "del_001", protocol.DeliveryDelivered, "msg_res_1", ""); err != nil {
		t.Fatalf("UpdateEventDeliveryStatus failed: %v", err)
	}
	retDel2, _ := db.GetEventDelivery(ctx, "evt_001", "codex")
	if retDel2.Status != protocol.DeliveryDelivered || retDel2.ResultMessageID != "msg_res_1" {
		t.Fatalf("unexpected delivery after update: %+v", retDel2)
	}

	// 7. Decision CRUD
	dec := &protocol.Decision{
		ID:           "dec_001",
		SpaceID:      spaceID,
		Title:        "Use SQLite parameterized queries",
		Statement:    "All database operations must use parameterized queries",
		Rationale:    "Prevents SQL injection vulnerabilities",
		EvidenceRefs: []string{"ev_001"},
		ProposedBy:   "antigravity",
		AcceptedBy:   []string{"codex"},
		Status:       protocol.DecisionAccepted,
	}
	if err := db.SaveDecision(ctx, dec); err != nil {
		t.Fatalf("SaveDecision failed: %v", err)
	}

	retDec, err := db.GetDecision(ctx, spaceID, "dec_001")
	if err != nil || retDec.Status != protocol.DecisionAccepted {
		t.Fatalf("GetDecision failed: err=%v, dec=%+v", err, retDec)
	}
	decs, err := db.ListDecisions(ctx, spaceID)
	if err != nil || len(decs) != 1 {
		t.Fatalf("ListDecisions failed: err=%v, count=%d", err, len(decs))
	}

	// 8. Participant Cursor
	cursor := &protocol.ParticipantCursor{
		SpaceID:           spaceID,
		ParticipantID:     "codex",
		Mode:              protocol.ParticipantModeActive,
		LastSeenMessageID: "msg_collab_1",
		LastSeenEventID:   "evt_001",
	}
	if err := db.UpdateParticipantCursor(ctx, cursor); err != nil {
		t.Fatalf("UpdateParticipantCursor failed: %v", err)
	}
	retCursor, err := db.GetParticipantCursor(ctx, spaceID, "codex")
	if err != nil || retCursor.LastSeenMessageID != "msg_collab_1" {
		t.Fatalf("GetParticipantCursor failed: err=%v, cursor=%+v", err, retCursor)
	}

	// 9. Summaries
	if err := db.SaveSummary(ctx, spaceID, "thread", "th_001", "Discussion on SQL query safety concluded with parameterized query policy.", []string{"msg_collab_1"}); err != nil {
		t.Fatalf("SaveSummary failed: %v", err)
	}
	summaries, err := db.GetSummaries(ctx, spaceID, "th_001")
	if err != nil || len(summaries) != 1 {
		t.Fatalf("GetSummaries failed: err=%v, count=%d", err, len(summaries))
	}
}

func TestStore_MultiSpaceCompositePK_Isolation(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenSQLite(filepath.Join(tmpDir, "collab_iso.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()

	// Space A
	spaceA := &protocol.CollaborationSpace{
		ID:             "space_alpha",
		Title:          "Alpha Space",
		WorkspaceID:    "/tmp/alpha",
		LifecycleState: protocol.SpaceStateActive,
		Channels: map[string]protocol.Channel{
			"general": {
				ID:         "general",
				SpaceID:    "space_alpha",
				Name:       "alpha general",
				Visibility: protocol.ChannelVisibilityAll,
			},
		},
	}
	if err := db.SaveSpace(ctx, spaceA); err != nil {
		t.Fatalf("SaveSpace alpha failed: %v", err)
	}

	// Space B
	spaceB := &protocol.CollaborationSpace{
		ID:             "space_beta",
		Title:          "Beta Space",
		WorkspaceID:    "/tmp/beta",
		LifecycleState: protocol.SpaceStateActive,
		Channels: map[string]protocol.Channel{
			"general": {
				ID:         "general",
				SpaceID:    "space_beta",
				Name:       "beta general",
				Visibility: protocol.ChannelVisibilityAll,
			},
		},
	}
	if err := db.SaveSpace(ctx, spaceB); err != nil {
		t.Fatalf("SaveSpace beta failed: %v", err)
	}

	// Subscriptions with identical ID across different spaces
	subA := &protocol.Subscription{
		ID:            "sub_claude_default",
		SpaceID:       "space_alpha",
		ParticipantID: "claude",
		Channels:      []string{"general"},
		EventTypes:    []string{protocol.EventRepoChanged},
	}
	if err := db.SaveSubscription(ctx, subA); err != nil {
		t.Fatalf("SaveSubscription A failed: %v", err)
	}

	subB := &protocol.Subscription{
		ID:            "sub_claude_default",
		SpaceID:       "space_beta",
		ParticipantID: "claude",
		Channels:      []string{"general"},
		EventTypes:    []string{protocol.EventCommitCreated},
	}
	if err := db.SaveSubscription(ctx, subB); err != nil {
		t.Fatalf("SaveSubscription B failed: %v", err)
	}

	// Verify channels in both spaces remain isolated
	chA, err := db.GetChannel(ctx, "space_alpha", "general")
	if err != nil || chA == nil || chA.Name != "alpha general" {
		t.Fatalf("unexpected channel A: err=%v, ch=%+v", err, chA)
	}

	chB, err := db.GetChannel(ctx, "space_beta", "general")
	if err != nil || chB == nil || chB.Name != "beta general" {
		t.Fatalf("unexpected channel B: err=%v, ch=%+v", err, chB)
	}

	// Verify subscriptions in both spaces remain isolated
	subsA, err := db.GetParticipantSubscriptions(ctx, "space_alpha", "claude")
	if err != nil || len(subsA) != 1 || subsA[0].EventTypes[0] != protocol.EventRepoChanged {
		t.Fatalf("unexpected subs A: err=%v, subs=%+v", err, subsA)
	}

	subsB, err := db.GetParticipantSubscriptions(ctx, "space_beta", "claude")
	if err != nil || len(subsB) != 1 || subsB[0].EventTypes[0] != protocol.EventCommitCreated {
		t.Fatalf("unexpected subs B: err=%v, subs=%+v", err, subsB)
	}

	// Verify DeleteSubscription scoped to space_alpha does not touch space_beta
	if err := db.DeleteSubscription(ctx, "space_alpha", "sub_claude_default"); err != nil {
		t.Fatalf("DeleteSubscription alpha failed: %v", err)
	}
	subsAAfter, _ := db.GetParticipantSubscriptions(ctx, "space_alpha", "claude")
	if len(subsAAfter) != 0 {
		t.Fatalf("expected 0 subs in alpha after delete, got %d", len(subsAAfter))
	}
	subsBAfter, _ := db.GetParticipantSubscriptions(ctx, "space_beta", "claude")
	if len(subsBAfter) != 1 {
		t.Fatalf("expected 1 sub in beta preserved after alpha delete, got %d", len(subsBAfter))
	}
}

func TestMigration_V5ToV7_PreservesThreadsAndChannels(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "v5_upgrade.db")

	// 1. Manually construct a v5 database with original v5 channels and threads foreign keys
	rawDB, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=ON")
	if err != nil {
		t.Fatal(err)
	}

	initSQL := `
		CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TIMESTAMP NOT NULL);
		INSERT INTO schema_migrations (version, applied_at) VALUES (1, CURRENT_TIMESTAMP), (2, CURRENT_TIMESTAMP), (3, CURRENT_TIMESTAMP), (4, CURRENT_TIMESTAMP), (5, CURRENT_TIMESTAMP), (6, CURRENT_TIMESTAMP);

		CREATE TABLE collaboration_spaces (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL,
			purpose TEXT NOT NULL DEFAULT '',
			lifecycle_state TEXT NOT NULL DEFAULT 'active',
			writer_participant TEXT NOT NULL DEFAULT '',
			budget_json TEXT NOT NULL DEFAULT '{}',
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);

		CREATE TABLE channels (
			id TEXT PRIMARY KEY,
			space_id TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			visibility TEXT NOT NULL DEFAULT 'all_participants',
			allowed_participants_json TEXT NOT NULL DEFAULT '[]',
			allowed_capabilities_json TEXT NOT NULL DEFAULT '[]',
			created_by TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL,
			archived_at TIMESTAMP,
			FOREIGN KEY (space_id) REFERENCES collaboration_spaces(id) ON DELETE CASCADE
		);

		CREATE TABLE threads (
			id TEXT PRIMARY KEY,
			space_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			root_message_id TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'open',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY (space_id) REFERENCES collaboration_spaces(id) ON DELETE CASCADE,
			FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE
		);

		CREATE TABLE subscriptions (
			id TEXT PRIMARY KEY,
			space_id TEXT NOT NULL,
			participant_id TEXT NOT NULL,
			channels_json TEXT NOT NULL DEFAULT '[]',
			event_types_json TEXT NOT NULL DEFAULT '[]',
			scope_patterns_json TEXT NOT NULL DEFAULT '[]',
			mode TEXT NOT NULL DEFAULT 'active',
			created_at TIMESTAMP NOT NULL,
			FOREIGN KEY (space_id) REFERENCES collaboration_spaces(id) ON DELETE CASCADE
		);

		INSERT INTO collaboration_spaces (id, workspace_id, title, purpose, lifecycle_state, writer_participant, budget_json, metadata_json, created_at, updated_at)
		VALUES ('sp_test', '.', 'Test Space', 'Test', 'active', 'user', '{}', '{}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

		INSERT INTO channels (id, space_id, name, description, visibility, created_by, created_at)
		VALUES ('general', 'sp_test', 'general', 'General channel', 'all_participants', 'system', CURRENT_TIMESTAMP);

		INSERT INTO threads (id, space_id, channel_id, root_message_id, title, status, created_at, updated_at)
		VALUES ('th_1', 'sp_test', 'general', 'msg_1', 'Important Thread', 'open', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);

		INSERT INTO subscriptions (id, space_id, participant_id, channels_json, event_types_json, scope_patterns_json, mode, created_at)
		VALUES ('sub_1', 'sp_test', 'user', '["general"]', '["repository.changed"]', '[]', 'active', CURRENT_TIMESTAMP);
	`
	if _, err := rawDB.Exec(initSQL); err != nil {
		rawDB.Close()
		t.Fatalf("failed to setup v5/v6 database: %v", err)
	}
	rawDB.Close()

	// 2. Open with HarnessMesh OpenSQLite, which runs Migration 7
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite failed during migration: %v", err)
	}
	defer st.Close()

	ctx := context.Background()

	// Verify thread still exists (not cascade deleted)
	th, err := st.GetThread(ctx, "th_1")
	if err != nil || th == nil || th.Title != "Important Thread" {
		t.Fatalf("thread th_1 lost or corrupt after migration: err=%v, th=%+v", err, th)
	}

	// Verify channel still exists
	ch, err := st.GetChannel(ctx, "sp_test", "general")
	if err != nil || ch == nil || ch.Name != "general" {
		t.Fatalf("channel general lost or corrupt after migration: err=%v, ch=%+v", err, ch)
	}

	// Verify subscription still exists
	subs, err := st.GetParticipantSubscriptions(ctx, "sp_test", "user")
	if err != nil || len(subs) != 1 || subs[0].ID != "sub_1" {
		t.Fatalf("subscription sub_1 lost or corrupt after migration: err=%v, subs=%+v", err, subs)
	}

	// Verify new threads can be created referencing the composite channel without FK error
	newTh := &protocol.Thread{
		ID:            "th_2",
		SpaceID:       "sp_test",
		ChannelID:     "general",
		RootMessageID: "msg_2",
		Title:         "Second Thread Post-Migration",
		Status:        protocol.ThreadStatusOpen,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := st.CreateThread(ctx, newTh); err != nil {
		t.Fatalf("CreateThread failed post-migration: %v", err)
	}
}

func TestStore_DeleteSubscription_RequiresSpaceID(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenSQLite(filepath.Join(tmpDir, "subs_delete.db"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Deleting with empty spaceID must fail
	if err := db.DeleteSubscription(ctx, "", "sub_123"); err == nil {
		t.Fatal("expected error when deleting subscription with empty space_id, got nil")
	}

	// Deleting with empty id must fail
	if err := db.DeleteSubscription(ctx, "sp_test", ""); err == nil {
		t.Fatal("expected error when deleting subscription with empty id, got nil")
	}
}

func TestStore_AtomicUpdateBudget(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := OpenSQLite(filepath.Join(tmpDir, "budget_atomic.db"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// 1. Nonexistent ID fails
	err = db.UpdateBudget(ctx, "nonexistent", protocol.BudgetStatus{Known: true, MaxTotalTokens: 100})
	if err == nil {
		t.Fatal("expected error updating budget for nonexistent session/space, got nil")
	}

	// 2. Create space (which mirrors to sessions)
	sp := &protocol.CollaborationSpace{
		ID:             "sp_atomic_1",
		WorkspaceID:    ".",
		Title:          "Atomic Space",
		LifecycleState: protocol.SpaceStateActive,
		Budget: protocol.BudgetStatus{
			Known:          true,
			MaxTotalTokens: 1000,
			UsedTotalTokens: 0,
		},
	}
	if err := db.SaveSpace(ctx, sp); err != nil {
		t.Fatal(err)
	}

	// 3. Atomically update budget
	newBudget := protocol.BudgetStatus{
		Known:           true,
		MaxTotalTokens:  1000,
		UsedTotalTokens: 350,
		UsedInputTokens: 150,
		UsedOutputTokens: 200,
		UsedCostUSD:     0.02,
	}
	if err := db.UpdateBudget(ctx, "sp_atomic_1", newBudget); err != nil {
		t.Fatalf("UpdateBudget failed: %v", err)
	}

	// 4. Verify both space and session reflect updated budget
	spAfter, err := db.GetSpace(ctx, "sp_atomic_1")
	if err != nil {
		t.Fatal(err)
	}
	if spAfter.Budget.UsedTotalTokens != 350 || spAfter.Budget.UsedOutputTokens != 200 {
		t.Fatalf("space budget not updated: %+v", spAfter.Budget)
	}

	sessAfter, err := db.GetSession(ctx, "sp_atomic_1")
	if err != nil {
		t.Fatal(err)
	}
	if sessAfter.Budget.UsedTotalTokens != 350 || sessAfter.Budget.UsedOutputTokens != 200 {
		t.Fatalf("session budget not updated: %+v", sessAfter.Budget)
	}
}
