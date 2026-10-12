package review

// DefaultActionCost assigns a fixed, deterministic cost unit per
// ArbiterActionType. This is NOT a real provider/monetary cost figure -
// Phase 5 explicitly allows a deterministic bounded cost model when real
// synchronous provider costs are unavailable, documented here rather
// than implying false monetary precision. RequestIndependentReview costs
// more than the others because it is the action that causes real
// additional agent work (a whole new scheduled task), not just an
// in-memory bookkeeping step.
func DefaultActionCost(t ArbiterActionType) float64 {
	switch t {
	case ActionRequestIndependentReview:
		return 3
	case ActionRequestEvidence:
		return 1
	case ActionRecommendVerification, ActionRejectClaim:
		return 1
	default: // SupportClaim/ContestClaim - audit-only, cheapest
		return 0.5
	}
}

// countDistinctAgents returns the number of distinct SourceAgentIDs
// across a group's observations - the basis for enforcing
// MaxIndependentReviews (Core-owned: bounds how many separate agents may
// ever contribute to one claim, regardless of what an Arbiter proposes).
func countDistinctAgents(group ReviewGroup) int {
	seen := map[string]bool{}
	for _, o := range group.Observations {
		seen[string(o.SourceAgentID)] = true
	}
	return len(seen)
}
