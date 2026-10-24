package store

import (
	"context"
	"testing"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/tenant"
)

func TestPostgresGovernanceRequiresTenantIdentity(t *testing.T) {
	store, err := NewPostgresGovernanceStore(nil)
	if err == nil || store != nil {
		t.Fatal("expected nil database to be rejected")
	}
	// The validation is deliberately performed before any SQL is sent.
	var unconfigured *PostgresGovernanceStore
	err = unconfigured.SaveApproval(context.Background(), &protocol.ApprovalRequest{ID: "a"})
	if err == nil || err != tenant.ErrUnauthenticated {
		t.Fatalf("expected unauthenticated error, got %v", err)
	}
}
