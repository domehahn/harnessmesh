package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// DecisionID uniquely identifies one production agent-selection
// decision event - distinct from TaskID (which identifies the task
// across every decision/attempt) and from ExecutionID (which identifies
// the subsequent execution attempt that decision led to). Core-owned:
// generated only by Scheduler.selectAgent itself, once per decision,
// never derived from or trusted from agent/advisor output. A retry
// (a later Tick re-selecting for the same TaskID) always generates a
// NEW DecisionID - ADR 0009's explicit "one production selection event
// -> one stable decision identity" requirement.
type DecisionID string

// NewDecisionID generates a Core-owned, unique decision identifier,
// mirroring NewExecutionID's crypto/rand-based pattern exactly (ADR
// 0006 Phase 7C) - uniqueness never depends on wall-clock time alone.
func NewDecisionID(taskID TaskID) DecisionID {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return DecisionID(fmt.Sprintf("%s@%d", taskID, time.Now().UnixNano()))
	}
	return DecisionID(fmt.Sprintf("%s#%s", taskID, hex.EncodeToString(b)))
}

// LegacyDeterministicPolicyID/-Version are the Core-owned, explicit,
// versioned identity of Scheduler's own deterministic capability-based
// fallback (routeAgent/the lexicographically-smallest-eligible-agent
// rule) - ADR 0009's explicit instruction that this real routing policy
// must never be captured as an empty/unnamed ChampionPolicy for an
// evaluation-eligible decision.
const (
	LegacyDeterministicPolicyID      = "legacy-deterministic"
	LegacyDeterministicPolicyVersion = "v1"
)

// PolicyProvenance is the Core-owned record of WHICH policy actually
// produced a production Champion decision, captured at the moment of
// selection - never reconstructed later from current configuration. If
// an Advisor was attempted but a fallback was actually used (error,
// insufficient data, or a TOCTOU-ineligible top pick), AttemptedPolicyID/
// -Version record what was tried, while PolicyID/Version always name
// the mechanism that ACTUALLY produced the returned AgentID - "advisor
// attempted, advisor failed, legacy fallback selected Agent A" means
// PolicyID/Version here are the legacy fallback's, not the advisor's.
type PolicyProvenance struct {
	PolicyID      string
	PolicyVersion string

	AttemptedPolicyID      string
	AttemptedPolicyVersion string
	FallbackUsed           bool
}

// PolicyIdentifiable is an OPTIONAL interface an AgentDecisionAdvisor
// implementation may satisfy to let Scheduler capture its real policy
// ID/Version separately (rather than only the single combined
// AdviceBasis string AgentAdvice already carries). Not required - an
// advisor that doesn't implement this still gets its AdviceBasis
// captured as PolicyID with an empty PolicyVersion, which is strictly
// better than capturing nothing.
type PolicyIdentifiable interface {
	PolicyID() string
	PolicyVersion() string
}

// RoutingDecisionContext is the Core-owned, immutable record of exactly
// which candidates were eligible and which policy/agent was actually
// selected for one production decision - ADR 0009's "exact authoritative
// decision context." CandidateAgentIDs is the FINAL eligible set (after
// tenant/capability/availability filtering - the same list routeAgent's
// fallback and any configured Advisor both already see), never widened
// or substituted by any caller.
type RoutingDecisionContext struct {
	DecisionID DecisionID
	TaskID     TaskID
	Tenant     string
	TaskClass  string // task.Type

	CandidateAgentIDs []AgentID
	CapturedAt        time.Time

	ChampionPolicy  PolicyProvenance
	ChampionAgentID AgentID
}

// ExperimentAdmission is a Core-owned pre-dispatch gate. Implementations
// may only return a member of the already Core-derived candidate set; errors
// and unavailable control state must leave the Champion selected.
type ExperimentAdmission interface {
	Admit(ctx context.Context, task Task, decisionID DecisionID, candidates []AgentCandidate, champion AgentID, policy PolicyProvenance) (AgentID, bool, error)
}

// RoutingDecisionObserver is an optional, Core-owned hook Scheduler
// calls after it has determined a production decision's AUTHORITATIVE
// outcome (candidate set + champion policy + champion agent) - never
// before, and never from anything an Advisor/Challenger claimed beyond
// what TOCTOU-revalidated eligibility already allowed. Its method
// returns nothing and must not be allowed to affect selectAgent's own
// return value - the same failure-isolation guarantee
// ExecutionObserver/ReviewFinalizationObserver already provide (ADR
// 0006/0007). nil is safe - Scheduler works identically with no
// observer attached (and this is still the DEFAULT, since
// EvaluationMode defaults to EvaluationDisabled).
type RoutingDecisionObserver interface {
	ObserveRoutingDecision(ctx context.Context, decision RoutingDecisionContext)
}

// RoutingDecisionObserverFailureSink mirrors ExecutionObserverFailureSink
// (ADR 0006 Phase 7C) for the routing-decision hook - a panicking
// RoutingDecisionObserver must never crash production routing, and that
// recovery must not be silent.
type RoutingDecisionObserverFailureSink interface {
	RecordRoutingDecisionObserverPanic()
}

// EvaluationMode controls whether/how Scheduler captures a production
// decision for the Phase 9/9C evaluation plane. Default is
// EvaluationDisabled - no mode is ever auto-activated.
type EvaluationMode string

const (
	// EvaluationDisabled: the RoutingDecisionObserver is never called.
	EvaluationDisabled EvaluationMode = "disabled"
	// EvaluationShadow: the RoutingDecisionObserver IS called and
	// captures the decision context (snapshot + champion policy/agent),
	// but this has zero effect on which agent is actually selected -
	// identical production behavior to every prior phase. There is no
	// "exploration" or "challenger execution" mode - see ADR 0009.
	EvaluationShadow EvaluationMode = "shadow"
)
