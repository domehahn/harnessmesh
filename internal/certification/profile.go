package certification

import (
	"errors"
	"fmt"
	"strings"
)

const ProfileVersion = "harnessmesh-compatible-v1"

type Profile struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	MCP              bool   `json:"mcp"`
	A2A              bool   `json:"a2a"`
	SignedEvidence   bool   `json:"signed_evidence"`
	ActionFirewall   bool   `json:"action_firewall"`
	Replay           bool   `json:"replay"`
	OTel             bool   `json:"otel"`
	MarketplaceTrust bool   `json:"marketplace_trust"`
}

type Result struct {
	Passed  bool     `json:"passed"`
	Profile string   `json:"profile"`
	Failed  []string `json:"failed,omitempty"`
}

func Validate(p Profile) (Result, error) {
	result := Result{Passed: true, Profile: ProfileVersion}
	if strings.TrimSpace(p.Name) == "" {
		result.Passed = false
		result.Failed = append(result.Failed, "name")
	}
	if p.Version != "" && p.Version != ProfileVersion {
		return Result{}, fmt.Errorf("unsupported certification profile %q", p.Version)
	}
	checks := map[string]bool{"mcp": p.MCP, "a2a": p.A2A, "signed_evidence": p.SignedEvidence, "action_firewall": p.ActionFirewall, "replay": p.Replay, "otel": p.OTel, "marketplace_trust": p.MarketplaceTrust}
	for name, ok := range checks {
		if !ok {
			result.Passed = false
			result.Failed = append(result.Failed, name)
		}
	}
	if !result.Passed {
		return result, errors.New("certification requirements are incomplete")
	}
	return result, nil
}
