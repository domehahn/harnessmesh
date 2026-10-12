package review

import (
	"context"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

// FindingProjector is the one-way, Core-only path from an authoritative
// ReviewGroup status transition onto the corresponding
// protocol.FindingPayload.Status. ReviewState/ReviewGroup remain the
// single authoritative owner of the review decision; FindingPayload.Status
// is a PROJECTION of it, never the reverse - nothing in this package
// reads a FindingPayload's status back into a ReviewGroup. A real
// implementation (internal/collaboration's adapter) writes through to
// the existing collaboration.Engine/store.Store finding record; it never
// defines a second, competing status machine.
//
// Status mapping is the identity function: ReviewGroup.Status is
// already a protocol.FindingStatus value (the six Phase 4 verdict
// states), so "projecting" it onto FindingPayload.Status requires no
// translation table - Project's status argument IS the value to write,
// verbatim. This is deliberate: Phase 4 reused protocol.FindingStatus
// specifically so this phase would not need a lossy or hand-maintained
// mapping.
type FindingProjector interface {
	Project(ctx context.Context, sessionID, findingID string, status protocol.FindingStatus) error
}

// FakeFindingProjector is a deterministic, in-memory FindingProjector
// for tests - the review-layer analogue of FakeSupervisor/FakeArbiter.
type FakeFindingProjector struct {
	ProjectFunc func(ctx context.Context, sessionID, findingID string, status protocol.FindingStatus) error
	// Projected records every call this fake received, for test
	// assertions. Not safe for concurrent use without external
	// synchronization by the caller (tests only).
	Projected []ProjectedStatus
}

// ProjectedStatus records one FakeFindingProjector.Project call.
type ProjectedStatus struct {
	SessionID string
	FindingID string
	Status    protocol.FindingStatus
}

func (f *FakeFindingProjector) Project(ctx context.Context, sessionID, findingID string, status protocol.FindingStatus) error {
	f.Projected = append(f.Projected, ProjectedStatus{SessionID: sessionID, FindingID: findingID, Status: status})
	if f.ProjectFunc == nil {
		return nil
	}
	return f.ProjectFunc(ctx, sessionID, findingID, status)
}

var _ FindingProjector = (*FakeFindingProjector)(nil)
