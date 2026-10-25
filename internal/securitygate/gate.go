// Package securitygate applies the runtime data-boundary rules used when
// untrusted agent content is forwarded to another agent or persisted as an
// orchestration message. It is intentionally deterministic and does not grant
// capabilities based on model output.
package securitygate

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"

	"github.com/domehahn/harnessmesh/internal/assurance"
	"github.com/domehahn/harnessmesh/internal/contextpack"
)

var ErrBlocked = errors.New("runtime security policy blocked untrusted agent content")

type Decision struct {
	Allowed   bool
	Redacted  bool
	Reason    string
	RiskScore int
	Findings  []string
}

var dangerous = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+(all\s+)?previous\s+instructions`),
	regexp.MustCompile(`(?i)(reveal|print|dump|show|read|exfiltrat).{0,80}(secret|password|api[_ -]?key|token|credential|\.env)`),
	regexp.MustCompile(`(?i)(upload|send|post|transmit|forward).{0,80}(secret|password|api[_ -]?key|token|credential|\.env)`),
	regexp.MustCompile(`(?i)(disable|bypass|turn\s+off).{0,40}(security|sandbox|approval|policy|guard)`),
}

// Check treats content as data, redacts recognizable credentials, and blocks
// only high-confidence attempts to override policy or exfiltrate secrets.
func Check(content string) (string, Decision, error) {
	return CheckContext(context.Background(), content, "", "", "", "", "")
}

func CheckWithContext(content, agent, tool, path, operation, network string) (string, Decision, error) {
	return CheckContext(context.Background(), content, agent, tool, path, operation, network)
}

func CheckContext(ctx context.Context, content, agent, tool, path, operation, network string) (string, Decision, error) {
	redacted := contextpack.RedactSecrets(content)
	_, score, findings := assurance.Analyze(redacted)
	policyPath := strings.TrimSpace(os.Getenv("HARNESSMESH_POLICY_FILE"))
	policy := assurance.Policy{}
	if policyPath != "" {
		loaded, err := assurance.LoadPolicy(policyPath)
		if err != nil {
			return "", Decision{Allowed: false, Reason: "policy could not be loaded", RiskScore: score, Findings: []string{"policy-load-failed"}}, err
		}
		policy = loaded
	}
	if opaBinary := strings.TrimSpace(os.Getenv("HARNESSMESH_OPA_BIN")); opaBinary != "" {
		allowed, reason, opaErr := assurance.EvaluateOPA(ctx, opaBinary, os.Getenv("HARNESSMESH_OPA_PACKAGE"), map[string]any{"agent": agent, "tool": tool, "path": path, "operation": operation, "network": network, "risk_score": score, "findings": findings})
		if opaErr != nil || !allowed {
			if opaErr == nil {
				opaErr = ErrBlocked
			}
			return redacted, Decision{Allowed: false, Redacted: redacted != content, Reason: reason, RiskScore: score, Findings: append(findings, "opa-policy")}, opaErr
		}
	}
	if decision := assurance.Evaluate(policy, agent, tool, path, operation, network, score); !decision.Allowed {
		decision.Findings = append(decision.Findings, findings...)
		return redacted, Decision{Allowed: false, Redacted: redacted != content, Reason: decision.Reason, RiskScore: score, Findings: decision.Findings}, ErrBlocked
	}
	for _, pattern := range dangerous {
		if pattern.MatchString(redacted) {
			return redacted, Decision{Allowed: false, Redacted: redacted != content, Reason: "prompt-injection or secret-exfiltration pattern", RiskScore: score, Findings: findings}, ErrBlocked
		}
	}
	return redacted, Decision{Allowed: true, Redacted: redacted != content, RiskScore: score, Findings: findings}, nil
}

func CheckEnabled() bool {
	return strings.TrimSpace(strings.ToLower(os.Getenv("HARNESSMESH_RUNTIME_SECURITY"))) != "0"
}
