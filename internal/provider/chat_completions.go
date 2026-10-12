package provider

// This file is a deliberately narrow legacy adapter. Responses API is the
// canonical provider contract; this endpoint exists for older clients and
// translates ordinary non-streaming Chat Completions requests into the same
// authenticated, policy-checked Responses pipeline.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type legacyChatCompletionsRequest struct {
	Model            string              `json:"model"`
	Messages         []legacyChatMessage `json:"messages"`
	Stream           bool                `json:"stream,omitempty"`
	Tools            []legacyChatTool    `json:"tools,omitempty"`
	ToolChoice       json.RawMessage     `json:"tool_choice,omitempty"`
	Temperature      *float64            `json:"temperature,omitempty"`
	MaxTokens        *int                `json:"max_tokens,omitempty"`
	ParallelToolCall *bool               `json:"parallel_tool_calls,omitempty"`
}

type legacyChatMessage struct {
	Role       string               `json:"role"`
	Content    json.RawMessage      `json:"content"`
	Name       string               `json:"name,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	ToolCalls  []legacyChatToolCall `json:"tool_calls,omitempty"`
}

type legacyChatTool struct {
	Type     string               `json:"type"`
	Function legacyChatToolSchema `json:"function,omitempty"`
}

type legacyChatToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type legacyChatToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function legacyChatFunction `json:"function"`
}

type legacyChatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCompletionsResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   *Usage                 `json:"usage,omitempty"`
}

type chatCompletionChoice struct {
	Index        int           `json:"index"`
	Message      chatAssistant `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type chatAssistant struct {
	Role      string               `json:"role"`
	Content   *string              `json:"content"`
	ToolCalls []legacyChatToolCall `json:"tool_calls,omitempty"`
}

// handleChatCompletions adapts the widely deployed legacy route. Streaming is
// rejected explicitly instead of pretending to support it: callers needing
// semantic streaming must use /v1/responses, which is HarnessMesh's stable
// contract and the route used by Codex.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeProviderError(w, http.StatusMethodNotAllowed, &ForbiddenError{Reason: "only POST is supported"})
		return
	}
	var chatReq legacyChatCompletionsRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&chatReq); err != nil {
		writeProviderError(w, http.StatusBadRequest, fmt.Errorf("invalid chat completions request: %w", err))
		return
	}
	if chatReq.Stream {
		writeProviderError(w, http.StatusNotImplemented, fmt.Errorf("streaming chat completions is not supported; use POST /v1/responses with stream=true"))
		return
	}
	responsesReq, err := chatToResponsesRequest(chatReq)
	if err != nil {
		writeProviderError(w, http.StatusBadRequest, err)
		return
	}
	body, err := json.Marshal(responsesReq)
	if err != nil {
		writeProviderError(w, http.StatusInternalServerError, err)
		return
	}
	clone := r.Clone(r.Context())
	clone.URL.Path = "/v1/responses"
	clone.Body = io.NopCloser(bytes.NewReader(body))
	capture := &captureResponseWriter{header: make(http.Header)}
	s.handleResponses(capture, clone)
	if capture.status < http.StatusOK || capture.status >= http.StatusMultipleChoices {
		for k, values := range capture.header {
			for _, value := range values {
				w.Header().Add(k, value)
			}
		}
		w.WriteHeader(capture.status)
		_, _ = w.Write(capture.body.Bytes())
		return
	}
	var response Response
	if err := json.Unmarshal(capture.body.Bytes(), &response); err != nil {
		writeProviderError(w, http.StatusBadGateway, fmt.Errorf("responses backend returned invalid response: %w", err))
		return
	}
	for k, values := range capture.header {
		for _, value := range values {
			w.Header().Add(k, value)
		}
	}
	w.Header().Set("X-Request-Id", capture.header.Get("X-Request-Id"))
	writeJSON(w, http.StatusOK, responsesToChatCompletion(response))
}

type captureResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *captureResponseWriter) Header() http.Header { return w.header }
func (w *captureResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *captureResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func chatToResponsesRequest(in legacyChatCompletionsRequest) (Request, error) {
	if strings.TrimSpace(in.Model) == "" {
		return Request{}, fmt.Errorf("model is required")
	}
	if len(in.Messages) == 0 {
		return Request{}, fmt.Errorf("messages must contain at least one message")
	}
	out := Request{Model: in.Model, ToolChoice: in.ToolChoice, Temperature: in.Temperature, MaxOutputTokens: in.MaxTokens, ParallelToolCalls: in.ParallelToolCall}
	for _, msg := range in.Messages {
		if strings.TrimSpace(msg.Role) == "" {
			return Request{}, fmt.Errorf("message role is required")
		}
		if msg.Role == "tool" {
			text, err := chatContentText(msg.Content)
			if err != nil {
				return Request{}, err
			}
			out.Input = append(out.Input, InputItem{Type: "function_call_output", CallID: msg.ToolCallID, Output: NewTextCallOutput(text), Status: StatusCompleted})
			continue
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			if text, err := chatContentText(msg.Content); err != nil {
				return Request{}, err
			} else if text != "" {
				out.Input = append(out.Input, InputItem{Type: "message", Role: msg.Role, Content: NewTextContent(text)})
			}
			for _, call := range msg.ToolCalls {
				out.Input = append(out.Input, InputItem{Type: "function_call", ID: call.ID, CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments, Status: StatusCompleted})
			}
			continue
		}
		text, err := chatContentText(msg.Content)
		if err != nil {
			return Request{}, err
		}
		out.Input = append(out.Input, InputItem{Type: "message", Role: msg.Role, Content: NewTextContent(text)})
	}
	for _, tool := range in.Tools {
		if tool.Type != "function" {
			return Request{}, fmt.Errorf("unsupported chat tool type %q; use Responses API for provider-specific tools", tool.Type)
		}
		out.Tools = append(out.Tools, Tool{Type: "function", Name: tool.Function.Name, Description: tool.Function.Description, Parameters: tool.Function.Parameters, Strict: tool.Function.Strict})
	}
	return out, nil
}

func chatContentText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("message content must be a string or text-part array: %w", err)
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Type != "text" && part.Type != "input_text" {
			return "", fmt.Errorf("unsupported message content part %q in legacy adapter", part.Type)
		}
		b.WriteString(part.Text)
	}
	return b.String(), nil
}

func responsesToChatCompletion(response Response) chatCompletionsResponse {
	assistant := chatAssistant{Role: "assistant"}
	var text strings.Builder
	finish := "stop"
	for _, item := range response.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		case "function_call":
			finish = "tool_calls"
			assistant.ToolCalls = append(assistant.ToolCalls, legacyChatToolCall{ID: item.CallID, Type: "function", Function: legacyChatFunction{Name: item.Name, Arguments: item.Arguments}})
		}
	}
	content := text.String()
	if len(assistant.ToolCalls) > 0 {
		assistant.Content = nil
	} else {
		assistant.Content = &content
	}
	return chatCompletionsResponse{ID: response.ID, Object: "chat.completion", Created: response.CreatedAt, Model: response.Model, Choices: []chatCompletionChoice{{Index: 0, Message: assistant, FinishReason: finish}}, Usage: response.Usage}
}
