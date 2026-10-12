package supervisor

import (
	"time"

	"github.com/domehahn/harnessmesh/internal/telemetry"
)

// TelemetrySink records observability events for LLMSupervisor. It never
// receives prompt text, model output, or rationale content - only stage
// names, counts, durations, and provider/model identifiers already
// treated as non-secret elsewhere in this codebase (e.g.
// modelrouting.RouteMetadata).
type TelemetrySink interface {
	// StartStage marks the beginning of a plan/replan/synthesize call and
	// returns a function to call when it ends, recording duration.
	StartStage(stage string) (stop func())
	RecordRouting(stage string, decision ModelRoutingDecision)
	RecordServed(stage, provider, model string)
	RecordFailure(stage string, err error)
	RecordDecodeFailure(stage string)
	RecordProposals(stage string, count int)
}

type noopTelemetry struct{}

func (noopTelemetry) StartStage(stage string) func()                            { return func() {} }
func (noopTelemetry) RecordRouting(stage string, decision ModelRoutingDecision) {}
func (noopTelemetry) RecordServed(stage, provider, model string)                {}
func (noopTelemetry) RecordFailure(stage string, err error)                     {}
func (noopTelemetry) RecordDecodeFailure(stage string)                          {}
func (noopTelemetry) RecordProposals(stage string, count int)                   {}

var _ TelemetrySink = noopTelemetry{}

// RegistryTelemetry adapts the repository's existing dependency-free
// telemetry.Registry (Prometheus-text counters/latencies) to
// TelemetrySink, so Supervisor observability shows up next to every other
// HarnessMesh metric without introducing a second telemetry stack.
type RegistryTelemetry struct {
	Registry *telemetry.Registry
}

func NewRegistryTelemetry(r *telemetry.Registry) *RegistryTelemetry {
	return &RegistryTelemetry{Registry: r}
}

func (t *RegistryTelemetry) StartStage(stage string) func() {
	start := time.Now()
	t.Registry.Counter("supervisor." + stage + ".calls").Add(1)
	return func() {
		t.Registry.Observe("supervisor."+stage+".duration", time.Since(start))
	}
}

func (t *RegistryTelemetry) RecordRouting(stage string, decision ModelRoutingDecision) {
	t.Registry.Counter("supervisor." + stage + ".routed." + decision.Provider).Add(1)
}

func (t *RegistryTelemetry) RecordServed(stage, provider, model string) {
	t.Registry.Counter("supervisor.inference.served").Add(1)
}

func (t *RegistryTelemetry) RecordFailure(stage string, err error) {
	t.Registry.Counter("supervisor." + stage + ".failures").Add(1)
}

func (t *RegistryTelemetry) RecordDecodeFailure(stage string) {
	t.Registry.Counter("supervisor." + stage + ".decode_failures").Add(1)
}

func (t *RegistryTelemetry) RecordProposals(stage string, count int) {
	t.Registry.Counter("supervisor." + stage + ".proposals").Add(uint64(count))
}

var _ TelemetrySink = (*RegistryTelemetry)(nil)

// AdvisorTelemetry records Phase 7B's Decision Advisor observability
// counters (ADR 0006) - counts only, never task/agent rationale text or
// advisor reasons.
type AdvisorTelemetry interface {
	RecordAdvisorCall()
	RecordAdvisorFailure()
	RecordShadowRecommendation()
	RecordAdvisorySelection()
	RecordAdvisorFallback()
}

type noopAdvisorTelemetry struct{}

func (noopAdvisorTelemetry) RecordAdvisorCall()          {}
func (noopAdvisorTelemetry) RecordAdvisorFailure()       {}
func (noopAdvisorTelemetry) RecordShadowRecommendation() {}
func (noopAdvisorTelemetry) RecordAdvisorySelection()    {}
func (noopAdvisorTelemetry) RecordAdvisorFallback()      {}

var _ AdvisorTelemetry = noopAdvisorTelemetry{}

// RegistryAdvisorTelemetry adapts telemetry.Registry to AdvisorTelemetry,
// reusing the same dependency-free counters as every other HarnessMesh
// metric rather than introducing a second telemetry stack.
type RegistryAdvisorTelemetry struct{ Registry *telemetry.Registry }

func NewRegistryAdvisorTelemetry(r *telemetry.Registry) *RegistryAdvisorTelemetry {
	return &RegistryAdvisorTelemetry{Registry: r}
}
func (t *RegistryAdvisorTelemetry) RecordAdvisorCall() {
	t.Registry.Counter("advisor_calls").Add(1)
}
func (t *RegistryAdvisorTelemetry) RecordAdvisorFailure() {
	t.Registry.Counter("advisor_failures").Add(1)
}
func (t *RegistryAdvisorTelemetry) RecordShadowRecommendation() {
	t.Registry.Counter("advisor_shadow_recommendations").Add(1)
}
func (t *RegistryAdvisorTelemetry) RecordAdvisorySelection() {
	t.Registry.Counter("advisor_advisory_selections").Add(1)
}
func (t *RegistryAdvisorTelemetry) RecordAdvisorFallback() {
	t.Registry.Counter("advisor_fallbacks").Add(1)
}

var _ AdvisorTelemetry = (*RegistryAdvisorTelemetry)(nil)
