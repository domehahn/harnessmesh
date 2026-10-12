package performance

import "github.com/domehahn/harnessmesh/internal/supervisor"

// EvaluationCoverage is deterministic coverage accounting over a set of
// evaluation-eligible production decisions and the EvaluationRecords
// they actually produced (ADR 0009 Part 13). It distinguishes DECISION
// coverage from CHALLENGER-RECORD coverage explicitly, per the
// mandate's own instruction not to conflate the two semantics:
//
//   - EligibleDecisionCount: how many production decisions were
//     evaluation-eligible (EvaluationMode != disabled).
//   - FullyCapturedDecisionCount: decisions for which EVERY configured
//     challenger produced a record (ExpectedChallengerEvaluations ==
//     the number actually captured for that decision).
//   - PartiallyCapturedDecisionCount: decisions with at least one, but
//     not all, challenger records.
//   - MissingDecisionCount: decisions with zero challenger records at
//     all (e.g. snapshot capture failed, or Evaluate failed entirely).
//
// ExpectedChallengerEvaluations/CapturedChallengerEvaluations are the
// SEPARATE, finer-grained record-level coverage: "1 decision x 3
// configured challengers" implies 3 expected records for that decision
// - this is NOT the same number as EligibleDecisionCount.
type EvaluationCoverage struct {
	EligibleDecisionCount          int
	FullyCapturedDecisionCount     int
	PartiallyCapturedDecisionCount int
	MissingDecisionCount           int

	ExpectedChallengerEvaluations int
	CapturedChallengerEvaluations int
}

// Complete reports whether coverage shows no missing or partial
// decisions at all - the gate condition EvaluatePromotionGateWithCoverage
// checks before even considering EvaluatePromotionGate's own result.
func (c EvaluationCoverage) Complete() bool {
	return c.MissingDecisionCount == 0 && c.PartiallyCapturedDecisionCount == 0 &&
		c.CapturedChallengerEvaluations == c.ExpectedChallengerEvaluations
}

// DecisionCoverageInput is one DECISION's (ADR 0009D: DecisionID, never
// just TaskID) expected-vs-captured challenger-record count, used to
// compute EvaluationCoverage. A caller typically derives
// ExpectedChallengers from how many challengers were CONFIGURED at the
// time of that decision, and CapturedChallengers by counting actual
// RoutingEvaluationRecords found for it (e.g. via EvaluationStore.Query,
// grouping by RoutingEvaluationRecord.DecisionID - never by TaskID,
// since one task may have had several distinct decisions, each
// counted separately here).
type DecisionCoverageInput struct {
	DecisionID          supervisor.DecisionID // optional, for traceability/debugging only - not used in the arithmetic
	ExpectedChallengers int
	CapturedChallengers int
}

// ComputeEvaluationCoverage aggregates a set of per-decision counts
// into EvaluationCoverage - a pure function, no store access, so it can
// be reused equally for a live production report or an offline/replay
// audit.
func ComputeEvaluationCoverage(decisions []DecisionCoverageInput) EvaluationCoverage {
	var cov EvaluationCoverage
	for _, d := range decisions {
		cov.EligibleDecisionCount++
		cov.ExpectedChallengerEvaluations += d.ExpectedChallengers
		cov.CapturedChallengerEvaluations += d.CapturedChallengers

		switch {
		case d.ExpectedChallengers == 0:
			// A decision configured with zero challengers trivially has
			// full coverage (there was nothing to capture) - never
			// counted as missing/partial.
			cov.FullyCapturedDecisionCount++
		case d.CapturedChallengers == 0:
			cov.MissingDecisionCount++
		case d.CapturedChallengers < d.ExpectedChallengers:
			cov.PartiallyCapturedDecisionCount++
		default:
			cov.FullyCapturedDecisionCount++
		}
	}
	return cov
}

// PromotionCoverageIncomplete is the result EvaluatePromotionGateWithCoverage
// returns when evaluation coverage is incomplete - checked BEFORE
// EvaluatePromotionGate's own result, so missing data can never be
// silently treated as equivalent to complete data (ADR 0009's explicit
// "100 expected / 95 captured must not appear equivalent to 95
// expected / 95 captured" requirement).
const PromotionCoverageIncomplete PromotionGateResult = "evaluation_coverage_incomplete"

// EvaluatePromotionGateWithCoverage wraps EvaluatePromotionGate with a
// mandatory coverage check: if coverage is incomplete,
// PromotionCoverageIncomplete is returned regardless of how clean the
// captured subset's metrics look - a Challenger that causes evaluation
// failures cannot improve its apparent standing by making the bad
// cases simply vanish from the summary, since the coverage counters
// themselves remain visible and gate promotion before the summary is
// even considered.
func EvaluatePromotionGateWithCoverage(coverage EvaluationCoverage, summary ChallengerSummary, finalizedOutcomeSupportCount int, cfg PromotionGateConfig) PromotionGateResult {
	if !coverage.Complete() {
		return PromotionCoverageIncomplete
	}
	return EvaluatePromotionGate(summary, finalizedOutcomeSupportCount, cfg)
}
