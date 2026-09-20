package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

func TestRetryOutboxClaimAndComplete(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.SaveSpace(ctx, &protocol.CollaborationSpace{ID: "space-outbox", Title: "outbox", WorkspaceID: ".", LifecycleState: protocol.SpaceStateActive, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueRetry(ctx, &RetryItem{ID: "retry-1", EventID: "event-1", SpaceID: "space-outbox", ParticipantID: "codex", Payload: []byte(`{"text":"retry"}`), NextAttempt: time.Now().UTC().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	items, err := st.ClaimRetries(ctx, time.Now().UTC(), 10)
	if err != nil || len(items) != 1 || items[0].Attempt != 1 {
		t.Fatalf("unexpected claimed retries: err=%v items=%+v", err, items)
	}
	if err := st.CompleteRetry(ctx, "retry-1", false, time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	items, err = st.ClaimRetries(ctx, time.Now().UTC().Add(time.Hour), 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("expected completed retry to be removed: err=%v items=%+v", err, items)
	}
	if err := st.EnqueueRetry(ctx, &RetryItem{ID: "retry-dead", EventID: "event-2", SpaceID: "space-outbox", ParticipantID: "codex", Payload: []byte(`{"text":"dead"}`), NextAttempt: time.Now().UTC().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeferRetry(ctx, "retry-dead", time.Now().UTC().Add(time.Hour), "quota exceeded"); err != nil {
		t.Fatal(err)
	}
	items, err = st.ClaimRetries(ctx, time.Now().UTC(), 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("expected quota-deferred retry to remain hidden: err=%v items=%+v", err, items)
	}
	if err := st.CompleteRetry(ctx, "retry-dead", false, time.Time{}, "permanent failure"); err != nil {
		t.Fatal(err)
	}
	var deadLetters int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_dead_letters WHERE id = ?`, "retry-dead").Scan(&deadLetters); err != nil || deadLetters != 1 {
		t.Fatalf("expected dead letter: err=%v count=%d", err, deadLetters)
	}
}

func TestOperationalStatePersistence(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	health := &protocol.AgentHealth{Agent: "codex", Adapter: "codex", Status: protocol.AgentHealthQuotaWait, QuotaResetAt: now.Add(time.Hour), UpdatedAt: now}
	if err := st.SaveAgentHealth(ctx, health); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAgentHealth(ctx, "codex")
	if err != nil || got == nil || got.Status != protocol.AgentHealthQuotaWait {
		t.Fatalf("health persistence failed: err=%v health=%+v", err, got)
	}
	approval := &protocol.ApprovalRequest{ID: "approval-1", Agent: "codex", Reason: "expensive model", Status: protocol.ApprovalPending, CreatedAt: now}
	if err := st.SaveApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	approval.Status = protocol.ApprovalApproved
	approval.DecidedBy = "human"
	if err := st.SaveApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	gotApproval, err := st.GetApproval(ctx, "approval-1")
	if err != nil || gotApproval == nil || gotApproval.Status != protocol.ApprovalApproved {
		t.Fatalf("approval persistence failed: err=%v approval=%+v", err, gotApproval)
	}
	ledger := &BudgetLedger{Scope: "global", UsedTokens: 42, UsedCostUSD: 0.25, MaxTokens: 1000, MaxCostUSD: 10}
	if err := st.UpdateGlobalBudget(ctx, ledger); err != nil {
		t.Fatal(err)
	}
	gotLedger, err := st.GetGlobalBudget(ctx, "global")
	if err != nil || gotLedger == nil || gotLedger.UsedTokens != 42 {
		t.Fatalf("budget persistence failed: err=%v ledger=%+v", err, gotLedger)
	}
	if ok, err := st.AcquireLease(ctx, "worker", "node-a", time.Minute); err != nil || !ok {
		t.Fatalf("expected lease acquisition: ok=%v err=%v", ok, err)
	}
	if ok, err := st.AcquireLease(ctx, "worker", "node-b", time.Minute); err != nil || ok {
		t.Fatalf("expected competing lease rejection: ok=%v err=%v", ok, err)
	}
	if err := st.ReleaseLease(ctx, "worker", "node-a"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveJobDependency(ctx, &JobDependency{JobID: "job-b", DependsOn: "job-a", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	deps, err := st.ListJobDependencies(ctx, "job-b")
	if err != nil || len(deps) != 1 || deps[0].DependsOn != "job-a" {
		t.Fatalf("dependency persistence failed: err=%v deps=%+v", err, deps)
	}
}
