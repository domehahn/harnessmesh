package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"
)

// --- fakeChatServer: a deterministic, in-process stand-in for a local
// OpenAI-compatible inference server (vLLM/Ollama/LM Studio), used to
// exercise OpenAICompatibleBackend's real HTTP+SSE code path without
// requiring an actual LLM in CI (mission section 29). ---

type fakeChatMode string

const (
	modeNormal          fakeChatMode = "normal"
	modeToolCall        fakeChatMode = "tool_call"
	modeTimeout         fakeChatMode = "timeout"
	mode500             fakeChatMode = "500"
	modeMalformedStream fakeChatMode = "malformed_stream"
	modeSlow            fakeChatMode = "slow"
	modePartialThenHang fakeChatMode = "partial_then_hang"
)

type fakeChatServer struct {
	*httptest.Server
	mode      fakeChatMode
	callCount int
}

func newFakeChatServer(mode fakeChatMode) *fakeChatServer {
	f := &fakeChatServer{mode: mode}
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", f.handle)
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	})
	f.Server = httptest.NewServer(mux)
	return f
}

func (f *fakeChatServer) handle(w http.ResponseWriter, r *http.Request) {
	f.callCount++
	flusher, _ := w.(http.Flusher)

	switch f.mode {
	case mode500:
		w.WriteHeader(http.StatusInternalServerError)
		return
	case modeTimeout:
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	writeChunk := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}

	switch f.mode {
	case modeNormal:
		writeChunk(chunkText("Hello"))
		writeChunk(chunkText(", world"))
		writeChunk(chunkFinish("stop"))
		fmt.Fprint(w, "data: [DONE]\n\n")
	case modeToolCall:
		writeChunk(chunkToolCallStart(0, "call_1", "get_weather"))
		writeChunk(chunkToolCallArgs(0, `{"city":`))
		writeChunk(chunkToolCallArgs(0, `"Berlin"}`))
		writeChunk(chunkFinish("tool_calls"))
		fmt.Fprint(w, "data: [DONE]\n\n")
	case modeMalformedStream:
		writeChunk(chunkText("partial"))
		fmt.Fprint(w, "data: {not valid json\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	case modeSlow:
		writeChunk(chunkText("slow-start"))
		time.Sleep(200 * time.Millisecond)
		writeChunk(chunkText("-end"))
		writeChunk(chunkFinish("stop"))
		fmt.Fprint(w, "data: [DONE]\n\n")
	case modePartialThenHang:
		writeChunk(chunkText("partial-output"))
		<-r.Context().Done() // hang until the client disconnects/cancels
	}
}

func chunkText(delta string) map[string]any {
	return map[string]any{
		"id": "chatcmpl_fake",
		"choices": []map[string]any{
			{"delta": map[string]any{"content": delta}},
		},
	}
}

func chunkFinish(reason string) map[string]any {
	return map[string]any{
		"id": "chatcmpl_fake",
		"choices": []map[string]any{
			{"delta": map[string]any{}, "finish_reason": reason},
		},
		"usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
	}
}

func chunkToolCallStart(index int, id, name string) map[string]any {
	return map[string]any{
		"id": "chatcmpl_fake",
		"choices": []map[string]any{
			{"delta": map[string]any{"tool_calls": []map[string]any{
				{"index": index, "id": id, "type": "function", "function": map[string]any{"name": name}},
			}}},
		},
	}
}

func chunkToolCallArgs(index int, argsFragment string) map[string]any {
	return map[string]any{
		"id": "chatcmpl_fake",
		"choices": []map[string]any{
			{"delta": map[string]any{"tool_calls": []map[string]any{
				{"index": index, "function": map[string]any{"arguments": argsFragment}},
			}}},
		},
	}
}

// --- fakeInferenceBackend: a directly-implemented InferenceBackend for
// server-level tests that don't need to exercise real HTTP/SSE parsing. ---

type fakeInferenceBackend struct {
	name         string
	typ          string
	healthErr    error
	streamErr    error
	events       []StreamEvent
	invocations  int
	capabilities Capabilities
}

func (f *fakeInferenceBackend) Name() string             { return f.name }
func (f *fakeInferenceBackend) Type() string              { return f.typ }
func (f *fakeInferenceBackend) Capabilities() Capabilities { return f.capabilities }
func (f *fakeInferenceBackend) Health(ctx context.Context) error {
	return f.healthErr
}
func (f *fakeInferenceBackend) StreamResponse(ctx context.Context, req Request, sink Sink) error {
	f.invocations++
	if f.streamErr != nil {
		return f.streamErr
	}
	for _, ev := range f.events {
		if err := sink.Send(ev); err != nil {
			return err
		}
	}
	return nil
}

func simpleTextEvents(responseID, text string) []StreamEvent {
	oi, ci := 0, 0
	msgID := responseID + "_msg_0"
	return []StreamEvent{
		{Type: "response.created", Response: &Response{ID: responseID, Object: "response", Status: StatusInProgress, Output: []OutputItem{}}},
		{Type: "response.output_item.added", OutputIndex: &oi, Item: &OutputItem{ID: msgID, Type: "message", Status: StatusInProgress, Role: "assistant"}},
		{Type: "response.output_text.delta", ItemID: msgID, OutputIndex: &oi, ContentIndex: &ci, Delta: text},
		{Type: "response.output_text.done", ItemID: msgID, OutputIndex: &oi, ContentIndex: &ci, Text: text},
		{Type: "response.completed", Response: &Response{
			ID: responseID, Object: "response", Status: StatusCompleted,
			Output: []OutputItem{{ID: msgID, Type: "message", Status: StatusCompleted, Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: text}}}},
			Usage:  &Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		}},
	}
}
