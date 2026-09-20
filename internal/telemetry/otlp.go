package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Span struct {
	Name       string            `json:"name"`
	StartTime  time.Time         `json:"start_time"`
	EndTime    time.Time         `json:"end_time"`
	Attributes map[string]string `json:"attributes,omitempty"`
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
