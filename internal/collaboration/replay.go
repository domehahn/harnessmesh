package collaboration

import (
	"context"
	"fmt"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

// ReplaySession replays persisted events into the normal collaboration
// activation path without writing duplicate event rows. It is useful for
// incident analysis, deterministic recovery and chaos-test harnesses.
func (e *Engine) ReplaySession(ctx context.Context, sessionID string) (int, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("session id is required")
	}
	events, err := e.store.GetEvents(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	for _, stored := range events {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		event := &protocol.CollaborationEvent{
			ID: fmt.Sprintf("replay_%d", stored.ID), SpaceID: stored.SessionID, Type: stored.EventType,
			Timestamp: stored.Timestamp, Payload: stored.Payload,
		}
		e.handleCollaborationEvent(ctx, event)
	}
	return len(events), nil
}
