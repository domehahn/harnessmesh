package collaboration

import (
	"context"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/store"
)

func TestEngineFindingProjector_ProjectsOntoRealFinding(t *testing.T) {
	eng, st, _ := setupTestEngine(t, nil)
	ctx := context.Background()
	sessionID := "session-1"
	if err := st.SaveSession(ctx, &store.Session{ID: sessionID, Task: "t"}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	finding := &protocol.FindingPayload{ID: "F1", Claim: "sql injection", Status: protocol.FindingOpen, Timestamp: time.Now()}
	if err := st.SaveFinding(ctx, sessionID, finding); err != nil {
		t.Fatalf("SaveFinding: %v", err)
	}

	projector := NewEngineFindingProjector(eng)
	var _ review.FindingProjector = projector
	if err := projector.Project(ctx, sessionID, "F1", protocol.FindingVerified); err != nil {
		t.Fatalf("Project: %v", err)
	}

	got, err := st.GetFinding(ctx, sessionID, "F1")
	if err != nil {
		t.Fatalf("GetFinding: %v", err)
	}
	if got.Status != protocol.FindingVerified {
		t.Fatalf("expected the real, persisted finding's status to be projected to verified, got %q", got.Status)
	}
}

func TestEngineFindingProjector_NonexistentFindingFailsClosed(t *testing.T) {
	eng, st, _ := setupTestEngine(t, nil)
	ctx := context.Background()
	if err := st.SaveSession(ctx, &store.Session{ID: "session-1", Task: "t"}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	projector := NewEngineFindingProjector(eng)
	err := projector.Project(ctx, "session-1", "F-NOPE", protocol.FindingVerified)
	if err == nil {
		t.Fatalf("expected projecting onto a nonexistent finding to fail")
	}
}

func TestEngineEvidenceStore_PutGetRoundTripsBindingMetadata(t *testing.T) {
	eng, st, _ := setupTestEngine(t, nil)
	ctx := context.Background()
	if err := st.SaveSession(ctx, &store.Session{ID: "session-1", Task: "t"}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	evStore := NewEngineEvidenceStore(eng, "session-1")
	var _ review.EvidenceStore = evStore

	record := review.EvidenceRecord{
		ID: "E1", Tenant: "tenant-a", Artifact: "auth.go", ArtifactVersion: "v1",
		TaskID: "T1", ExecutionID: "exec-1", ProducerAgentID: "security-agent",
		Kind: "test_result", Digest: "sha256:abc", Status: review.EvidenceCurrent, CreatedAt: time.Now().UTC(),
	}
	if err := evStore.Put(ctx, record); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := evStore.Get(ctx, "E1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Tenant != "tenant-a" || got.ArtifactVersion != "v1" || got.Digest != "sha256:abc" || string(got.TaskID) != "T1" {
		t.Fatalf("binding metadata did not round-trip correctly: %+v", got)
	}

	exists, err := evStore.Exists(ctx, "E1")
	if err != nil || !exists {
		t.Fatalf("expected E1 to exist via the real persisted store, err=%v exists=%v", err, exists)
	}

	_, err = evStore.Get(ctx, "E-NOPE")
	var notFound *review.EvidenceNotFound
	if !isEvidenceNotFound(err, &notFound) {
		t.Fatalf("expected EvidenceNotFound for an unknown ID against the real store, got %v (%T)", err, err)
	}
}

func isEvidenceNotFound(err error, target **review.EvidenceNotFound) bool {
	nf, ok := err.(*review.EvidenceNotFound)
	if !ok {
		return false
	}
	*target = nf
	return true
}
