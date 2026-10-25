package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExporterSendsRedactedOTLPLogsAndTraces(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(mustJSON(payload)), "secret") {
			t.Fatal("export payload contains a secret")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	exporter := &Exporter{LogsURL: server.URL + "/v1/logs", TracesURL: server.URL + "/v1/traces", Client: server.Client()}
	err := exporter.Export(context.Background(), "tenant-a", []Log{{Message: "safe", EventType: "job.created", Timestamp: time.Now(), TraceID: "trace-a", SpanID: "span-a"}}, []Span{{TraceID: "trace-a", SpanID: "span-a", Service: "worker", Operation: "job.created", Start: time.Now(), Duration: 12, Status: "info"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/v1/logs" || paths[1] != "/v1/traces" {
		t.Fatalf("unexpected export paths: %#v", paths)
	}
}

func mustJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}
