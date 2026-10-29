package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type ExecutionStep struct {
	ID               string `json:"id"`
	Tool             string `json:"tool"`
	Instruction      string `json:"instruction,omitempty"`
	RequiresApproval bool   `json:"requires_approval,omitempty"`
}

type ExecutionPlan struct {
	Skill                   string          `json:"skill"`
	Version                 string          `json:"version"`
	Digest                  string          `json:"digest"`
	Steps                   []ExecutionStep `json:"steps"`
	DeclaredTools           []string        `json:"declared_tools"`
	SchemaTokens            int             `json:"schema_tokens"`
	FullSchemaTokens        int             `json:"full_schema_tokens"`
	EstimatedTokenSavings   int             `json:"estimated_token_savings"`
	EstimatedSavingsPercent float64         `json:"estimated_savings_percent"`
}

var functionStep = regexp.MustCompile(`\[fn:\s*([A-Za-z0-9_.:-]+)\]`)

// BuildExecutionPlan turns a verified, loaded skill into a deterministic
// tool sequence. A skill with no explicit fn markers still gets a valid plan
// with its declared tools in stable lexical order.
func BuildExecutionPlan(entry Entry, fullSchemaTokens int) (ExecutionPlan, error) {
	if strings.TrimSpace(entry.Manifest.Name) == "" {
		return ExecutionPlan{}, errors.New("skill name is required")
	}
	tools := append([]string(nil), entry.Manifest.Tools...)
	sort.Strings(tools)
	if len(tools) == 0 {
		return ExecutionPlan{}, fmt.Errorf("skill %q declares no tools", entry.Manifest.Name)
	}
	allowed := map[string]bool{}
	for _, tool := range tools {
		allowed[tool] = true
	}
	steps := make([]ExecutionStep, 0, len(tools))
	seen := map[string]bool{}
	for _, match := range functionStep.FindAllStringSubmatch(entry.Content, -1) {
		tool := match[1]
		if !allowed[tool] || seen[tool] {
			continue
		}
		seen[tool] = true
		steps = append(steps, ExecutionStep{ID: fmt.Sprintf("step-%02d", len(steps)+1), Tool: tool})
	}
	for _, tool := range tools {
		if !seen[tool] {
			steps = append(steps, ExecutionStep{ID: fmt.Sprintf("step-%02d", len(steps)+1), Tool: tool})
		}
	}
	digest := sha256.Sum256([]byte(entry.Content))
	plan := ExecutionPlan{Skill: entry.Manifest.Name, Version: entry.Manifest.Version, Digest: hex.EncodeToString(digest[:]), Steps: steps, DeclaredTools: tools, SchemaTokens: entry.SchemaTokenEstimate(), FullSchemaTokens: fullSchemaTokens}
	if fullSchemaTokens > plan.SchemaTokens {
		plan.EstimatedTokenSavings = fullSchemaTokens - plan.SchemaTokens
		plan.EstimatedSavingsPercent = float64(plan.EstimatedTokenSavings) / float64(fullSchemaTokens) * 100
	}
	return plan, nil
}

func (p ExecutionPlan) AllowsTool(tool string) bool {
	for _, step := range p.Steps {
		if step.Tool == tool {
			return true
		}
	}
	return false
}
