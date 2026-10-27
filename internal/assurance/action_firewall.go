package assurance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Action is the normalized unit evaluated before an agent, MCP server,
// provider, browser or external API is invoked. Callers must provide the
// authoritative identity and target; model output is never trusted for these
// fields.
type Action struct {
	SessionID string
	Tenant    string
	Project   string
	Agent     string
	Skill     string
	Tool      string
	Operation string
	Target    string
	Path      string
	Network   string
	Content   string
	DataClass string
}

type DataFlow struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	DataClass   string `json:"data_class"`
	CrossTenant bool   `json:"cross_tenant,omitempty"`
	Allowed     bool   `json:"allowed"`
	Reason      string `json:"reason,omitempty"`
}

type ActionDecision struct {
	Decision
	ActionDigest string     `json:"action_digest"`
	Flows        []DataFlow `json:"flows,omitempty"`
	BlockedWhy   string     `json:"blocked_why,omitempty"`
	EvaluatedAt  time.Time  `json:"evaluated_at"`
}

// EvaluateAction is the single reusable pre-side-effect firewall entrypoint.
// It redacts content for analysis, evaluates declarative policy, and rejects
// cross-tenant or sensitive outbound flows unless explicitly allowed by the
// policy decision. It does not execute tools or create approvals.
func EvaluateAction(ctx context.Context, policy Policy, action Action, flows []DataFlow) (ActionDecision, error) {
	if ctx == nil {
		return ActionDecision{}, errors.New("context is required")
	}
	if strings.TrimSpace(action.Tenant) == "" || strings.TrimSpace(action.Agent) == "" || strings.TrimSpace(action.Operation) == "" {
		return ActionDecision{}, errors.New("tenant, agent and operation are required")
	}
	_, risk, findings := Analyze(action.Content)
	decision := Evaluate(policy, action.Agent, action.Tool, action.Path, action.Operation, action.Network, risk)
	decision.Findings = append(decision.Findings, findings...)
	for i := range flows {
		if strings.TrimSpace(flows[i].DataClass) == "" {
			flows[i].DataClass = "unknown"
		}
		if flows[i].CrossTenant && !flows[i].Allowed {
			decision.Allowed = false
			decision.RequiresApproval = false
			decision.Reason = first(flows[i].Reason, "cross-tenant data flow is not allowed")
			decision.Findings = append(decision.Findings, "cross-tenant-data-flow")
		}
	}
	digest, err := Fingerprint(struct {
		Tenant, Project, Agent, Skill, Tool, Operation, Target, Path, Network, DataClass string
	}{action.Tenant, action.Project, action.Agent, action.Skill, action.Tool, action.Operation, action.Target, action.Path, action.Network, action.DataClass})
	if err != nil {
		return ActionDecision{}, err
	}
	result := ActionDecision{Decision: decision, ActionDigest: digest, Flows: flows, EvaluatedAt: time.Now().UTC()}
	if !decision.Allowed {
		result.BlockedWhy = fmt.Sprintf("%s (risk=%d)", first(decision.Reason, "blocked by runtime policy"), decision.RiskScore)
		return result, errors.New(result.BlockedWhy)
	}
	return result, nil
}
