package provenance

import (
	"context"
	"time"
)

// EventSink is the minimal persistence boundary used by transports that
// already have an event store. Keeping this interface small makes provenance
// usable by SQLite, PostgreSQL and test stores without coupling the package to
// the collaboration store implementation.
type EventSink interface {
	EmitEvent(context.Context, string, string, any) error
}

// Recorder appends a tamper-evident in-memory chain and optionally persists a
// redacted chain projection. Payloads are never persisted by Recorder; only
// the event digest and chain metadata are emitted to the sink.
type Recorder struct {
	Chain *Chain
	Sink  EventSink
}

func NewRecorder(sink EventSink) *Recorder {
	return &Recorder{Chain: New(), Sink: sink}
}

func (r *Recorder) Record(ctx context.Context, sessionID, kind, subject string, payload any) (Event, error) {
	event, err := r.Chain.Append(kind, subject, payload, time.Now().UTC())
	if err != nil {
		return Event{}, err
	}
	if r.Sink != nil {
		_ = r.Sink.EmitEvent(ctx, sessionID, "provenance."+kind, map[string]any{
			"sequence":   event.Sequence,
			"digest":     event.Digest,
			"previous":   event.Previous,
			"chain_root": r.Chain.Root(),
			"subject":    event.Subject,
		})
	}
	return event, nil
}
