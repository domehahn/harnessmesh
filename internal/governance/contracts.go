// Package governance contains the explainable safety contracts used when
// selecting an agent and exposing tools to it.
package governance

import (
	"fmt"
	"strings"
)

type AgentContract struct {
	Agent           string            `json:"agent"`
	Capabilities    []string          `json:"capabilities,omitempty"`
	AllowedTools    []string          `json:"allowed_tools,omitempty"`
	DeniedTools     []string          `json:"denied_tools,omitempty"`
	Filesystem      string            `json:"filesystem,omitempty"`
	Network         string            `json:"network,omitempty"`
	RequiresApprove bool              `json:"requires_approval"`
	MaxCostUSD      float64           `json:"max_cost_usd,omitempty"`
	MaxRuntimeSec   int               `json:"max_runtime_seconds,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type TaskRequirements struct {
	Capabilities []string `json:"capabilities,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	WriteAccess  bool     `json:"write_access"`
	Network      bool     `json:"network"`
	Approval     bool     `json:"approval"`
	MaxCostUSD   float64  `json:"max_cost_usd,omitempty"`
}

type Decision struct {
	Allowed         bool     `json:"allowed"`
	RequiresApprove bool     `json:"requires_approval"`
	Reasons         []string `json:"reasons,omitempty"`
}

func (c AgentContract) AllowsTool(tool string) bool {
	tool = strings.TrimSpace(tool)
	if tool == "" || contains(c.DeniedTools, tool) {
		return false
	}
	return len(c.AllowedTools) == 0 || contains(c.AllowedTools, tool)
}

func (c AgentContract) Satisfies(task TaskRequirements) Decision {
	d := Decision{Allowed: true}
	for _, capability := range task.Capabilities {
		if !contains(c.Capabilities, capability) {
			d.Allowed = false
			d.Reasons = append(d.Reasons, fmt.Sprintf("missing capability %q", capability))
		}
	}
	for _, tool := range task.Tools {
		if !c.AllowsTool(tool) {
			d.Allowed = false
			d.Reasons = append(d.Reasons, fmt.Sprintf("tool %q is not allowed", tool))
		}
	}
	if task.WriteAccess && !strings.EqualFold(c.Filesystem, "write") {
		d.Allowed = false
		d.Reasons = append(d.Reasons, "write access is not granted")
	}
	if task.Network && !strings.EqualFold(c.Network, "allow") {
		d.Allowed = false
		d.Reasons = append(d.Reasons, "network access is not granted")
	}
	if task.Approval && !c.RequiresApprove {
		d.RequiresApprove = true
		d.Reasons = append(d.Reasons, "task requires an approval boundary")
	}
	if task.MaxCostUSD > 0 && c.MaxCostUSD > 0 && task.MaxCostUSD > c.MaxCostUSD {
		d.Allowed = false
		d.Reasons = append(d.Reasons, "task cost exceeds agent contract limit")
	}
	return d
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted || value == "*" {
			return true
		}
	}
	return false
}

type TrustSignals struct {
	SuccessRate       float64 `json:"success_rate"`
	ReviewAcceptance  float64 `json:"review_acceptance_rate"`
	RegressionRate    float64 `json:"regression_rate"`
	SecurityIncidents int     `json:"security_incidents"`
	ManualCorrections int     `json:"manual_corrections"`
	SampleCount       int     `json:"sample_count"`
}

// TrustScore is intentionally transparent: every component is bounded and
// the result can be explained in the dashboard without an opaque model.
func TrustScore(s TrustSignals) float64 {
	score := 0.45*s.SuccessRate + 0.30*s.ReviewAcceptance + 0.15*(1-s.RegressionRate)
	score -= 0.05 * float64(s.SecurityIncidents)
	score -= 0.01 * float64(s.ManualCorrections)
	if s.SampleCount == 0 {
		score *= 0.5
	}
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}
