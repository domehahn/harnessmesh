package collaboration

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
	"github.com/domehahn/harnessmesh/internal/telemetry"
)

// operationState centralizes operational safeguards shared by every adapter.
// The durable parts are written to SQLite while fast admission decisions stay
// in memory to avoid making every invocation depend on a database round trip.
type operationState struct {
	mu         sync.Mutex
	cfg        *config.Config
	store      store.Store
	health     map[string]protocol.AgentHealth
	usedTokens int64
	usedCost   float64
	metrics    *telemetry.Registry
}

func newOperationState(cfg *config.Config, st store.Store) *operationState {
	o := &operationState{cfg: cfg, store: st, health: make(map[string]protocol.AgentHealth), metrics: telemetry.NewRegistry()}
	if st != nil {
		if hs, err := st.ListAgentHealth(context.Background()); err == nil {
			for _, h := range hs {
				o.health[h.Agent] = h
			}
		}
		if budget, err := st.GetGlobalBudget(context.Background(), "global"); err == nil && budget != nil {
			o.usedTokens = budget.UsedTokens
			o.usedCost = budget.UsedCostUSD
		}
	}
	return o
}

func (o *operationState) thresholdFailures() int {
	if o.cfg == nil || o.cfg.Collaboration.CircuitBreakerFailures <= 0 {
		return 3
	}
	return o.cfg.Collaboration.CircuitBreakerFailures
}
func (o *operationState) cooldown() time.Duration {
	if o.cfg == nil {
		return 2 * time.Minute
	}
	return o.cfg.Collaboration.CircuitBreakerCooldownDuration()
}

func (o *operationState) before(agentName string, req agent.InvokeRequest) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	h := o.health[agentName]
	if !h.CircuitOpenUntil.IsZero() && time.Now().Before(h.CircuitOpenUntil) {
		return fmt.Errorf("agent %q circuit breaker open until %s", agentName, h.CircuitOpenUntil.UTC().Format(time.RFC3339))
	}
	if !h.QuotaResetAt.IsZero() && time.Now().Before(h.QuotaResetAt) {
		return fmt.Errorf("agent %q quota wait until %s", agentName, h.QuotaResetAt.UTC().Format(time.RFC3339))
	}
	if o.cfg != nil {
		c := o.cfg.Collaboration
		if c.GlobalMaxTokens > 0 && o.usedTokens+req.MaxTokens > c.GlobalMaxTokens {
			return &protocol.BudgetExceededError{Metric: "global_tokens", Used: float64(o.usedTokens + req.MaxTokens), Limit: float64(c.GlobalMaxTokens)}
		}
		if c.GlobalMaxCostUSD > 0 && o.usedCost+req.MaxCostUSD > c.GlobalMaxCostUSD {
			return &protocol.BudgetExceededError{Metric: "global_cost_usd", Used: o.usedCost + req.MaxCostUSD, Limit: c.GlobalMaxCostUSD}
		}
		if c.ApprovalCostUSD > 0 && req.MaxCostUSD >= c.ApprovalCostUSD {
			if req.ApprovalID == "" {
				id := fmt.Sprintf("approval_%d", time.Now().UnixNano())
				if o.store != nil {
					_ = o.store.SaveApproval(context.Background(), &protocol.ApprovalRequest{ID: id, Agent: agentName, Reason: fmt.Sprintf("estimated cost %.4f exceeds approval threshold %.4f", req.MaxCostUSD, c.ApprovalCostUSD), EstimatedCostUSD: req.MaxCostUSD, EstimatedTokens: req.MaxTokens, Status: protocol.ApprovalPending, CreatedAt: time.Now().UTC()})
				}
				return &protocol.ApprovalRequiredError{ApprovalID: id, Agent: agentName, Reason: "operation exceeds configured approval threshold"}
			}
			if o.store != nil {
				a, err := o.store.GetApproval(context.Background(), req.ApprovalID)
				if err != nil || a == nil || a.Status != protocol.ApprovalApproved {
					return &protocol.ApprovalRequiredError{ApprovalID: req.ApprovalID, Agent: agentName, Reason: "approval is not approved"}
				}
			}
		}
	}
	o.usedTokens += req.MaxTokens
	o.usedCost += req.MaxCostUSD
	if o.store != nil {
		maxTokens, maxCost := int64(0), float64(0)
		if o.cfg != nil {
			maxTokens = o.cfg.Collaboration.GlobalMaxTokens
			maxCost = o.cfg.Collaboration.GlobalMaxCostUSD
		}
		_ = o.store.UpdateGlobalBudget(context.Background(), &store.BudgetLedger{Scope: "global", UsedTokens: o.usedTokens, UsedCostUSD: o.usedCost, MaxTokens: maxTokens, MaxCostUSD: maxCost, UpdatedAt: time.Now().UTC()})
	}
	o.metrics.Counter("harnessmesh_invocations_started_total").Add(1)
	return nil
}

func (o *operationState) after(ctx context.Context, h agent.Harness, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	name := h.ID()
	now := time.Now().UTC()
	state := o.health[name]
	state.Agent = name
	state.Adapter = h.AdapterType()
	state.TotalInvocations++
	state.UpdatedAt = now
	if err == nil {
		state.Successful++
		state.ConsecutiveFails = 0
		state.Status = protocol.AgentHealthHealthy
		state.LastSuccess = now
		state.CircuitOpenUntil = time.Time{}
		state.LastError = ""
		o.metrics.Counter("harnessmesh_invocations_succeeded_total").Add(1)
	} else {
		state.Failed++
		state.ConsecutiveFails++
		state.LastFailure = now
		state.LastError = err.Error()
		state.Status = protocol.AgentHealthDegraded
		o.metrics.Counter("harnessmesh_invocations_failed_total").Add(1)
		if protocol.IsQuotaLimited(err) {
			state.Status = protocol.AgentHealthQuotaWait
			if reset, ok := protocol.QuotaRetryAt(err); ok {
				state.QuotaResetAt = reset
			}
		} else if state.ConsecutiveFails >= o.thresholdFailures() {
			state.Status = protocol.AgentHealthCircuitOpen
			state.CircuitOpenUntil = now.Add(o.cooldown())
		}
	}
	o.health[name] = state
	if o.store != nil {
		_ = o.store.SaveAgentHealth(ctx, &state)
	}
}

func (o *operationState) invoke(ctx context.Context, h agent.Harness, req agent.InvokeRequest) (agent.InvokeResult, error) {
	if err := o.before(h.ID(), req); err != nil {
		return agent.InvokeResult{}, err
	}
	start := time.Now()
	res, err := h.Invoke(ctx, req)
	o.metrics.Observe("harnessmesh_invocation_duration", time.Since(start))
	o.after(ctx, h, err)
	return res, err
}
func (o *operationState) healthSnapshot() []protocol.AgentHealth {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]protocol.AgentHealth, 0, len(o.health))
	for _, h := range o.health {
		out = append(out, h)
	}
	return out
}
func (o *operationState) metricsText() string { return o.metrics.Prometheus() }

func (e *Engine) invokeHarness(ctx context.Context, h agent.Harness, req agent.InvokeRequest) (agent.InvokeResult, error) {
	return e.operations.invoke(ctx, h, req)
}
func (e *Engine) AgentHealth(ctx context.Context) ([]protocol.AgentHealth, error) {
	if e.operations == nil {
		return nil, nil
	}
	if e.store != nil {
		if hs, err := e.store.ListAgentHealth(ctx); err == nil && len(hs) > 0 {
			return hs, nil
		}
	}
	return e.operations.healthSnapshot(), nil
}
func (e *Engine) OperationalMetrics() string {
	if e.operations == nil {
		return ""
	}
	return e.operations.metricsText()
}

func (e *Engine) RequestApproval(ctx context.Context, a *protocol.ApprovalRequest) error {
	if a.ID == "" {
		a.ID = fmt.Sprintf("approval_%d", time.Now().UnixNano())
	}
	a.Status = protocol.ApprovalPending
	return e.store.SaveApproval(ctx, a)
}
func (e *Engine) DecideApproval(ctx context.Context, id, decider string, approved bool) (*protocol.ApprovalRequest, error) {
	a, err := e.store.GetApproval(ctx, id)
	if err != nil || a == nil {
		return a, fmt.Errorf("approval %q not found", id)
	}
	now := time.Now().UTC()
	a.Status = protocol.ApprovalRejected
	if approved {
		a.Status = protocol.ApprovalApproved
	}
	a.DecidedBy = decider
	a.DecidedAt = &now
	return a, e.store.SaveApproval(ctx, a)
}
func (e *Engine) Approvals(ctx context.Context, status protocol.ApprovalStatus) ([]protocol.ApprovalRequest, error) {
	_, _ = e.store.ExpireApprovals(ctx, time.Now().UTC().Add(-24*time.Hour))
	return e.store.ListApprovals(ctx, status)
}

func (e *Engine) AddJobDependency(ctx context.Context, jobID, dependsOn string) error {
	return e.store.SaveJobDependency(ctx, &store.JobDependency{JobID: jobID, DependsOn: dependsOn, Status: "pending"})
}

func (e *Engine) JobDependenciesReady(ctx context.Context, jobID string, completed map[string]bool) (bool, []store.JobDependency, error) {
	deps, err := e.store.ListJobDependencies(ctx, jobID)
	if err != nil {
		return false, nil, err
	}
	for _, dep := range deps {
		if dep.Status != "completed" && !completed[dep.DependsOn] {
			return false, deps, nil
		}
	}
	return true, deps, nil
}
