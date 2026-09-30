package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
)

// StreamEvent is one Responses-API SSE event. Type is the SSE "event:"
// name (e.g. "response.output_text.delta"); the remaining fields are
// marshaled as the "data:" JSON payload alongside a monotonic
// sequence_number, matching the documented streaming-events contract.
type StreamEvent struct {
	Type string `json:"type"`

	// SequenceNumber is a pointer so a real, upstream-assigned value of 0
	// (the documented first event's sequence number) can never be mistaken
	// for "not yet assigned" - a plain int field with that meaning caused a
	// real defect: relaying a genuine OpenAI stream (whose events already
	// carry correct, 0-indexed sequence numbers) through sseSink.Send's old
	// `if ev.SequenceNumber == 0` heuristic silently overwrote the true
	// first event's sequence_number with a freshly-generated one,
	// producing a collision with the next event's legitimate value (both
	// observed as sequence_number=1 against a real account). Only a
	// genuinely nil pointer means this event needs one synthesized locally
	// - see sseSink.Send.
	SequenceNumber *int `json:"sequence_number,omitempty"`

	Response *Response `json:"response,omitempty"`

	OutputIndex  *int        `json:"output_index,omitempty"`
	ContentIndex *int        `json:"content_index,omitempty"`
	ItemID       string      `json:"item_id,omitempty"`
	Item         *OutputItem `json:"item,omitempty"`
	Delta        string      `json:"delta,omitempty"`
	Text         string      `json:"text,omitempty"`
	Arguments    string      `json:"arguments,omitempty"`

	Error *ResponseError `json:"error,omitempty"`
}

// Sink receives StreamEvents from a backend and is responsible for
// delivering them to the HTTP client (as SSE) or to a test harness. A sink
// is used by exactly one in-flight request.
type Sink interface {
	Send(ev StreamEvent) error
	// Done reports whether the client has gone away (e.g. disconnected or
	// cancelled), so a backend can stop producing further events promptly
	// instead of streaming into the void.
	Done() <-chan struct{}
}

// sseSink writes StreamEvents as Server-Sent Events to an http.ResponseWriter,
// flushing after every event so partial output reaches the client
// immediately rather than being buffered - streaming must never be faked by
// collecting the full result first.
type sseSink struct {
	w       http.ResponseWriter
	flusher http.Flusher
	done    <-chan struct{}
	seq     atomic.Int64
}

func newSSESink(w http.ResponseWriter, r *http.Request) (*sseSink, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming unsupported by response writer")
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &sseSink{w: w, flusher: flusher, done: r.Context().Done()}, nil
}

func (s *sseSink) Send(ev StreamEvent) error {
	if ev.SequenceNumber == nil {
		// Documented streaming contract: sequence_number starts at 0 for a
		// stream's first event. Only events this sink itself synthesizes
		// (locally-generated backends that never assign one) reach this
		// branch; a relayed real upstream event already carries its own
		// genuine sequence_number and is left untouched above.
		n := int(s.seq.Add(1)) - 1
		ev.SequenceNumber = &n
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", ev.Type, body); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *sseSink) Done() <-chan struct{} {
	return s.done
}

// bufferingSink accumulates events for a non-streaming request: the server
// still drives the backend through its normal streaming interface (backends
// implement exactly one code path), but the HTTP client receives a single
// JSON Response body once the final event arrives.
type bufferingSink struct {
	events chan StreamEvent
	done   chan struct{}
}

func newBufferingSink() *bufferingSink {
	return &bufferingSink{events: make(chan StreamEvent, 64), done: make(chan struct{})}
}

func (b *bufferingSink) Send(ev StreamEvent) error {
	select {
	case b.events <- ev:
		return nil
	case <-b.done:
		return fmt.Errorf("sink closed")
	}
}

func (b *bufferingSink) Done() <-chan struct{} { return b.done }

// Close must be called exactly once, after the producing goroutine's call
// into StreamResponse has returned (so no further Send call can race a
// close of the events channel), to unblock a `range sink.events` reader.
func (b *bufferingSink) Close() {
	close(b.done)
	close(b.events)
}
