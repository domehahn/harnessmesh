package skills

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/domehahn/harnessmesh/internal/evaluation"
)

// HarnessPatchProposal represents a deterministic, approval-gated mutation
// proposal derived from execution traces in the Outer Improvement Loop.
type HarnessPatchProposal struct {
	Skill             string   `json:"skill"`
	BaseVersion       string   `json:"base_version"`
	ProposedVersion   string   `json:"proposed_version"`
	RequiresApproval  bool     `json:"requires_approval"`
	AddedTools        []string `json:"added_tools,omitempty"`
	AddedTriggers     []string `json:"added_triggers,omitempty"`
	EnableStrictOrder bool     `json:"enable_strict_order,omitempty"`
	SuggestedSteps    []string `json:"suggested_steps,omitempty"`
	Findings          []string `json:"findings,omitempty"`
	ManifestPatch     Manifest `json:"manifest_patch"`
}

// HarnessProbeReport combines the Propose, Probe, and Compose phases of
// recursive harness optimization for a skill artifact.
type HarnessProbeReport struct {
	Skill            string                       `json:"skill"`
	BaseVersion      string                       `json:"base_version"`
	CandidateVersion string                       `json:"candidate_version"`
	Proposal         HarnessPatchProposal         `json:"proposal"`
	Comparison       evaluation.HarnessComparison `json:"comparison"`
	Decision         string                       `json:"decision"` // promote, hold, rollback
	RequiresApproval bool                         `json:"requires_approval"`
}

// ProposeHarnessPatch inspects observed execution traces against a skill's
// manifest and SKILL.md content to propose deterministic harness refinements.
// It never auto-applies changes or grants unauthorized permissions without approval.
func ProposeHarnessPatch(entry Entry, runs []evaluation.Run) (HarnessPatchProposal, error) {
	name := strings.TrimSpace(entry.Manifest.Name)
	if name == "" {
		return HarnessPatchProposal{}, errors.New("skill name is required")
	}
	baseVersion := strings.TrimSpace(entry.Manifest.Version)
	if baseVersion == "" {
		baseVersion = "0.0.0"
	}
	nextVersion := bumpPatchVersion(baseVersion)

	declaredTools := map[string]bool{}
	for _, t := range entry.Manifest.Tools {
		declaredTools[strings.TrimSpace(t)] = true
	}
	declaredTriggers := map[string]bool{}
	for _, tr := range entry.Manifest.Triggers {
		declaredTriggers[strings.ToLower(strings.TrimSpace(tr))] = true
	}

	var addedTools []string
	seenAddedTool := map[string]bool{}
	var orderedTools []string
	seenOrdered := map[string]bool{}
	var findings []string
	seenFinding := map[string]bool{}
	addFinding := func(msg string) {
		if !seenFinding[msg] {
			seenFinding[msg] = true
			findings = append(findings, msg)
		}
	}

	hadRetryOrFailure := false
	for _, run := range runs {
		if !run.PolicyOK {
			addFinding(fmt.Sprintf("run %q violated policy; keep strict firewall enforcement", run.TaskID))
		}
		for _, step := range run.Steps {
			if !step.PolicyOK {
				addFinding(fmt.Sprintf("step %q in task %q failed policy check; do not auto-allow", step.Name, run.TaskID))
				continue
			}
			if step.Kind == "retry" || step.Status == "failed" {
				hadRetryOrFailure = true
				addFinding(fmt.Sprintf("step %q (%s) required retry or failed; consider enabling strict_order", step.Name, step.Kind))
			}
			if step.Kind == "tool" && step.Status == "completed" && step.PolicyOK {
				toolName := strings.TrimSpace(step.Name)
				if toolName == "" {
					continue
				}
				if !seenOrdered[toolName] {
					seenOrdered[toolName] = true
					orderedTools = append(orderedTools, toolName)
				}
				if !declaredTools[toolName] && !seenAddedTool[toolName] {
					seenAddedTool[toolName] = true
					addedTools = append(addedTools, toolName)
					addFinding(fmt.Sprintf("policy-compliant tool %q was used in successful steps but is missing from skill manifest tools", toolName))
				}
			}
		}
	}
	sort.Strings(addedTools)

	var suggestedSteps []string
	for _, toolName := range orderedTools {
		marker := "[fn: " + toolName + "]"
		if !strings.Contains(entry.Content, marker) {
			suggestedSteps = append(suggestedSteps, marker)
		}
	}
	if len(suggestedSteps) > 0 {
		addFinding("SKILL.md is missing explicit [fn: tool] markers for observed tool sequence")
	}

	enableStrict := false
	if hadRetryOrFailure && !entry.Manifest.StrictOrder {
		enableStrict = true
	}

	patched := entry.Manifest
	patched.Version = nextVersion
	if len(addedTools) > 0 {
		merged := append([]string(nil), entry.Manifest.Tools...)
		merged = append(merged, addedTools...)
		sort.Strings(merged)
		patched.Tools = merged
	}
	if enableStrict {
		patched.StrictOrder = true
	}

	return HarnessPatchProposal{
		Skill:             name,
		BaseVersion:       baseVersion,
		ProposedVersion:   nextVersion,
		RequiresApproval:  true,
		AddedTools:        addedTools,
		EnableStrictOrder: enableStrict,
		SuggestedSteps:    suggestedSteps,
		Findings:          findings,
		ManifestPatch:     patched,
	}, nil
}

// ProbeHarness executes the full Propose-Probe-Compose cycle comparing a
// baseline run set against a candidate run set for a skill.
func ProbeHarness(entry Entry, baselineRuns, candidateRuns []evaluation.Run, minScore float64) (HarnessProbeReport, error) {
	proposal, err := ProposeHarnessPatch(entry, baselineRuns)
	if err != nil {
		return HarnessProbeReport{}, err
	}
	comp, err := evaluation.CompareHarnesses(baselineRuns, candidateRuns, minScore)
	if err != nil {
		return HarnessProbeReport{}, err
	}
	return HarnessProbeReport{
		Skill:            proposal.Skill,
		BaseVersion:      proposal.BaseVersion,
		CandidateVersion: proposal.ProposedVersion,
		Proposal:         proposal,
		Comparison:       comp,
		Decision:         comp.Recommendation,
		RequiresApproval: true,
	}, nil
}

func bumpPatchVersion(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) != 3 {
		return "0.0.1"
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil || patch < 0 {
		return parts[0] + "." + parts[1] + ".1"
	}
	return fmt.Sprintf("%s.%s.%d", parts[0], parts[1], patch+1)
}
