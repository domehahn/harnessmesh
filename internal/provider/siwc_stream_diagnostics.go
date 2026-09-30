package provider

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"time"
)

// This file implements safe, opt-in diagnostic instrumentation for the
// SSE relay boundary itself - added to let a real Codex VS Code stream-
// lifecycle defect ("OutputTextDelta without active item") be proven or
// disproven from logs, by making it possible to observe that
// response.output_item.added for a given item genuinely reaches the
// client before that item's first response.output_text.delta, and that
// the two requests in a given session are distinguishable.
//
// It logs ONLY: a generated per-request correlation id, direction
// (upstream|downstream), event type, sequence_number, output_index,
// content_index, item type, item id, item_id, and response status for
// terminal events. It NEVER logs prompt text, output text, function
// arguments, file contents, OAuth tokens, cookies, or encrypted reasoning
// content. It is entirely inert unless HARNESSMESH_SIWC_DEBUG=1 is set (or
// a sink is injected directly, as tests do) - the same opt-in flag the
// token-exchange and request-shape diagnostics already use.

// StreamEventDiagnostic is a fully-redacted record of one SSE event
// crossing the relay boundary.
type StreamEventDiagnostic struct {
	Time           time.Time
	CorrelationID  string
	Direction      string // "upstream" (received from OpenAI) or "downstream" (sent to the client)
	EventType      string
	SequenceNumber *int
	OutputIndex    *int
	ContentIndex   *int
	ItemType       string // the nested item's own "type", when present
	ItemObjectID   string // the nested item's own "id" field, when present
	ItemID         string // the event's top-level "item_id" field, when present
	ResponseStatus string // response.status, populated only for terminal events (response.completed/failed/incomplete)
}

// StreamEventDiagnosticsSink receives StreamEventDiagnostic records.
type StreamEventDiagnosticsSink interface {
	RecordStreamEvent(StreamEventDiagnostic)
}

// stderrStreamEventDiagnosticsLogger is the only production
// StreamEventDiagnosticsSink implementation: one redacted line to stderr
// per event per direction.
type stderrStreamEventDiagnosticsLogger struct{}

func (stderrStreamEventDiagnosticsLogger) RecordStreamEvent(d StreamEventDiagnostic) {
	fmt.Fprintf(os.Stderr,
		"[siwc-stream-diag] %s correlation_id=%s dir=%s event=%q seq=%s output_index=%s content_index=%s item_type=%q item_id=%q item_id_field=%q response_status=%q\n",
		d.Time.Format(time.RFC3339Nano), d.CorrelationID, d.Direction, d.EventType,
		intPtrString(d.SequenceNumber), intPtrString(d.OutputIndex), intPtrString(d.ContentIndex),
		d.ItemType, d.ItemObjectID, d.ItemID, d.ResponseStatus)
}

func intPtrString(v *int) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}

// streamEventDiagnosticsSinkFromEnv returns a StreamEventDiagnosticsSink
// only when explicitly opted into via HARNESSMESH_SIWC_DEBUG=1 - a normal
// request through the provider gateway emits nothing extra by default.
func streamEventDiagnosticsSinkFromEnv() StreamEventDiagnosticsSink {
	if os.Getenv("HARNESSMESH_SIWC_DEBUG") == "1" {
		return stderrStreamEventDiagnosticsLogger{}
	}
	return nil
}

// newStreamCorrelationID generates a short, unique-enough id to
// distinguish concurrent/sequential requests in diagnostic output (e.g.
// two requests arriving in the same second must still be distinguishable)
// - it carries no information about the request's content.
func newStreamCorrelationID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// Extremely unlikely (crypto/rand failure); fall back to a
		// timestamp-derived id rather than failing the request over a
		// diagnostics-only concern.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// streamEventProbe extracts only the safe-to-log fields from one SSE
// event's raw JSON - never anything else (text/arguments/tokens are
// deliberately not fields on this struct, so they can never leak into a
// diagnostic record no matter what the upstream payload contains).
type streamEventProbe struct {
	Type           string `json:"type"`
	SequenceNumber *int   `json:"sequence_number"`
	OutputIndex    *int   `json:"output_index"`
	ContentIndex   *int   `json:"content_index"`
	ItemID         string `json:"item_id"`
	Item           *struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"item"`
	Response *struct {
		Status string `json:"status"`
	} `json:"response"`
}

func (p streamEventProbe) toDiagnostic(correlationID, direction string) StreamEventDiagnostic {
	d := StreamEventDiagnostic{
		Time: time.Now(), CorrelationID: correlationID, Direction: direction,
		EventType: p.Type, SequenceNumber: p.SequenceNumber, OutputIndex: p.OutputIndex, ContentIndex: p.ContentIndex,
		ItemID: p.ItemID,
	}
	if p.Item != nil {
		d.ItemType, d.ItemObjectID = p.Item.Type, p.Item.ID
	}
	if p.Response != nil {
		d.ResponseStatus = p.Response.Status
	}
	return d
}
