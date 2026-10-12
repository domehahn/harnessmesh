package performance

import "testing"

// TestCoverage_MissingEvaluationsBlockPromotion is ADR 0009 Part 24's
// exact scenario: 100 evaluation-eligible decisions, 95 fully captured,
// 5 missing -> coverage incomplete -> promotion gate must not report
// manual_promotion_eligible.
func TestCoverage_MissingEvaluationsBlockPromotion(t *testing.T) {
	var decisions []DecisionCoverageInput
	for i := 0; i < 95; i++ {
		decisions = append(decisions, DecisionCoverageInput{ExpectedChallengers: 1, CapturedChallengers: 1})
	}
	for i := 0; i < 5; i++ {
		decisions = append(decisions, DecisionCoverageInput{ExpectedChallengers: 1, CapturedChallengers: 0})
	}
	coverage := ComputeEvaluationCoverage(decisions)
	if coverage.Complete() {
		t.Fatalf("expected incomplete coverage with 5 missing decisions")
	}

	challenger := RoutingPolicyDescriptor{ID: "P", Version: "v1"}
	summary := SummarizeChallenger(recordsFor(challenger, 95, StatusSameRecommendation), challenger)
	result := EvaluatePromotionGateWithCoverage(coverage, summary, 100, DefaultPromotionGateConfig())
	if result != PromotionCoverageIncomplete {
		t.Fatalf("expected evaluation_coverage_incomplete, got %q", result)
	}
}

func TestCoverage_FullCoverageAllowsPromotionGateToProceed(t *testing.T) {
	var decisions []DecisionCoverageInput
	for i := 0; i < 100; i++ {
		decisions = append(decisions, DecisionCoverageInput{ExpectedChallengers: 1, CapturedChallengers: 1})
	}
	coverage := ComputeEvaluationCoverage(decisions)
	if !coverage.Complete() {
		t.Fatalf("expected complete coverage with 100/100 captured")
	}

	challenger := RoutingPolicyDescriptor{ID: "P", Version: "v1"}
	summary := SummarizeChallenger(recordsFor(challenger, 100, StatusSameRecommendation), challenger)
	result := EvaluatePromotionGateWithCoverage(coverage, summary, 100, DefaultPromotionGateConfig())
	if result != PromotionManualEligible {
		t.Fatalf("expected manual_promotion_eligible with full coverage and clean metrics, got %q", result)
	}
}

// TestNegative_CoverageCannotBeGamedByHidingFailures proves a
// challenger that causes repeated evaluation failures cannot improve
// its APPARENT metrics by making the bad cases vanish: the missing
// decisions remain visible in coverage counters regardless of how clean
// the CAPTURED subset's ChallengerSummary looks.
func TestNegative_CoverageCannotBeGamedByHidingFailures(t *testing.T) {
	// 10 decisions total; 9 fail to capture at all (vanish), 1 captured
	// with a perfect same_recommendation record.
	var decisions []DecisionCoverageInput
	decisions = append(decisions, DecisionCoverageInput{ExpectedChallengers: 1, CapturedChallengers: 1})
	for i := 0; i < 9; i++ {
		decisions = append(decisions, DecisionCoverageInput{ExpectedChallengers: 1, CapturedChallengers: 0})
	}
	coverage := ComputeEvaluationCoverage(decisions)

	challenger := RoutingPolicyDescriptor{ID: "P", Version: "v1"}
	// The CAPTURED subset looks perfect (1/1 same_recommendation).
	summary := SummarizeChallenger(recordsFor(challenger, 1, StatusSameRecommendation), challenger)
	result := EvaluatePromotionGateWithCoverage(coverage, summary, 100, DefaultPromotionGateConfig())
	if result == PromotionManualEligible {
		t.Fatalf("expected the 9 missing decisions to block promotion despite a perfect-looking captured subset, got %q", result)
	}
	if coverage.MissingDecisionCount != 9 {
		t.Fatalf("expected the 9 missing decisions to remain visible in coverage, got %d", coverage.MissingDecisionCount)
	}
}

func TestCoverage_PartialVsMissingDistinguished(t *testing.T) {
	coverage := ComputeEvaluationCoverage([]DecisionCoverageInput{
		{ExpectedChallengers: 3, CapturedChallengers: 3}, // full
		{ExpectedChallengers: 3, CapturedChallengers: 1}, // partial
		{ExpectedChallengers: 3, CapturedChallengers: 0}, // missing
		{ExpectedChallengers: 0, CapturedChallengers: 0}, // no challengers configured - trivially full
	})
	if coverage.FullyCapturedDecisionCount != 2 {
		t.Fatalf("expected 2 fully captured, got %d", coverage.FullyCapturedDecisionCount)
	}
	if coverage.PartiallyCapturedDecisionCount != 1 {
		t.Fatalf("expected 1 partially captured, got %d", coverage.PartiallyCapturedDecisionCount)
	}
	if coverage.MissingDecisionCount != 1 {
		t.Fatalf("expected 1 missing, got %d", coverage.MissingDecisionCount)
	}
	if coverage.ExpectedChallengerEvaluations != 9 || coverage.CapturedChallengerEvaluations != 4 {
		t.Fatalf("expected 9 expected / 4 captured challenger-record counts, got %d/%d", coverage.ExpectedChallengerEvaluations, coverage.CapturedChallengerEvaluations)
	}
}
