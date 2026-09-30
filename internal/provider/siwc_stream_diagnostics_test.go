package provider

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// capturingStreamEventDiagnosticsSink records every StreamEventDiagnostic
// it receives, in order.
type capturingStreamEventDiagnosticsSink struct {
	records []StreamEventDiagnostic
}

func (s *capturingStreamEventDiagnosticsSink) RecordStreamEvent(d StreamEventDiagnostic) {
	s.records = append(s.records, d)
}

// TestStreamEventDiagnostics_UpstreamBeforeDownstream_SameItem proves the
// required ordering property from Mission 3: an upstream
// response.output_item.added record for a given item is observed before
// the first output_text.delta record for that same item, and that the
// upstream record for one event always precedes its own downstream
// record.
func TestStreamEventDiagnostics_UpstreamBeforeDownstream_SameItem(t *testing.T) {
	upstream := newRawSSEUpstream(t, realisticTextStreamFixture)

	tokenPath := t.TempDir() + "/auth.json"
	if err := saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: upstream.URL, responsesURL: upstream.URL, httpClient: http.DefaultClient}
	sink := &capturingStreamEventDiagnosticsSink{}
	b.streamDiagnostics = sink

	collecting := newCollectingSink()
	if err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: NewTextContent("hi")}}}, collecting); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	if len(sink.records) == 0 {
		t.Fatalf("expected diagnostic records to be captured")
	}

	// Every record must share the same, non-empty correlation id within
	// this one request.
	correlationID := sink.records[0].CorrelationID
	if correlationID == "" {
		t.Fatalf("expected a non-empty correlation id")
	}
	for i, r := range sink.records {
		if r.CorrelationID != correlationID {
			t.Fatalf("record %d: expected correlation_id=%q, got %q", i, correlationID, r.CorrelationID)
		}
	}

	// Find the output_item.added record's position and the first
	// output_text.delta record's position; upstream(added) must come
	// before downstream(added), and downstream(added) must come before
	// upstream(first delta) - i.e. the item is fully announced before any
	// delta for it is observed.
	var addedUpstreamIdx, addedDownstreamIdx, firstDeltaUpstreamIdx = -1, -1, -1
	for i, r := range sink.records {
		if r.EventType == "response.output_item.added" && r.Direction == "upstream" && addedUpstreamIdx == -1 {
			addedUpstreamIdx = i
		}
		if r.EventType == "response.output_item.added" && r.Direction == "downstream" && addedDownstreamIdx == -1 {
			addedDownstreamIdx = i
		}
		if r.EventType == "response.output_text.delta" && r.Direction == "upstream" && firstDeltaUpstreamIdx == -1 {
			firstDeltaUpstreamIdx = i
		}
	}
	if addedUpstreamIdx == -1 || addedDownstreamIdx == -1 || firstDeltaUpstreamIdx == -1 {
		t.Fatalf("expected to observe output_item.added (upstream+downstream) and a delta, got %d records", len(sink.records))
	}
	if !(addedUpstreamIdx < addedDownstreamIdx && addedDownstreamIdx < firstDeltaUpstreamIdx) {
		t.Fatalf("expected order upstream(added) < downstream(added) < upstream(first delta), got indices %d, %d, %d",
			addedUpstreamIdx, addedDownstreamIdx, firstDeltaUpstreamIdx)
	}

	// The item id referenced by output_item.added must match the item_id
	// referenced by the delta - proving they're the same item.
	if sink.records[addedUpstreamIdx].ItemObjectID != "msg_1" {
		t.Fatalf("expected output_item.added item id=msg_1, got %q", sink.records[addedUpstreamIdx].ItemObjectID)
	}
	if sink.records[firstDeltaUpstreamIdx].ItemID != "msg_1" {
		t.Fatalf("expected delta item_id=msg_1 matching the added item, got %q", sink.records[firstDeltaUpstreamIdx].ItemID)
	}
}

// TestStreamEventDiagnostics_TwoRequests_DistinctCorrelationIDs proves two
// separate requests (e.g. two turns arriving close together) get
// independent correlation ids.
func TestStreamEventDiagnostics_TwoRequests_DistinctCorrelationIDs(t *testing.T) {
	upstream := newRawSSEUpstream(t, realisticTextStreamFixture)

	tokenPath := t.TempDir() + "/auth.json"
	if err := saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: upstream.URL, responsesURL: upstream.URL, httpClient: http.DefaultClient}
	sink := &capturingStreamEventDiagnosticsSink{}
	b.streamDiagnostics = sink

	for i := 0; i < 2; i++ {
		collecting := newCollectingSink()
		if err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: NewTextContent("hi")}}}, collecting); err != nil {
			t.Fatalf("StreamResponse #%d: %v", i, err)
		}
	}

	seen := map[string]bool{}
	for _, r := range sink.records {
		seen[r.CorrelationID] = true
	}
	if len(seen) != 2 {
		t.Fatalf("expected exactly 2 distinct correlation ids across 2 requests, got %d: %v", len(seen), seen)
	}
}

// TestStreamEventDiagnostics_NeverLogsSensitiveValues proves the
// diagnostic record never carries prompt/output text, function arguments,
// or tokens - only the safe fields.
func TestStreamEventDiagnostics_NeverLogsSensitiveValues(t *testing.T) {
	const secretDelta = "SECRET_OUTPUT_TEXT_MUST_NEVER_APPEAR"
	sseBody := `event: response.output_text.delta
data: {"type":"response.output_text.delta","sequence_number":0,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"` + secretDelta + `"}

event: response.completed
data: {"type":"response.completed","sequence_number":1,"response":{"id":"r1","object":"response","status":"completed","output":[]}}

`
	upstream := newRawSSEUpstream(t, sseBody)

	tokenPath := t.TempDir() + "/auth.json"
	if err := saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_test", AccessToken: "secret-token-value", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("saveSIWCTokenSet: %v", err)
	}
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: upstream.URL, responsesURL: upstream.URL, httpClient: http.DefaultClient}
	sink := &capturingStreamEventDiagnosticsSink{}
	b.streamDiagnostics = sink

	collecting := newCollectingSink()
	if err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: NewTextContent("hi")}}}, collecting); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	for _, r := range sink.records {
		if r.EventType == secretDelta || r.ItemType == secretDelta || r.ItemObjectID == secretDelta || r.ItemID == secretDelta || r.ResponseStatus == secretDelta {
			t.Fatalf("diagnostic record leaked the secret delta text: %+v", r)
		}
		if r.CorrelationID == "secret-token-value" {
			t.Fatalf("diagnostic record leaked the access token")
		}
	}
}
