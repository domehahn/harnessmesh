// Package telemetry provides dependency-free counters and latency metrics.
// It exposes Prometheus text so an OpenTelemetry Collector or Prometheus can
// scrape HarnessMesh without forcing a specific exporter into the core.
package telemetry

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Counter struct{ value atomic.Uint64 }

func (c *Counter) Add(delta uint64) { c.value.Add(delta) }
func (c *Counter) Value() uint64    { return c.value.Load() }

type Registry struct {
	mu       sync.RWMutex
	counters map[string]*Counter
	latency  map[string]*latencyMetric
}

type latencyMetric struct {
	count atomic.Uint64
	total atomic.Uint64
}

func NewRegistry() *Registry {
	return &Registry{counters: make(map[string]*Counter), latency: make(map[string]*latencyMetric)}
}

func (r *Registry) Counter(name string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.counters[name] == nil {
		r.counters[name] = &Counter{}
	}
	return r.counters[name]
}

func (r *Registry) Observe(name string, duration time.Duration) {
	r.mu.Lock()
	metric := r.latency[name]
	if metric == nil {
		metric = &latencyMetric{}
		r.latency[name] = metric
	}
	r.mu.Unlock()
	metric.count.Add(1)
	metric.total.Add(uint64(duration.Microseconds()))
}

func (r *Registry) Prometheus() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var b strings.Builder
	names := make([]string, 0, len(r.counters))
	for name := range r.counters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "%s %d\n", name, r.counters[name].Value())
	}
	latencies := make([]string, 0, len(r.latency))
	for name := range r.latency {
		latencies = append(latencies, name)
	}
	sort.Strings(latencies)
	for _, name := range latencies {
		m := r.latency[name]
		fmt.Fprintf(&b, "%s_count %d\n%s_total_microseconds %d\n", name, m.count.Load(), name, m.total.Load())
	}
	return b.String()
}
