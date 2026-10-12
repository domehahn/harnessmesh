package performance

// ChallengerSummary is a deterministic aggregation over a set of
// RoutingEvaluationRecords for ONE challenger policy - no composite
// "accuracy" score (ADR 0008's explicit "no accuracy without ground
// truth" instruction, since a Challenger never actually executes, there
// is no ground truth to be accurate against).
type ChallengerSummary struct {
	Policy RoutingPolicyDescriptor

	EvaluationCount int

	SameRecommendationCount      int
	DifferentRecommendationCount int

	InvalidRecommendationCount int
	ErrorCount                 int
	InsufficientDataCount      int

	// HistoricalSupportCount is the number of evaluations where the
	// challenger's recommended agent had sufficient historical data
	// (per DeterministicAdvisor's own ConfidenceInsufficient threshold)
	// for THIS task class at evaluation time - observational cohort
	// support, never a claim about actual outcome quality.
	HistoricalSupportCount int
}

// SummarizeChallenger aggregates records for one challenger policy.
// Records for OTHER challenger policies in the slice are ignored (a
// caller typically pre-filters via EvaluationStore.Query's
// ChallengerPolicyID, but SummarizeChallenger is defensive regardless).
func SummarizeChallenger(records []RoutingEvaluationRecord, challengerPolicy RoutingPolicyDescriptor) ChallengerSummary {
	summary := ChallengerSummary{Policy: challengerPolicy}
	for _, rec := range records {
		if rec.ChallengerPolicy != challengerPolicy {
			continue
		}
		summary.EvaluationCount++
		switch rec.Status {
		case StatusSameRecommendation:
			summary.SameRecommendationCount++
		case StatusDifferentRecommendation:
			summary.DifferentRecommendationCount++
		case StatusChallengerInvalidRecommendation:
			summary.InvalidRecommendationCount++
		case StatusChallengerError:
			summary.ErrorCount++
		case StatusChallengerInsufficientData:
			summary.InsufficientDataCount++
		}
	}
	return summary
}

// PromotionGateResult answers only "is this policy eligible for
// human/manual consideration" - it is never activation, and nothing in
// this file (or anywhere in this package) has a Promote/
// ActivateChallenger/SetChampion method. The active Champion is
// configuration state this package does not own, read, or write.
type PromotionGateResult string

const (
	PromotionManualEligible                PromotionGateResult = "manual_promotion_eligible"
	PromotionNotEnoughData                 PromotionGateResult = "not_enough_data"
	PromotionInvalidRecommendations        PromotionGateResult = "invalid_recommendations"
	PromotionPolicyViolation               PromotionGateResult = "policy_violation"
	PromotionTenantViolation               PromotionGateResult = "tenant_violation"
	PromotionReliabilityRegression         PromotionGateResult = "reliability_regression"
	PromotionQualityRegression             PromotionGateResult = "quality_regression"
	PromotionInsufficientFinalizedOutcomes PromotionGateResult = "insufficient_finalized_outcomes"
)

// PromotionGateConfig is Core-configured (never Challenger-steerable).
// Reuses DefaultMinimumObservationCount (ADR 0005) as the default for
// both thresholds, rather than inventing new magic numbers - an
// operator wanting different thresholds sets them explicitly.
type PromotionGateConfig struct {
	MinimumEvaluations       int
	MinimumFinalizedOutcomes int
}

func DefaultPromotionGateConfig() PromotionGateConfig {
	return PromotionGateConfig{
		MinimumEvaluations:       DefaultMinimumObservationCount,
		MinimumFinalizedOutcomes: DefaultMinimumObservationCount,
	}
}

func (c PromotionGateConfig) minimumEvaluations() int {
	if c.MinimumEvaluations > 0 {
		return c.MinimumEvaluations
	}
	return DefaultMinimumObservationCount
}
func (c PromotionGateConfig) minimumFinalizedOutcomes() int {
	if c.MinimumFinalizedOutcomes > 0 {
		return c.MinimumFinalizedOutcomes
	}
	return DefaultMinimumObservationCount
}

// EvaluatePromotionGate checks hard blockers FIRST (invalid
// recommendations - which structurally subsumes any cross-tenant or
// policy/authorization-bypass attempt, since the Challenger has no
// other way to express either than via an invalid recommendation - see
// ShadowEvaluator.evaluateOne's doc comment), then minimum-evidence
// gates, then returns eligible. It NEVER returns a result implying the
// Challenger produced better outcomes - see
// CompareHistoricalCohortReliability for the separate, explicitly
// bounded reliability/quality signal, which is itself observational,
// never causal proof.
func EvaluatePromotionGate(summary ChallengerSummary, finalizedOutcomeSupportCount int, cfg PromotionGateConfig) PromotionGateResult {
	if summary.InvalidRecommendationCount > 0 {
		return PromotionInvalidRecommendations
	}
	if summary.EvaluationCount < cfg.minimumEvaluations() {
		return PromotionNotEnoughData
	}
	if finalizedOutcomeSupportCount < cfg.minimumFinalizedOutcomes() {
		return PromotionInsufficientFinalizedOutcomes
	}
	return PromotionManualEligible
}

// CompareHistoricalCohortReliability is a SEPARATE, explicitly bounded
// signal (not folded into EvaluatePromotionGate's hard-blocker logic,
// since the Challenger produced no real execution to be unreliable
// with) - it compares the CHAMPION's actual historical reliability
// (failure+timeout rate over finalized runs) against the historical
// reliability of the agent the Challenger MOST OFTEN recommended, using
// ordinary Aggregate (ADR 0005) over each agent's own task-class cohort.
// Returns PromotionReliabilityRegression/PromotionQualityRegression
// only when BOTH cohorts have sufficient data AND the challenger-favored
// agent's failure/rejection rate is markedly worse (more than double) -
// a conservative, documented threshold, never a claim that the
// Challenger itself "would" underperform (ADR 0008's explicit "real
// outcome superiority remains unproven" instruction). Returns
// PromotionManualEligible (i.e. "no regression signal") otherwise,
// including when either cohort is insufficient - a sparse comparison is
// never reported as a regression.
func CompareHistoricalCohortReliability(champion, challengerFavored AgentPerformanceSummary) PromotionGateResult {
	if champion.InsufficientData || challengerFavored.InsufficientData {
		return PromotionManualEligible
	}
	championFailureRate := ratio(champion.FailedCount+champion.TimedOutCount, champion.ObservationCount)
	challengerFailureRate := ratio(challengerFavored.FailedCount+challengerFavored.TimedOutCount, challengerFavored.ObservationCount)
	if championFailureRate > 0 && challengerFailureRate > 2*championFailureRate {
		return PromotionReliabilityRegression
	}

	championRejectRate := ratio(champion.RejectedCount, champion.FinalizedCount)
	challengerRejectRate := ratio(challengerFavored.RejectedCount, challengerFavored.FinalizedCount)
	if championRejectRate > 0 && challengerRejectRate > 2*championRejectRate {
		return PromotionQualityRegression
	}
	return PromotionManualEligible
}
