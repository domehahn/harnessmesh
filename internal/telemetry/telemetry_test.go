package telemetry

import (
	"strings"
	"testing"
	"time"
)

func TestPrometheusRegistry(t *testing.T) {
	r := NewRegistry()
	r.Counter("harnessmesh_test_total").Add(2)
	r.Observe("harnessmesh_test_latency", 5*time.Millisecond)
	out := r.Prometheus()
	if !strings.Contains(out, "harnessmesh_test_total 2") || !strings.Contains(out, "harnessmesh_test_latency_count 1") {
		t.Fatalf("unexpected metrics: %s", out)
	}
}

func TestNewGenAISpanSemanticConventions(t *testing.T) {
	now := time.Now().UTC()
	span := NewGenAISpan(GenAISpanSpec{
		TraceID:      "trace-1",
		SpanID:       "span-2",
		ParentSpanID: "span-1",
		Operation:    "execute_tool",
		AgentName:    "codex",
		Model:        "gpt-5-codex",
		ToolName:     "knowledge.cognify",
		InputTokens:  320,
		OutputTokens: 140,
		FinishReason: "stop",
		StartTime:    now,
		EndTime:      now.Add(25 * time.Millisecond),
	})
	if span.TraceID != "trace-1" || span.ParentSpanID != "span-1" || span.Name != "execute_tool knowledge.cognify" {
		t.Fatalf("unexpected span hierarchy: %+v", span)
	}
	if span.Attributes["gen_ai.system"] != "harnessmesh" ||
		span.Attributes["gen_ai.agent.name"] != "codex" ||
		span.Attributes["gen_ai.tool.name"] != "knowledge.cognify" ||
		span.Attributes["gen_ai.usage.input_tokens"] != "320" {
		t.Fatalf("unexpected GenAI attributes: %+v", span.Attributes)
	}
}
