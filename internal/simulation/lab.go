// Package simulation provides deterministic, non-executing safety scenarios.
// It is safe to run in CI because it never starts an agent or calls a provider.
package simulation

import "fmt"

type Fault string

const (
	FaultToolDenied          Fault = "tool_denied"
	FaultAgentTimeout        Fault = "agent_timeout"
	FaultQuotaWait           Fault = "quota_wait"
	FaultApprovalBlocked     Fault = "approval_blocked"
	FaultReplayInconsistent  Fault = "replay_inconsistent"
	FaultProviderUnavailable Fault = "provider_unavailable"
)

type Scenario struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Fault       Fault  `json:"fault"`
	Expected    string `json:"expected"`
}

type Observation struct {
	RetryScheduled bool
	ApprovalRaised bool
	Execution      string
	ReplayStatus   string
	ProviderState  string
}

type Result struct {
	ScenarioID string   `json:"scenario_id"`
	Passed     bool     `json:"passed"`
	Findings   []string `json:"findings,omitempty"`
}

func Catalog() []Scenario {
	return []Scenario{
		{ID: "tool-denial", Name: "MCP tool denial", Fault: FaultToolDenied, Description: "A tool is outside the agent contract or MCP allowlist.", Expected: "deny without dialing the external tool"},
		{ID: "agent-timeout", Name: "Agent timeout", Fault: FaultAgentTimeout, Description: "An agent exceeds its execution deadline.", Expected: "cancel, persist evidence and schedule bounded retry"},
		{ID: "quota-wait", Name: "Provider quota wait", Fault: FaultQuotaWait, Description: "The provider reports a future quota reset.", Expected: "do not spin; persist retry-at and wait"},
		{ID: "approval-blocked", Name: "Approval blocked", Fault: FaultApprovalBlocked, Description: "A destructive operation has no human decision.", Expected: "remain pending and do not execute"},
		{ID: "replay-inconsistent", Name: "Replay inconsistency", Fault: FaultReplayInconsistent, Description: "A replay lacks the original candidate snapshot.", Expected: "mark replay incomplete and never execute"},
		{ID: "provider-unavailable", Name: "Provider unavailable", Fault: FaultProviderUnavailable, Description: "The configured model provider is unavailable.", Expected: "fail closed or use an explicitly configured fallback"},
	}
}

func Evaluate(s Scenario, o Observation) Result {
	r := Result{ScenarioID: s.ID, Passed: true}
	need := func(ok bool, finding string) {
		if !ok {
			r.Passed = false
			r.Findings = append(r.Findings, finding)
		}
	}
	switch s.Fault {
	case FaultToolDenied:
		need(o.Execution == "denied", "denied tool was not blocked")
	case FaultAgentTimeout:
		need(o.Execution == "cancelled", "timed out execution was not cancelled")
		need(o.RetryScheduled, "timeout did not schedule a bounded retry")
	case FaultQuotaWait:
		need(o.ProviderState == "quota_wait", "quota wait was not persisted")
		need(o.RetryScheduled, "quota wait did not schedule a retry")
	case FaultApprovalBlocked:
		need(o.ApprovalRaised, "approval boundary was not raised")
		need(o.Execution == "pending", "blocked approval was executed")
	case FaultReplayInconsistent:
		need(o.ReplayStatus == "incomplete", "inconsistent replay was not marked incomplete")
		need(o.Execution != "executed", "inconsistent replay executed work")
	case FaultProviderUnavailable:
		need(o.ProviderState == "unavailable" || o.ProviderState == "fallback", "provider outage was not classified")
	default:
		r.Passed = false
		r.Findings = append(r.Findings, fmt.Sprintf("unknown simulation fault %q", s.Fault))
	}
	return r
}
