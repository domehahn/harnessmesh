package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
)

type collectingSink struct {
	events []StreamEvent
	done   chan struct{}
}

func newCollectingSink() *collectingSink { return &collectingSink{done: make(chan struct{})} }

func (c *collectingSink) Send(ev StreamEvent) error {
	c.events = append(c.events, ev)
	return nil
}
func (c *collectingSink) Done() <-chan struct{} { return c.done }

func newTestBackend(t *testing.T, srv *fakeChatServer) *OpenAICompatibleBackend {
	t.Helper()
	return NewOpenAICompatibleBackend("local", config.ProviderBackendConfig{
		Type: "openai-compatible", BaseURL: srv.URL, TimeoutSec: 5,
	})
}

func TestOpenAICompatibleBackend_NormalStream(t *testing.T) {
	srv := newFakeChatServer(modeNormal)
	defer srv.Close()
	b := newTestBackend(t, srv)

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "test", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink)
	if err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	var deltas []string
	var sawCreated, sawCompleted bool
	var finalText string
	for _, ev := range sink.events {
		switch ev.Type {
		case "response.created":
			sawCreated = true
		case "response.output_text.delta":
			deltas = append(deltas, ev.Delta)
		case "response.completed":
			sawCompleted = true
			if len(ev.Response.Output) > 0 {
				finalText = ev.Response.Output[0].Content[0].Text
			}
		}
	}
	if !sawCreated || !sawCompleted {
		t.Fatalf("expected response.created and response.completed events, got: %+v", sink.events)
	}
	if strings.Join(deltas, "") != "Hello, world" {
		t.Fatalf("expected deltas to join to 'Hello, world', got %q", strings.Join(deltas, ""))
	}
	if finalText != "Hello, world" {
		t.Fatalf("expected final output text 'Hello, world', got %q", finalText)
	}
}

func TestOpenAICompatibleBackend_ToolCallStream(t *testing.T) {
	srv := newFakeChatServer(modeToolCall)
	defer srv.Close()
	b := newTestBackend(t, srv)

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "test", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "weather?"}}}}, Tools: []Tool{{Type: "function", Name: "get_weather"}}}, sink)
	if err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	var argDeltas []string
	var sawFunctionCallDone bool
	var finalCall *OutputItem
	for _, ev := range sink.events {
		switch ev.Type {
		case "response.function_call_arguments.delta":
			argDeltas = append(argDeltas, ev.Delta)
		case "response.function_call_arguments.done":
			sawFunctionCallDone = true
		case "response.completed":
			for i := range ev.Response.Output {
				if ev.Response.Output[i].Type == "function_call" {
					finalCall = &ev.Response.Output[i]
				}
			}
		}
	}
	if !sawFunctionCallDone {
		t.Fatalf("expected response.function_call_arguments.done event")
	}
	args := strings.Join(argDeltas, "")
	if args != `{"city":"Berlin"}` {
		t.Fatalf("expected tool call arguments to join to the full JSON, got %q", args)
	}
	if finalCall == nil || finalCall.Name != "get_weather" || finalCall.Arguments != args {
		t.Fatalf("expected final function_call output item with matching arguments, got %+v", finalCall)
	}
}

func TestOpenAICompatibleBackend_PartialOutputThenCancellation(t *testing.T) {
	srv := newFakeChatServer(modePartialThenHang)
	defer srv.Close()
	b := newTestBackend(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	sink := newCollectingSink()
	err := b.StreamResponse(ctx, Request{Model: "test", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "go"}}}}}, sink)
	if err == nil {
		t.Fatalf("expected an error when the request is cancelled mid-stream")
	}
	if _, ok := err.(*StreamInterruptedError); !ok {
		t.Fatalf("expected StreamInterruptedError, got %T: %v", err, err)
	}
	foundPartial := false
	for _, ev := range sink.events {
		if ev.Type == "response.output_text.delta" && ev.Delta == "partial-output" {
			foundPartial = true
		}
	}
	if !foundPartial {
		t.Fatalf("expected the partial output emitted before cancellation to have reached the sink")
	}
}

func TestOpenAICompatibleBackend_ClientDisconnect(t *testing.T) {
	srv := newFakeChatServer(modePartialThenHang)
	defer srv.Close()
	b := newTestBackend(t, srv)

	sink := newCollectingSink()
	close(sink.done) // simulate an already-disconnected client

	err := b.StreamResponse(context.Background(), Request{Model: "test", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "go"}}}}}, sink)
	if err == nil {
		t.Fatalf("expected an error when the sink reports the client is gone")
	}
}

func TestOpenAICompatibleBackend_MalformedStreamEvent(t *testing.T) {
	srv := newFakeChatServer(modeMalformedStream)
	defer srv.Close()
	b := newTestBackend(t, srv)

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "test", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "go"}}}}}, sink)
	if err == nil {
		t.Fatalf("expected an error for a malformed backend stream event")
	}
	if _, ok := err.(*StreamInterruptedError); !ok {
		t.Fatalf("expected StreamInterruptedError, got %T: %v", err, err)
	}
	foundErrorEvent := false
	for _, ev := range sink.events {
		if ev.Type == "error" {
			foundErrorEvent = true
		}
	}
	if !foundErrorEvent {
		t.Fatalf("expected an 'error' stream event to be sent before returning")
	}
}

func TestOpenAICompatibleBackend_Backend500(t *testing.T) {
	srv := newFakeChatServer(mode500)
	defer srv.Close()
	b := newTestBackend(t, srv)

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "test", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "go"}}}}}, sink)
	if err == nil {
		t.Fatalf("expected an error for backend HTTP 500")
	}
	if _, ok := err.(*BackendUnavailableError); !ok {
		t.Fatalf("expected BackendUnavailableError, got %T: %v", err, err)
	}
}

func TestOpenAICompatibleBackend_Timeout(t *testing.T) {
	srv := newFakeChatServer(modeTimeout)
	defer srv.Close()
	b := NewOpenAICompatibleBackend("local", config.ProviderBackendConfig{Type: "openai-compatible", BaseURL: srv.URL, TimeoutSec: 0})
	b.client.Timeout = 100 * time.Millisecond

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "test", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "go"}}}}}, sink)
	if err == nil {
		t.Fatalf("expected a timeout error")
	}
}

func TestOpenAICompatibleBackend_SlowStreamStillDeliversIncrementally(t *testing.T) {
	srv := newFakeChatServer(modeSlow)
	defer srv.Close()
	b := newTestBackend(t, srv)

	sink := newCollectingSink()
	start := time.Now()
	err := b.StreamResponse(context.Background(), Request{Model: "test", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "go"}}}}}, sink)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("expected the artificial 200ms mid-stream delay to be reflected in wall time, got %v (streaming must not be faked by buffering)", elapsed)
	}
}
