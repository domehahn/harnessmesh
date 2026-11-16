package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Span struct {
	TraceID      string            `json:"trace_id,omitempty"`
	SpanID       string            `json:"span_id,omitempty"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	Name         string            `json:"name"`
	StartTime    time.Time         `json:"start_time"`
	EndTime      time.Time         `json:"end_time"`
	Attributes   map[string]string `json:"attributes,omitempty"`
}

// GenAISpanSpec describes an OpenTelemetry GenAI Semantic Convention span
// compatible with Langfuse, Arize Phoenix, Helicone and OTel Collectors.
type GenAISpanSpec struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Operation    string // invoke_agent, execute_tool, chat
	System       string // defaults to "harnessmesh"
	AgentName    string
	Model        string
	ToolName     string
	InputTokens  int64
	OutputTokens int64
	FinishReason string
	StartTime    time.Time
	EndTime      time.Time
}

func NewGenAISpan(spec GenAISpanSpec) Span {
	sys := strings.TrimSpace(spec.System)
	if sys == "" {
		sys = "harnessmesh"
	}
	op := strings.TrimSpace(spec.Operation)
	if op == "" {
		op = "invoke_agent"
	}
	attrs := map[string]string{
		"gen_ai.system":         sys,
		"gen_ai.operation.name": op,
	}
	if spec.AgentName != "" {
		attrs["gen_ai.agent.name"] = spec.AgentName
	}
	if spec.Model != "" {
		attrs["gen_ai.request.model"] = spec.Model
	}
	if spec.ToolName != "" {
		attrs["gen_ai.tool.name"] = spec.ToolName
	}
	if spec.InputTokens > 0 {
		attrs["gen_ai.usage.input_tokens"] = strconv.FormatInt(spec.InputTokens, 10)
	}
	if spec.OutputTokens > 0 {
		attrs["gen_ai.usage.output_tokens"] = strconv.FormatInt(spec.OutputTokens, 10)
	}
	if spec.FinishReason != "" {
		attrs["gen_ai.response.finish_reasons"] = spec.FinishReason
	}
	name := op
	if spec.ToolName != "" {
		name = op + " " + spec.ToolName
	} else if spec.AgentName != "" {
		name = op + " " + spec.AgentName
	}
	return Span{
		TraceID:      spec.TraceID,
		SpanID:       spec.SpanID,
		ParentSpanID: spec.ParentSpanID,
		Name:         name,
		StartTime:    spec.StartTime,
		EndTime:      spec.EndTime,
		Attributes:   attrs,
	}
}

// OTLPHTTPExporter emits a minimal JSON span envelope suitable for an
// OpenTelemetry Collector HTTP gateway configured for HarnessMesh spans.
type OTLPHTTPExporter struct {
	Endpoint string
	Client   *http.Client
}

func (e *OTLPHTTPExporter) Export(ctx context.Context, spans []Span) error {
	if e == nil || strings.TrimSpace(e.Endpoint) == "" {
		return fmt.Errorf("OTLP endpoint is empty")
	}
	body, err := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{"scopeSpans": []any{map[string]any{"spans": spans}}}}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := e.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OTLP exporter returned HTTP %d", resp.StatusCode)
	}
	return nil
}
