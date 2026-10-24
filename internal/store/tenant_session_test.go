package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSessionTenantScoping(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.SaveSession(ctx, &Session{ID: "s-acme", TenantID: "tenant-acme", Task: "task", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSessionForTenant(ctx, "s-acme", "tenant-other"); err == nil {
		t.Fatal("cross-tenant session lookup succeeded")
	}
	sess, err := st.GetSessionForTenant(ctx, "s-acme", "tenant-acme")
	if err != nil {
		t.Fatal(err)
	}
	if sess.TenantID != "tenant-acme" {
		t.Fatalf("unexpected tenant: %+v", sess)
	}
	list, err := st.ListSessionsForTenant(ctx, "tenant-acme")
	if err != nil || len(list) != 1 {
		t.Fatalf("unexpected tenant sessions: %v %+v", err, list)
	}
}
