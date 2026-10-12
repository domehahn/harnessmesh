// Package performance implements ADR 0002 Phase 6: a Decision
// Intelligence / Performance Observation plane. It systematically
// records, for each task execution, which agent/model actually handled
// it and what HarnessMesh Core/Evidence/Review state authoritatively
// observed - never what the agent or an LLM merely claimed.
//
// This package is read-only with respect to routing authority: it
// observes HarnessMesh's (internal/supervisor) agent-selection decisions
// and Switchyard's (internal/modelrouting) model-selection decisions; it
// never makes or alters either. No code in this package selects an
// agent, selects a model, or changes any routing/scheduling behavior.
// See docs/decisions/0005-agent-performance-and-decision-intelligence-foundation.md.
package performance

import "sync"

// TaskClass is a stable, typed task taxonomy - never unbounded free
// text, because performance is meaningless without a task class to
// condition on ("Agent A is great at code_generation, poor at
// security_review" requires knowing which class each observation
// belongs to).
type TaskClass string

const (
	TaskClassCodeGeneration     TaskClass = "code_generation"
	TaskClassCodeModification   TaskClass = "code_modification"
	TaskClassDebugging          TaskClass = "debugging"
	TaskClassTestGeneration     TaskClass = "test_generation"
	TaskClassTestExecution      TaskClass = "test_execution"
	TaskClassCodeReview         TaskClass = "code_review"
	TaskClassSecurityReview     TaskClass = "security_review"
	TaskClassArchitectureReview TaskClass = "architecture_review"
	TaskClassDependencyAnalysis TaskClass = "dependency_analysis"
	TaskClassIACReview          TaskClass = "iac_review"
	TaskClassDocumentation      TaskClass = "documentation"
	TaskClassResearch           TaskClass = "research"
	TaskClassArtifactGeneration TaskClass = "artifact_generation"
	TaskClassRemediation        TaskClass = "remediation"
	TaskClassVerification       TaskClass = "verification"
)

var (
	taxonomyMu sync.RWMutex
	taxonomy   = map[TaskClass]bool{
		TaskClassCodeGeneration:     true,
		TaskClassCodeModification:   true,
		TaskClassDebugging:          true,
		TaskClassTestGeneration:     true,
		TaskClassTestExecution:      true,
		TaskClassCodeReview:         true,
		TaskClassSecurityReview:     true,
		TaskClassArchitectureReview: true,
		TaskClassDependencyAnalysis: true,
		TaskClassIACReview:          true,
		TaskClassDocumentation:      true,
		TaskClassResearch:           true,
		TaskClassArtifactGeneration: true,
		TaskClassRemediation:        true,
		TaskClassVerification:       true,
	}
)

// KnownTaskClass reports whether c is a registered task class.
func KnownTaskClass(c TaskClass) bool {
	taxonomyMu.RLock()
	defer taxonomyMu.RUnlock()
	return taxonomy[c]
}

// RegisterTaskClass extends the taxonomy - the explicit, deliberate
// extension point requested ("task classes must be versionable/
// extensible"), rather than an unbounded free-text field. Intended to be
// called at Core startup/configuration time, not from untrusted input;
// nothing in this package calls it with Arbiter/Supervisor-controlled
// data.
func RegisterTaskClass(c TaskClass) {
	taxonomyMu.Lock()
	defer taxonomyMu.Unlock()
	taxonomy[c] = true
}

// KnownTaskClasses returns a defensively-copied list of every currently
// registered task class.
func KnownTaskClasses() []TaskClass {
	taxonomyMu.RLock()
	defer taxonomyMu.RUnlock()
	out := make([]TaskClass, 0, len(taxonomy))
	for c := range taxonomy {
		out = append(out, c)
	}
	return out
}
