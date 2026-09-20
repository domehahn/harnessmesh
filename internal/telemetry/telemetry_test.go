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
