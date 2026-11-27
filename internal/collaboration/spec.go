package collaboration

import (
	"fmt"
	"strings"
)

// ComputeChangeTrustScore calculates a deterministic 0-100 trust score and
// review tier ("fast-track", "standard-review", "human-escalation") for a
// change transaction based on proof coverage, diff footprint and policy checks.
func ComputeChangeTrustScore(totalObligations, passedObligations, failedObligations, changedFiles, policyViolations int) (int, string) {
	if policyViolations > 0 || failedObligations > 0 {
		return 0, "human-escalation"
	}
	if totalObligations <= 0 {
		return 30, "human-escalation"
	}
	coverageScore := (passedObligations * 80) / totalObligations
	sizeBonus := 20
	if changedFiles > 20 {
		sizeBonus = 0
	} else if changedFiles > 5 {
		sizeBonus = 10
	}
	score := coverageScore + sizeBonus
	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}
	tier := "standard-review"
	if score >= 85 && passedObligations == totalObligations {
		tier = "fast-track"
	} else if score < 50 {
		tier = "human-escalation"
	}
	return score, tier
}

// SpecContract represents an SDD (Spec-Driven Development) pre-execution contract
// checked against the 7 Spec Rules before an AI coding agent modifies files.
type DecisionReference struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
}

type SpecContract struct {
	AcceptanceProofs     map[string][]string `json:"acceptance_proofs,omitempty"`
	DecisionReferences   []DecisionReference `json:"decision_references,omitempty"`
	AcceptanceCriteria   []string            `json:"acceptance_criteria,omitempty"`
	Title                string              `json:"title"`
	Intent               string              `json:"intent"`
	NonGoals             []string            `json:"non_goals"`
	Scope                []string            `json:"scope"`
	MaxChangedFiles      int                 `json:"max_changed_files"`
	MaxChangedLines      int                 `json:"max_changed_lines"`
	NeverCut             []string            `json:"never_cut"`
	VerificationCommands []string            `json:"verification_commands"`
	OpenQuestions        []string            `json:"open_questions,omitempty"`
}

// SpecValidationReport summarizes compliance with the 7 SDD Rules and lists
// concrete "Grill-Me" interrogation prompts for any underspecified area.
type SpecValidationReport struct {
	Ready            bool     `json:"ready"`
	Score            int      `json:"score"` // 0-7 rules satisfied
	PassedRules      []string `json:"passed_rules"`
	FailedRules      []string `json:"failed_rules,omitempty"`
	GrillMeQuestions []string `json:"grill_me_questions,omitempty"`
}

var ambiguousSpecMarkers = []string{"todo", "tbd", "fixme", "???", "maybe", "unsure"}

// ValidateSpec deterministically evaluates a SpecContract against the 7 SDD
// rules and generates targeted Grill-Me questions when ambiguities exist.
func ValidateSpec(spec SpecContract) SpecValidationReport {
	var passed []string
	var failed []string
	var questions []string

	// Rule 1: Clear Goal / Intent without vague placeholders
	title := strings.TrimSpace(spec.Title)
	intent := strings.TrimSpace(spec.Intent)
	hasAmbiguity := false
	lowerIntent := strings.ToLower(title + " " + intent)
	for _, m := range ambiguousSpecMarkers {
		if strings.Contains(lowerIntent, m) {
			hasAmbiguity = true
			break
		}
	}
	if title != "" && len(intent) >= 15 && !hasAmbiguity {
		passed = append(passed, "rule_1_clear_intent")
	} else {
		failed = append(failed, "rule_1_clear_intent")
		questions = append(questions, "What is the exact, unambiguous goal of this change (without TODO/TBD placeholders)?")
	}

	// Rule 2: Explicit Non-Goals (anti-overengineering boundary)
	if len(nonEmptyStrings(spec.NonGoals)) > 0 {
		passed = append(passed, "rule_2_explicit_non_goals")
	} else {
		failed = append(failed, "rule_2_explicit_non_goals")
		questions = append(questions, "Which adjacent features, refactorings or abstractions are explicit Non-Goals for this change?")
	}

	// Rule 3: Bounded File Scope
	scope := nonEmptyStrings(spec.Scope)
	hasWildcardScope := false
	for _, s := range scope {
		if s == "*" || s == "**" || s == "**/*" {
			hasWildcardScope = true
			break
		}
	}
	if len(scope) > 0 && !hasWildcardScope {
		passed = append(passed, "rule_3_bounded_file_scope")
	} else {
		failed = append(failed, "rule_3_bounded_file_scope")
		questions = append(questions, "Which specific repository paths or packages are in scope for modification?")
	}

	// Rule 4: Explicit Diff Budget
	if spec.MaxChangedFiles > 0 && spec.MaxChangedLines > 0 {
		passed = append(passed, "rule_4_diff_budget")
	} else {
		failed = append(failed, "rule_4_diff_budget")
		questions = append(questions, fmt.Sprintf("What is the maximum diff budget (max_changed_files and max_changed_lines) before the agent must stop and split the change?"))
	}

	// Rule 5: Never-Cut Invariants / Protected Paths
	if len(nonEmptyStrings(spec.NeverCut)) > 0 {
		passed = append(passed, "rule_5_never_cut_list")
	} else {
		failed = append(failed, "rule_5_never_cut_list")
		questions = append(questions, "Which existing files, public APIs or security invariants are on the Never-Cut list and must not be removed or altered?")
	}

	// Rule 6: Deterministic Verification Commands
	if len(nonEmptyStrings(spec.VerificationCommands)) > 0 {
		passed = append(passed, "rule_6_verification_commands")
	} else {
		failed = append(failed, "rule_6_verification_commands")
		questions = append(questions, "Which exact test or build commands prove that this specification is satisfied?")
	}

	// Rule 7: Zero Unresolved Open Questions
	openQ := nonEmptyStrings(spec.OpenQuestions)
	if len(openQ) == 0 && !hasAmbiguity {
		passed = append(passed, "rule_7_zero_open_questions")
	} else {
		failed = append(failed, "rule_7_zero_open_questions")
		for _, q := range openQ {
			questions = append(questions, "Resolve open spec question: "+q)
		}
	}

	return SpecValidationReport{
		Ready:            len(failed) == 0,
		Score:            len(passed),
		PassedRules:      passed,
		FailedRules:      failed,
		GrillMeQuestions: questions,
	}
}

func nonEmptyStrings(items []string) []string {
	var out []string
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
