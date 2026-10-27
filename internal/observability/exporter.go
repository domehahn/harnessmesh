package observability

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Log is the redacted control-plane log projection exported to an external
// collector. It intentionally contains no raw prompt, tool argument, or
// credential fields.
type Log struct {
	ID        string
	Source    string
	Level     string
	Message   string
	EventType string
	Timestamp time.Time
	TraceID   string
	SpanID    string
}

type Span struct {
	TraceID    string
	SpanID     string
	ParentID   string
	Service    string
	Operation  string
	Start      time.Time
	Duration   int64
	Status     string
	Attributes map[string]string
}

type Exporter struct {
	LogsURL   string
	TracesURL string
	Token     string
	Client    *http.Client
}

// GenAIAttributes and MCPAttributes keep emitted telemetry aligned with the
// OpenTelemetry GenAI/MCP semantic namespaces while allowing callers to add
// provider-specific fields explicitly.
func GenAIAttributes(model, operation string, inputTokens, outputTokens int) map[string]string {
	return map[string]string{"gen_ai.request.model": model, "gen_ai.operation.name": operation, "gen_ai.usage.input_tokens": fmt.Sprintf("%d", inputTokens), "gen_ai.usage.output_tokens": fmt.Sprintf("%d", outputTokens)}
}
func MCPAttributes(server, tool string) map[string]string {
	return map[string]string{"mcp.server.name": server, "mcp.tool.name": tool}
}

func FromEnvironment() *Exporter {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("HARNESSMESH_OTEL_EXPORT_URL")), "/")
	logsURL := strings.TrimSpace(os.Getenv("HARNESSMESH_OTEL_LOGS_URL"))
	tracesURL := strings.TrimSpace(os.Getenv("HARNESSMESH_OTEL_TRACES_URL"))
	if base != "" {
		if logsURL == "" {
			logsURL = base + "/v1/logs"
		}
		if tracesURL == "" {
			tracesURL = base + "/v1/traces"
		}
	}
	if logsURL == "" && tracesURL == "" {
		return nil
	}
	return &Exporter{LogsURL: logsURL, TracesURL: tracesURL, Token: strings.TrimSpace(os.Getenv("HARNESSMESH_OTEL_EXPORT_TOKEN")), Client: &http.Client{Timeout: 10 * time.Second}}
}

func validID(value string, size int) string {
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == size {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	if size == 8 {
		return hex.EncodeToString(digest[:8])
	}
	return hex.EncodeToString(digest[:])
}

func unixNano(t time.Time) string {
	return fmt.Sprintf("%d", t.UnixNano())
}

func attributes(values map[string]string) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for key, value := range values {
		out = append(out, map[string]any{"key": key, "value": map[string]string{"stringValue": value}})
	}
	return out
}

func (e *Exporter) post(ctx context.Context, endpoint string, payload any) error {
	if strings.TrimSpace(endpoint) == "" {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.Token != "" {
		req.Header.Set("Authorization", "Bearer "+e.Token)
	}
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
		return fmt.Errorf("observability exporter returned %s", resp.Status)
	}
	return nil
}

func (e *Exporter) Export(ctx context.Context, tenant string, logs []Log, spans []Span) error {
	if e == nil {
		return errors.New("observability exporter is not configured")
	}
	resource := map[string]any{"attributes": attributes(map[string]string{"service.name": "harnessmesh", "harnessmesh.tenant": tenant})}
	if len(logs) > 0 {
		records := make([]map[string]any, 0, len(logs))
		for _, log := range logs {
			records = append(records, map[string]any{"timeUnixNano": unixNano(log.Timestamp), "severityText": strings.ToUpper(log.Level), "body": map[string]string{"stringValue": log.Message}, "attributes": attributes(map[string]string{"source": log.Source, "event.type": log.EventType, "trace_id": validID(log.TraceID, 16), "span_id": validID(log.SpanID, 8)})})
		}
		if err := e.post(ctx, e.LogsURL, map[string]any{"resourceLogs": []any{map[string]any{"resource": resource, "scopeLogs": []any{map[string]any{"logRecords": records}}}}}); err != nil {
			return err
		}
	}
	if len(spans) > 0 {
		exported := make([]map[string]any, 0, len(spans))
		for _, span := range spans {
			start := span.Start
			end := start.Add(time.Duration(span.Duration) * time.Millisecond)
			spanAttrs := map[string]string{"service.name": span.Service, "harnessmesh.status": span.Status}
			for key, value := range span.Attributes {
				spanAttrs[key] = value
			}
			item := map[string]any{"traceId": validID(span.TraceID, 16), "spanId": validID(span.SpanID, 8), "name": span.Operation, "startTimeUnixNano": unixNano(start), "endTimeUnixNano": unixNano(end), "attributes": attributes(spanAttrs)}
			if span.ParentID != "" {
				item["parentSpanId"] = validID(span.ParentID, 8)
			}
			exported = append(exported, item)
		}
		if err := e.post(ctx, e.TracesURL, map[string]any{"resourceSpans": []any{map[string]any{"resource": resource, "scopeSpans": []any{map[string]any{"spans": exported}}}}}); err != nil {
			return err
		}
	}
	return nil
}
