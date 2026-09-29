package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
)

// OpenAICompatibleBackend talks to a local/self-hosted server that speaks
// the widely-supported OpenAI Chat Completions wire protocol (POST
// /chat/completions) - vLLM, Ollama's OpenAI-compatibility endpoint,
// LM Studio, llama.cpp's server, etc. This is deliberately Chat
// Completions, not Responses, on the backend side: it is the protocol
// nearly every local inference server actually implements, so this backend
// translates Responses-wire requests from the gateway into Chat
// Completions requests, and translates the streamed Chat Completions
// deltas back into Responses-wire StreamEvents.
//
// This backend never contacts api.openai.com and never requires
// OPENAI_API_KEY: its API key (if any) comes only from the configured
// APIKeyEnv, for the local server's own auth, if it has one.
type OpenAICompatibleBackend struct {
	name    string
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
	headers map[string]string
}

func NewOpenAICompatibleBackend(name string, cfg config.ProviderBackendConfig) *OpenAICompatibleBackend {
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	apiKey := ""
	if cfg.APIKeyEnv != "" {
		apiKey = os.Getenv(cfg.APIKeyEnv)
	}
	return &OpenAICompatibleBackend{
		name:    name,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		model:   cfg.Model,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: timeout},
		headers: cfg.ExtraHeaders,
	}
}

func (b *OpenAICompatibleBackend) Name() string { return b.name }
func (b *OpenAICompatibleBackend) Type() string { return "openai-compatible" }

func (b *OpenAICompatibleBackend) Capabilities() Capabilities {
	return Capabilities{
		Streaming:         true,
		Tools:             true,
		ParallelToolCalls: true,
		MaxContextTokens:  0, // unknown for an arbitrary local server; not advertised
		MaxOutputTokens:   0,
	}
}

func (b *OpenAICompatibleBackend) Health(ctx context.Context) error {
	if b.baseURL == "" {
		return &BackendUnavailableError{Backend: b.name, Reason: "base_url is not configured"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/models", nil)
	if err != nil {
		return &BackendUnavailableError{Backend: b.name, Reason: err.Error()}
	}
	b.applyHeaders(req)
	resp, err := b.client.Do(req)
	if err != nil {
		return &BackendUnavailableError{Backend: b.name, Reason: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return &BackendUnavailableError{Backend: b.name, Reason: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	return nil
}

func (b *OpenAICompatibleBackend) applyHeaders(req *http.Request) {
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range b.headers {
		req.Header.Set(k, v)
	}
}

// --- Chat Completions wire (backend-facing) ---

type chatCompletionsRequest struct {
	Model    string             `json:"model"`
	Messages []chatMessage      `json:"messages"`
	Stream   bool               `json:"stream"`
	Tools    []chatTool         `json:"tools,omitempty"`
	Tool     *chatToolChoiceRaw `json:"tool_choice,omitempty"`
}

type chatToolChoiceRaw = json.RawMessage

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // "function"
	Function chatToolCallFnBody `json:"function"`
}

type chatToolCallFnBody struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string       `json:"type"` // "function"
	Function chatToolSpec `json:"function"`
}

type chatToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatCompletionsChunk struct {
	ID      string `json:"id"`
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// translateRequest converts a Responses-wire Request into a Chat
// Completions request. Only the subset Codex is known to exercise (text
// messages, tool/function_call round trips) is translated; anything else is
// dropped rather than silently mis-rendered.
func translateRequest(model string, req Request) chatCompletionsRequest {
	out := chatCompletionsRequest{Model: model, Stream: true}
	if req.Instructions != "" {
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: req.Instructions})
	}
	pendingToolCalls := map[string]chatToolCall{}
	for _, item := range req.Input {
		switch item.Type {
		case "message":
			var text strings.Builder
			for _, part := range item.Content {
				text.WriteString(part.Text)
			}
			role := item.Role
			if role == "" {
				role = "user"
			}
			if role == "developer" {
				role = "system"
			}
			out.Messages = append(out.Messages, chatMessage{Role: role, Content: text.String()})
		case "function_call":
			pendingToolCalls[item.CallID] = chatToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: chatToolCallFnBody{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			}
			out.Messages = append(out.Messages, chatMessage{Role: "assistant", ToolCalls: []chatToolCall{pendingToolCalls[item.CallID]}})
		case "function_call_output":
			out.Messages = append(out.Messages, chatMessage{Role: "tool", ToolCallID: item.CallID, Content: item.Output})
		}
	}
	for _, t := range req.Tools {
		if t.Type != "function" {
			continue
		}
		out.Tools = append(out.Tools, chatTool{
			Type: "function",
			Function: chatToolSpec{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}

// StreamResponse implements InferenceBackend by POSTing a translated
// request to {base_url}/chat/completions, reading the backend's SSE stream,
// and re-emitting each chunk as Responses-wire StreamEvents in real time -
// never buffering the whole backend response before starting to emit.
func (b *OpenAICompatibleBackend) StreamResponse(ctx context.Context, req Request, sink Sink) error {
	model := req.Model
	if b.model != "" {
		model = b.model
	}
	chatReq := translateRequest(model, req)

	body, err := json.Marshal(chatReq)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	b.applyHeaders(httpReq)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return &BackendUnavailableError{Backend: b.name, Reason: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return &BackendUnavailableError{Backend: b.name, Reason: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &RateLimitedError{}
	}
	if resp.StatusCode >= 400 {
		b2, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &BackendUnavailableError{Backend: b.name, Reason: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(b2))}
	}

	responseID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	now := time.Now().Unix()

	if err := sink.Send(StreamEvent{Type: "response.created", Response: &Response{
		ID: responseID, Object: "response", CreatedAt: now, Status: StatusInProgress, Model: model, Output: []OutputItem{},
	}}); err != nil {
		return err
	}

	msgItemID := fmt.Sprintf("%s_msg_0", responseID)
	msgIndex := 0
	msgContentIndex := 0
	msgOpened := false
	var textBuilder strings.Builder

	type toolCallState struct {
		id, name string
		args     strings.Builder
		index    int
	}
	toolCalls := map[int]*toolCallState{}
	var toolCallOrder []int
	nextOutputIndex := 1

	usage := &Usage{}
	finishReason := ""

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		select {
		case <-sink.Done():
			return &StreamInterruptedError{Reason: "client disconnected"}
		case <-ctx.Done():
			return &StreamInterruptedError{Reason: "request cancelled"}
		default:
		}

		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		if data == "" {
			continue
		}

		var chunk chatCompletionsChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// A malformed event from the backend must not crash the
			// gateway or silently corrupt the stream - surface it as a
			// stream-level error event and stop.
			_ = sink.Send(StreamEvent{Type: "error", Error: &ResponseError{Code: "malformed_backend_event", Message: "backend produced a malformed stream event", Type: "backend_error"}})
			return &StreamInterruptedError{Reason: "malformed backend event"}
		}
		if chunk.Usage != nil {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
			usage.TotalTokens = chunk.Usage.TotalTokens
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != nil {
			finishReason = *choice.FinishReason
		}

		if choice.Delta.Content != "" {
			if !msgOpened {
				oi := msgIndex
				if err := sink.Send(StreamEvent{Type: "response.output_item.added", OutputIndex: &oi, Item: &OutputItem{ID: msgItemID, Type: "message", Status: StatusInProgress, Role: "assistant"}}); err != nil {
					return err
				}
				ci := msgContentIndex
				if err := sink.Send(StreamEvent{Type: "response.content_part.added", ItemID: msgItemID, OutputIndex: &oi, ContentIndex: &ci}); err != nil {
					return err
				}
				msgOpened = true
			}
			textBuilder.WriteString(choice.Delta.Content)
			oi, ci := msgIndex, msgContentIndex
			if err := sink.Send(StreamEvent{Type: "response.output_text.delta", ItemID: msgItemID, OutputIndex: &oi, ContentIndex: &ci, Delta: choice.Delta.Content}); err != nil {
				return err
			}
		}

		for _, tc := range choice.Delta.ToolCalls {
			st, ok := toolCalls[tc.Index]
			if !ok {
				st = &toolCallState{id: tc.ID, name: tc.Function.Name, index: nextOutputIndex}
				nextOutputIndex++
				toolCalls[tc.Index] = st
				toolCallOrder = append(toolCallOrder, tc.Index)
				oi := st.index
				if err := sink.Send(StreamEvent{Type: "response.output_item.added", OutputIndex: &oi, Item: &OutputItem{ID: fmt.Sprintf("%s_call_%d", responseID, st.index), Type: "function_call", Status: StatusInProgress, CallID: st.id, Name: st.name}}); err != nil {
					return err
				}
			}
			if tc.ID != "" {
				st.id = tc.ID
			}
			if tc.Function.Name != "" {
				st.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				st.args.WriteString(tc.Function.Arguments)
				oi := st.index
				itemID := fmt.Sprintf("%s_call_%d", responseID, st.index)
				if err := sink.Send(StreamEvent{Type: "response.function_call_arguments.delta", ItemID: itemID, OutputIndex: &oi, Delta: tc.Function.Arguments}); err != nil {
					return err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return &StreamInterruptedError{Reason: err.Error()}
	}

	output := []OutputItem{}
	if msgOpened {
		oi, ci := msgIndex, msgContentIndex
		finalText := textBuilder.String()
		if err := sink.Send(StreamEvent{Type: "response.output_text.done", ItemID: msgItemID, OutputIndex: &oi, ContentIndex: &ci, Text: finalText}); err != nil {
			return err
		}
		if err := sink.Send(StreamEvent{Type: "response.content_part.done", ItemID: msgItemID, OutputIndex: &oi, ContentIndex: &ci}); err != nil {
			return err
		}
		msgItem := OutputItem{ID: msgItemID, Type: "message", Status: StatusCompleted, Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: finalText}}}
		if err := sink.Send(StreamEvent{Type: "response.output_item.done", OutputIndex: &oi, Item: &msgItem}); err != nil {
			return err
		}
		output = append(output, msgItem)
	}
	for _, idx := range toolCallOrder {
		st := toolCalls[idx]
		itemID := fmt.Sprintf("%s_call_%d", responseID, st.index)
		args := st.args.String()
		oi := st.index
		if err := sink.Send(StreamEvent{Type: "response.function_call_arguments.done", ItemID: itemID, OutputIndex: &oi, Arguments: args}); err != nil {
			return err
		}
		item := OutputItem{ID: itemID, Type: "function_call", Status: StatusCompleted, CallID: st.id, Name: st.name, Arguments: args}
		if err := sink.Send(StreamEvent{Type: "response.output_item.done", OutputIndex: &oi, Item: &item}); err != nil {
			return err
		}
		output = append(output, item)
	}

	status := StatusCompleted
	if finishReason == "length" {
		status = StatusIncomplete
	}
	final := &Response{
		ID: responseID, Object: "response", CreatedAt: now, Status: status, Model: model, Output: output, Usage: usage,
	}
	return sink.Send(StreamEvent{Type: "response.completed", Response: final})
}
