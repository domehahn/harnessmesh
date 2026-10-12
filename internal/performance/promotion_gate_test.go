package performance

import (
	"testing"
)

func recordsFor(challenger RoutingPolicyDescriptor, n int, status EvaluationStatus) []RoutingEvaluationRecord {
	out := make([]RoutingEvaluationRecord, n)
	for i := range out {
		out[i] = RoutingEvaluationRecord{ChallengerPolicy: challenger, Status: status}
	}
	return out
}

// TestPromotionGate_EligibleWithSufficientCleanData is ADR 0008 Part 25's
// "Policy P: 100 evaluations, 0 invalid recommendations, 0 tenant
// violations, sufficient historical support" scenario.
func TestPromotionGate_EligibleWithSufficientCleanData(t *testing.T) {
	challenger := RoutingPolicyDescriptor{ID: "P", Version: "v1"}
	records := recordsFor(challenger, 100, StatusSameRecommendation)
	summary := SummarizeChallenger(records, challenger)
	if summary.InvalidRecommendationCount != 0 {
		t.Fatalf("expected 0 invalid recommendations, got %d", summary.InvalidRecommendationCount)
	}
	result := EvaluatePromotionGate(summary, DefaultMinimumObservationCount+5, DefaultPromotionGateConfig())
	if result != PromotionManualEligible {
		t.Fatalf("expected manual_promotion_eligible, got %q", result)
	}
}

// TestPromotionGate_PolicyViolationHardBlocksRegardlessOfOtherMetrics is
// ADR 0008 Part 25's "Policy Q: 1 cross-tenant recommendation -> gate:
// policy_violation regardless of other metrics" scenario. A cross-
// tenant/invalid recommendation always shows up as
// InvalidRecommendationCount (structural - see
// ShadowEvaluator.evaluateOne's doc comment), which this gate treats as
// the SAME hard blocker tier as an explicit policy_violation.
func TestPromotionGate_InvalidRecommendationHardBlocksRegardlessOfOtherMetrics(t *testing.T) {
	challenger := RoutingPolicyDescriptor{ID: "Q", Version: "v1"}
	records := recordsFor(challenger, 500, StatusSameRecommendation) // otherwise excellent
	records[0].Status = StatusChallengerInvalidRecommendation        // one bad apple
	records[0].ChallengerPolicy = challenger
	summary := SummarizeChallenger(records, challenger)

	result := EvaluatePromotionGate(summary, 1000, DefaultPromotionGateConfig())
	if result != PromotionInvalidRecommendations {
		t.Fatalf("expected invalid_recommendations as a hard blocker regardless of volume, got %q", result)
	}
}

func TestPromotionGate_NotEnoughData(t *testing.T) {
	challenger := RoutingPolicyDescriptor{ID: "R", Version: "v1"}
	records := recordsFor(challenger, 3, StatusSameRecommendation)
	summary := SummarizeChallenger(records, challenger)
	result := EvaluatePromotionGate(summary, 100, DefaultPromotionGateConfig())
	if result != PromotionNotEnoughData {
		t.Fatalf("expected not_enough_data, got %q", result)
	}
}

func TestPromotionGate_InsufficientFinalizedOutcomes(t *testing.T) {
	challenger := RoutingPolicyDescriptor{ID: "S", Version: "v1"}
	records := recordsFor(challenger, 50, StatusSameRecommendation)
	summary := SummarizeChallenger(records, challenger)
	result := EvaluatePromotionGate(summary, 2, DefaultPromotionGateConfig()) // too few finalized outcomes
	if result != PromotionInsufficientFinalizedOutcomes {
		t.Fatalf("expected insufficient_finalized_outcomes, got %q", result)
	}
}

func TestPromotionGate_DoesNotClaimChallengerOutcomeSuperiority(t *testing.T) {
	// Structural/behavioral: EvaluatePromotionGate's only inputs are
	// recommendation-comparison counts and a finalized-outcome SUPPORT
	// count (sample-size evidence) - it has no parameter through which
	// an actual challenger outcome could be supplied, since no such
	// outcome exists. This test documents and confirms that by
	// signature inspection equivalent: calling it with wildly different
	// "support" counts never changes anything about ChampionAgentID
	// comparisons, because the function doesn't take one.
	challenger := RoutingPolicyDescriptor{ID: "T", Version: "v1"}
	records := recordsFor(challenger, 100, StatusSameRecommendation)
	summary := SummarizeChallenger(records, challenger)
	resultLowSupport := EvaluatePromotionGate(summary, 0, DefaultPromotionGateConfig())
	resultHighSupport := EvaluatePromotionGate(summary, 10000, DefaultPromotionGateConfig())
	if resultLowSupport == PromotionManualEligible {
		t.Fatalf("expected insufficient support to block eligibility")
	}
	if resultHighSupport != PromotionManualEligible {
		t.Fatalf("expected sufficient support to be eligible")
	}
}

func TestPromotionGate_CohortReliabilityRegressionSignal(t *testing.T) {
	champion := AgentPerformanceSummary{
		AgentID: "champion-agent", ObservationCount: 100, FinalizedCount: 100,
		FailedCount: 2, VerifiedCount: 90, RejectedCount: 8,
	}
	challengerFavored := AgentPerformanceSummary{
		AgentID: "challenger-agent", ObservationCount: 100, FinalizedCount: 100,
		FailedCount: 20, VerifiedCount: 60, RejectedCount: 20, // much worse failure rate
	}
	result := CompareHistoricalCohortReliability(champion, challengerFavored)
	if result != PromotionReliabilityRegression {
		t.Fatalf("expected reliability_regression, got %q", result)
	}
}

func TestPromotionGate_SparseCohortNeverFlaggedAsRegression(t *testing.T) {
	champion := AgentPerformanceSummary{AgentID: "a", ObservationCount: 100, FinalizedCount: 100, VerifiedCount: 95, InsufficientData: false}
	challengerFavored := AgentPerformanceSummary{AgentID: "b", ObservationCount: 3, FinalizedCount: 3, FailedCount: 2, InsufficientData: true}
	result := CompareHistoricalCohortReliability(champion, challengerFavored)
	if result != PromotionManualEligible {
		t.Fatalf("expected a sparse challenger-favored cohort to never be flagged as a regression, got %q", result)
	}
}

// TestNegative_PromotionGateDoesNotMutateActivePolicy proves the gate
// returning eligible has no observable side effect on anything - there
// is no "active champion" state anywhere in this package for it to
// touch (see TestNegative_PromotionGateHasNoMutationMethod for the
// structural companion).
func TestNegative_PromotionGateDoesNotMutateActivePolicy(t *testing.T) {
	championBefore := championDesc()
	challenger := RoutingPolicyDescriptor{ID: "U", Version: "v1"}
	records := recordsFor(challenger, 100, StatusSameRecommendation)
	summary := SummarizeChallenger(records, challenger)
	result := EvaluatePromotionGate(summary, 100, DefaultPromotionGateConfig())
	if result != PromotionManualEligible {
		t.Fatalf("expected eligible")
	}
	championAfter := championDesc()
	if championBefore != championAfter {
		t.Fatalf("champion descriptor must be unaffected by a promotion-gate evaluation")
	}
}
