// Package assurance is the bounded, provider-neutral assurance plane for
// agent actions. It records capabilities and observations, makes policy
// decisions before side effects, and produces digest-bound evidence without
// retaining raw prompt or secret content.
package assurance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Capability struct {
	ID, Kind, Subject, DeclaredBy, Digest string
	Allowed                               bool `json:"allowed"`
}
type Observation struct {
	ID, CapabilityID, Agent, Tenant, Operation, Target, Digest string
	Allowed                                                    bool
	Reason                                                     string
	At                                                         time.Time
}
type Graph struct {
	Nodes []Capability        `json:"nodes"`
	Edges map[string][]string `json:"edges,omitempty"`
}
type Rule struct {
	Effect, Agent, Tool, Path, Operation, Network, Reason string `json:"-"`
}
type Policy struct {
	Deny            []Rule `json:"deny,omitempty"`
	RequireApproval []Rule `json:"require_approval,omitempty"`
}
type Decision struct {
	Allowed          bool     `json:"allowed"`
	RequiresApproval bool     `json:"requires_approval,omitempty"`
	RiskScore        int      `json:"risk_score"`
	Reason           string   `json:"reason,omitempty"`
	Findings         []string `json:"findings,omitempty"`
}
type Evidence struct {
	Version   int           `json:"version"`
	SessionID string        `json:"session_id,omitempty"`
	Tenant    string        `json:"tenant,omitempty"`
	Agent     string        `json:"agent,omitempty"`
	Operation string        `json:"operation,omitempty"`
	Events    []Observation `json:"events"`
	Graph     Graph         `json:"graph"`
	Digest    string        `json:"digest,omitempty"`
	Signature string        `json:"signature,omitempty"`
	KeyID     string        `json:"key_id,omitempty"`
}

var sensitive = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|passwd|token|credential|private[_-]?key|\.env)`)
var injection = regexp.MustCompile(`(?i)(ignore\s+(all\s+)?previous|reveal|exfiltrat|disable\s+(security|sandbox|approval|policy)|send\s+.*(secret|token|password))`)

func Analyze(text string) (string, int, []string) {
	findings := []string{}
	score := 0
	if sensitive.MatchString(text) {
		findings = append(findings, "sensitive-data-reference")
		score += 35
	}
	if injection.MatchString(text) {
		findings = append(findings, "instruction-override-or-exfiltration")
		score += 65
	}
	return text, min(score, 100), findings
}

func Evaluate(policy Policy, agent, tool, path, operation, network string, risk int) Decision {
	for _, rule := range policy.Deny {
		if matches(rule, agent, tool, path, operation, network) {
			return Decision{Allowed: false, RiskScore: risk, Reason: first(rule.Reason, "denied by assurance policy"), Findings: []string{"policy-deny"}}
		}
	}
	for _, rule := range policy.RequireApproval {
		if matches(rule, agent, tool, path, operation, network) {
			return Decision{Allowed: false, RequiresApproval: true, RiskScore: risk, Reason: first(rule.Reason, "human approval required"), Findings: []string{"approval-required"}}
		}
	}
	if risk >= 80 {
		return Decision{Allowed: false, RiskScore: risk, Reason: "risk score exceeds runtime threshold", Findings: []string{"high-risk-content"}}
	}
	return Decision{Allowed: true, RiskScore: risk}
}

func matches(r Rule, agent, tool, path, operation, network string) bool {
	return match(r.Agent, agent) && match(r.Tool, tool) && match(r.Path, path) && match(r.Operation, operation) && match(r.Network, network)
}
func match(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	if pattern == "**" || pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "**/") {
		return strings.HasSuffix(value, strings.TrimPrefix(pattern, "**/"))
	}
	return pattern == value
}
func first(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func Fingerprint(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func SignEvidence(e Evidence, keyID string, key ed25519.PrivateKey) (Evidence, error) {
	e.Version = 1
	e.KeyID = keyID
	e.Signature = ""
	digest, err := Fingerprint(e)
	if err != nil {
		return e, err
	}
	e.Digest = digest
	raw, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	e.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw))
	return e, nil
}
func VerifyEvidence(e Evidence, pub ed25519.PublicKey) error {
	if e.Digest == "" || e.Signature == "" || len(pub) != ed25519.PublicKeySize {
		return errors.New("incomplete assurance evidence")
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil {
		return err
	}
	unsigned := e
	unsigned.Signature = ""
	raw, err := json.Marshal(unsigned)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, raw, sig) {
		return errors.New("assurance evidence signature invalid")
	}
	return nil
}

func SignEvidenceFromEnvironment(e Evidence) (Evidence, error) {
	keyText := strings.TrimSpace(os.Getenv("HARNESSMESH_ASSURANCE_SIGNING_PRIVATE_KEY"))
	if keyText == "" {
		return e, errors.New("assurance signing key is not configured")
	}
	key, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return e, errors.New("invalid assurance signing key")
	}
	keyID := strings.TrimSpace(os.Getenv("HARNESSMESH_ASSURANCE_SIGNING_KEY_ID"))
	if keyID == "" {
		return e, errors.New("assurance signing key id is required")
	}
	return SignEvidence(e, keyID, ed25519.PrivateKey(key))
}

// EvaluateOPA delegates an explicit policy decision to a local OPA binary.
// The input is JSON, no shell is used, and any unavailable/malformed decision
// fails closed. The policy must return a boolean at the configured package.
func EvaluateOPA(ctx context.Context, binary, packageName string, input map[string]any) (bool, string, error) {
	if strings.TrimSpace(binary) == "" {
		return true, "opa not configured", nil
	}
	if strings.TrimSpace(packageName) == "" {
		packageName = "data.harnessmesh.allow"
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return false, "invalid opa input", err
	}
	cmd := exec.CommandContext(ctx, binary, "eval", "--format=json", "--stdin-input", packageName)
	cmd.Stdin = bytes.NewReader(payload)
	out, err := cmd.Output()
	if err != nil {
		return false, "opa evaluation failed", err
	}
	var response struct {
		Result []struct {
			Expressions []struct {
				Value any `json:"value"`
			} `json:"expressions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &response); err != nil || len(response.Result) == 0 || len(response.Result[0].Expressions) == 0 {
		return false, "opa returned no boolean decision", errors.New("invalid OPA decision")
	}
	allowed, ok := response.Result[0].Expressions[0].Value.(bool)
	if !ok {
		return false, "opa decision is not boolean", errors.New("OPA decision must be boolean")
	}
	if !allowed {
		return false, "denied by OPA policy", nil
	}
	return true, "allowed by OPA policy", nil
}

func SortCapabilities(g *Graph) {
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
}
func Validate(g Graph) error {
	seen := map[string]bool{}
	for _, n := range g.Nodes {
		if n.ID == "" {
			return errors.New("capability id is required")
		}
		if seen[n.ID] {
			return fmt.Errorf("duplicate capability %q", n.ID)
		}
		seen[n.ID] = true
	}
	return nil
}

// LoadPolicy accepts JSON and the deliberately small, reviewable YAML subset
// used by HarnessMesh policies. Rego remains an external decision provider;
// unknown YAML constructs fail closed instead of being silently ignored.
func LoadPolicy(path string) (Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, err
	}
	var policy Policy
	if json.Unmarshal(raw, &policy) == nil {
		return policy, nil
	}
	section := ""
	var current *Rule
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(trimmed, "-") {
			section = strings.TrimSuffix(trimmed, ":")
			current = nil
			if section != "deny" && section != "require_approval" {
				return Policy{}, fmt.Errorf("unsupported policy section %q", section)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			if section == "" {
				return Policy{}, errors.New("policy rule has no section")
			}
			rule := Rule{}
			if section == "deny" {
				policy.Deny = append(policy.Deny, rule)
				current = &policy.Deny[len(policy.Deny)-1]
			} else {
				policy.RequireApproval = append(policy.RequireApproval, rule)
				current = &policy.RequireApproval[len(policy.RequireApproval)-1]
			}
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if trimmed == "" {
				continue
			}
		}
		if current == nil {
			return Policy{}, fmt.Errorf("invalid policy line %q", line)
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			return Policy{}, fmt.Errorf("invalid policy line %q", line)
		}
		key, value := strings.TrimSpace(parts[0]), strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		switch key {
		case "agent":
			current.Agent = value
		case "tool":
			current.Tool = value
		case "path":
			current.Path = value
		case "operation":
			current.Operation = value
		case "network":
			current.Network = value
		case "reason":
			current.Reason = value
		default:
			return Policy{}, fmt.Errorf("unsupported policy field %q", key)
		}
	}
	return policy, nil
}
