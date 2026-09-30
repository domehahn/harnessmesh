package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This file audits sseSink's sequence_number handling, per a real-account
// observation: a genuine OpenAI stream's first two events both showed
// sequence_number=1 (response.created and response.in_progress), which
// cannot be correct for a strictly monotonic/unique per-stream counter.
//
// Root cause: sseSink.Send used `if ev.SequenceNumber == 0` as its
// "not yet assigned" sentinel on a plain `int` field. The documented
// streaming contract's first event genuinely has sequence_number=0, so a
// relayed real upstream event carrying that correct value was
// indistinguishable from "unset" and got silently overwritten by a freshly
// generated value (which happened to be 1, colliding with the next
// event's own genuine value of 1). SequenceNumber is now a *int: nil
// unambiguously means "needs one synthesized locally"; a non-nil pointer -
// including one pointing at 0 - is always left untouched.

func parseSSEDataLines(t *testing.T, body []byte) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev StreamEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("parse SSE data line %q: %v", line, err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE body: %v", err)
	}
	return events
}

func newTestSSESink(t *testing.T) (*sseSink, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	sink, err := newSSESink(rec, req)
	if err != nil {
		t.Fatalf("newSSESink: %v", err)
	}
	return sink, rec
}

// Locally-synthesized events (no upstream-assigned sequence_number) get a
// monotonic counter starting at 0, matching the documented convention.
func TestSSESink_LocallySynthesizedSequenceNumbers_StartAtZeroAndIncrement(t *testing.T) {
	sink, rec := newTestSSESink(t)
	for i := 0; i < 4; i++ {
		if err := sink.Send(StreamEvent{Type: "response.output_text.delta", Delta: "x"}); err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
	}
	events := parseSSEDataLines(t, rec.Body.Bytes())
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}
	for i, ev := range events {
		if ev.SequenceNumber == nil {
			t.Fatalf("event %d: expected a synthesized sequence_number, got nil", i)
		}
		if *ev.SequenceNumber != i {
			t.Fatalf("event %d: expected sequence_number=%d, got %d", i, i, *ev.SequenceNumber)
		}
	}
}

// A relayed real upstream event's own sequence_number - including a
// genuine 0 for the first event - must be preserved exactly, never
// reassigned. This is the regression test for the exact reported evidence:
// response.created and response.in_progress must NOT both end up as 1.
func TestSSESink_RelayedUpstreamSequenceNumbers_PreservedUnchanged(t *testing.T) {
	sink, rec := newTestSSESink(t)
	zero, one := 0, 1
	if err := sink.Send(StreamEvent{Type: "response.created", SequenceNumber: &zero}); err != nil {
		t.Fatalf("Send response.created: %v", err)
	}
	if err := sink.Send(StreamEvent{Type: "response.in_progress", SequenceNumber: &one}); err != nil {
		t.Fatalf("Send response.in_progress: %v", err)
	}

	events := parseSSEDataLines(t, rec.Body.Bytes())
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].SequenceNumber == nil || *events[0].SequenceNumber != 0 {
		t.Fatalf("expected response.created to keep its real upstream sequence_number=0, got %v", events[0].SequenceNumber)
	}
	if events[1].SequenceNumber == nil || *events[1].SequenceNumber != 1 {
		t.Fatalf("expected response.in_progress to keep its real upstream sequence_number=1, got %v", events[1].SequenceNumber)
	}
	if *events[0].SequenceNumber == *events[1].SequenceNumber {
		t.Fatalf("sequence numbers must be unique per stream - got a collision at %d", *events[0].SequenceNumber)
	}
}

// TestSIWCRelay_PreservesRealUpstreamSequenceNumbers drives the actual
// SubscriptionBackend.StreamResponse -> relaySIWCStream path (not just
// sseSink in isolation) against a fake upstream emitting literal
// sequence_number fields matching the exact real-account evidence shape
// (response.created=0, response.in_progress=1), proving the backend-level
// relay never rewrites them.
func TestSIWCRelay_PreservesRealUpstreamSequenceNumbers(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"sequence_number\":1,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		flusher.Flush()
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	tokenPath := t.TempDir() + "/auth.json"
	if err := saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: upstream.URL, responsesURL: upstream.URL, httpClient: http.DefaultClient}

	sink := newCollectingSink()
	if err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: NewTextContent("hi")}}}, sink); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	if len(sink.events) < 3 {
		t.Fatalf("expected at least 3 relayed events, got %d", len(sink.events))
	}
	if sink.events[0].SequenceNumber == nil || *sink.events[0].SequenceNumber != 0 {
		t.Fatalf("expected the relayed response.created to keep sequence_number=0, got %v", sink.events[0].SequenceNumber)
	}
	if sink.events[1].SequenceNumber == nil || *sink.events[1].SequenceNumber != 1 {
		t.Fatalf("expected the relayed response.in_progress to keep sequence_number=1, got %v", sink.events[1].SequenceNumber)
	}
	if *sink.events[0].SequenceNumber == *sink.events[1].SequenceNumber {
		t.Fatalf("relayed sequence numbers collided at %d - this is the exact real-account defect", *sink.events[0].SequenceNumber)
	}
}
