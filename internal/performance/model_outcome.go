package performance

import (
	"context"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// ModelOutcomeRecord is the authoritative, bounded feedback payload
// Decision Intelligence may surface toward a model-routing observer
// (e.g. a future Switchyard-side integration). Every field is derived
// only from already-authoritative PerformanceObservation state - never
// from LLM text, agent stdout, or Finding/Arbiter output.
//
// There is no RoutingDecisionID field here, unlike ADR 0007's own Go
// sketch: this codebase has no persisted, named "model routing decision"
// ID anywhere (supervisor.ModelRoutingDecision carries RouteID/Provider/
// Model/Reason but no ID; economy.RoutingDecision.ID is a DIFFERENT,
// unrelated concept - agent/participant selection, not model routing).
// ModelUsage.RouteID already serves as the correlation key a consumer
// would otherwise want a separate ID for, so none was invented.
type ModelOutcomeRecord struct {
	Tenant           string
	ExecutionID      string
	TaskClass        TaskClass
	AgentID          supervisor.AgentID
	ModelUsage       ModelUsageRef
	ExecutionOutcome ExecutionOutcome
	QualityOutcome   QualityOutcome
}

// ModelOutcomeObserver is a bounded, optional feedback sink - evidence/
// telemetry about an observed outcome, NEVER a routing command. Nothing
// in this package or any caller ever invokes a method on Switchyard/
// modelrouting as a result of this being called; see ADR 0007's
// "Feedback semantics" section. Mirrors the existing Observer pattern
// (ExecutionObserver/ReviewFinalizationObserver) - optional, nil-safe,
// failure-isolated.
type ModelOutcomeObserver interface {
	ObserveModelOutcome(ctx context.Context, outcome ModelOutcomeRecord) error
}

// NoopModelOutcomeObserver discards every record - the default when no
// feedback sink is configured.
type NoopModelOutcomeObserver struct{}

func (NoopModelOutcomeObserver) ObserveModelOutcome(ctx context.Context, outcome ModelOutcomeRecord) error {
	return nil
}

var _ ModelOutcomeObserver = NoopModelOutcomeObserver{}

// TelemetryModelOutcomeObserver records only bounded counts via
// WiringTelemetry - never the record's content. This is intentionally
// the ONLY implementation this phase ships: Switchyard currently has no
// outcome-feedback transport/endpoint of any kind (confirmed by
// inspection of internal/modelrouting - SwitchyardInferenceRouter only
// ever sends chat-completion requests, never a feedback/outcome call),
// so this phase builds only the HarnessMesh-side interface/boundary, not
// an invented HTTP API against Switchyard - documented as a residual
// risk/future-integration point in ADR 0007, per the mandate's explicit
// instruction not to fabricate a transport that doesn't exist.
type TelemetryModelOutcomeObserver struct {
	Telemetry WiringTelemetry
}

func NewTelemetryModelOutcomeObserver(tel WiringTelemetry) *TelemetryModelOutcomeObserver {
	if tel == nil {
		tel = noopWiringTelemetry{}
	}
	return &TelemetryModelOutcomeObserver{Telemetry: tel}
}

func (o *TelemetryModelOutcomeObserver) ObserveModelOutcome(ctx context.Context, outcome ModelOutcomeRecord) error {
	o.Telemetry.RecordModelOutcomeFeedback()
	return nil
}

var _ ModelOutcomeObserver = (*TelemetryModelOutcomeObserver)(nil)

// emitModelOutcomeFeedback calls observer for every ModelUsages entry on
// obs, exactly once per entry - failure-isolated: an error or panic from
// observer never affects the caller (the SAME execution-truth/review-
// truth-unchanged discipline every other Observer in this package
// already follows). Telemetry records a failure counter; nothing is
// ever retried (see ADR 0007's explicit "no hidden goroutine retry loop"
// decision) and nothing is ever logged beyond the bounded counts
// WiringTelemetry itself already restricts failures to.
func emitModelOutcomeFeedback(ctx context.Context, observer ModelOutcomeObserver, tel WiringTelemetry, obs PerformanceObservation) {
	if observer == nil {
		return
	}
	for _, usage := range obs.ModelUsages {
		func() {
			defer func() {
				if r := recover(); r != nil {
					tel.RecordModelOutcomeFeedbackFailure()
				}
			}()
			record := ModelOutcomeRecord{
				Tenant: obs.Tenant, ExecutionID: obs.ExecutionID, TaskClass: obs.TaskClass,
				AgentID: obs.AgentID, ModelUsage: usage,
				ExecutionOutcome: obs.ExecutionOutcome, QualityOutcome: obs.QualityOutcome,
			}
			if err := observer.ObserveModelOutcome(ctx, record); err != nil {
				tel.RecordModelOutcomeFeedbackFailure()
			}
		}()
	}
}
