package supervisor

import "context"

// AgentDecisionMode controls whether/how Scheduler consults an
// AgentDecisionAdvisor. Default is AgentDecisionDisabled - no mode is
// ever auto-activated; an operator must explicitly configure shadow or
// advisory.
type AgentDecisionMode string

const (
	// AgentDecisionDisabled: the advisor is never called. Scheduler's
	// existing, unmodified deterministic capability-based routing
	// (routeAgent) is the only selection mechanism - identical behavior
	// to Phases 1-6.
	AgentDecisionDisabled AgentDecisionMode = "disabled"
	// AgentDecisionShadow: the advisor IS called and its recommendation
	// is recorded, but Scheduler still dispatches to routeAgent's
	// deterministic pick. The advisor has zero effect on production
	// selection in this mode - it exists purely to observe what the
	// advisor WOULD have recommended, for offline evaluation.
	AgentDecisionShadow AgentDecisionMode = "shadow"
	// AgentDecisionAdvisory: if the advisor's top recommendation passes
	// Core's full re-validation (still eligible, still capability-
	// compatible, still tenant-compatible, still authorized - the same
	// TOCTOU re-checks Applier/Executor already perform for ANY
	// assignment), Scheduler proposes that agent instead of routeAgent's
	// pick. If the recommendation fails re-validation, or the advisor
	// fails/returns nothing usable, Scheduler falls back to routeAgent's
	// deterministic pick with an explicit, recorded reason - never
	// silently.
	AgentDecisionAdvisory AgentDecisionMode = "advisory"
)

// AgentCandidate is one capability-ELIGIBLE agent Scheduler is
// considering for a task - the advisor only ever sees agents that
// already passed capability eligibility; it cannot see, and therefore
// cannot recommend, an ineligible agent.
type AgentCandidate struct {
	AgentID      AgentID
	Capabilities []string
}

// ConfidenceBand is a qualitative, rule-based confidence level - never a
// fabricated statistical probability. See internal/performance's
// DeterministicAdvisor for the concrete rule that assigns one.
type ConfidenceBand string

const (
	ConfidenceInsufficient ConfidenceBand = "insufficient"
	ConfidenceLow          ConfidenceBand = "low"
	ConfidenceModerate     ConfidenceBand = "moderate"
	ConfidenceStrong       ConfidenceBand = "strong"
)

// AgentRecommendation is one ranked candidate with bounded, auditable
// reasons - never an opaque composite score and never raw chain-of-
// thought. Reasons are short strings describing the observable
// components the ranking was based on (e.g. "37 finalized
// security_review observations", "30 verified, 2 rejected").
type AgentRecommendation struct {
	AgentID    AgentID
	Confidence ConfidenceBand
	Reasons    []string
}

// AdviceBasis records which deterministic policy produced an AgentAdvice
// - versioned explicitly so historical agent selections remain
// explainable even after the policy's rules change later.
type AdviceBasis string

// AgentAdvice is the advisor's untrusted-for-authority output: a ranked
// list plus the policy basis that produced it. Core treats this exactly
// like Phases 1-6 treat any other advisory/proposal output - informative
// only, never applied without independent re-validation.
type AgentAdvice struct {
	Ranked []AgentRecommendation
	Basis  AdviceBasis
}

// AgentDecisionAdvisor ranks already-eligible candidates for a task -
// it never grants eligibility, never selects a model, and never mutates
// anything. Scheduler remains the sole authority over what actually gets
// proposed and, through the unmodified Applier/Executor, over what
// actually executes.
type AgentDecisionAdvisor interface {
	Advise(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error)
}
