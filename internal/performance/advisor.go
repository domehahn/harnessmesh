package performance

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
	"github.com/domehahn/harnessmesh/internal/telemetry"
)

// AdvisorQueryTelemetry records DeterministicAdvisor's own observability
// counters (advisor_insufficient_data, advisor_query_duration) - the
// Scheduler-level advisor_calls/advisor_failures/advisor_shadow_
// recommendations/advisor_advisory_selections/advisor_fallbacks counters
// live in internal/supervisor's AdvisorTelemetry instead, since those
// are routing-decision outcomes, not advisor-internal query behavior.
type AdvisorQueryTelemetry interface {
	RecordInsufficientData()
	RecordQueryDuration(d time.Duration)
}

type noopAdvisorQueryTelemetry struct{}

func (noopAdvisorQueryTelemetry) RecordInsufficientData()           {}
func (noopAdvisorQueryTelemetry) RecordQueryDuration(time.Duration) {}

var _ AdvisorQueryTelemetry = noopAdvisorQueryTelemetry{}

// RegistryAdvisorQueryTelemetry adapts telemetry.Registry to
// AdvisorQueryTelemetry.
type RegistryAdvisorQueryTelemetry struct{ Registry *telemetry.Registry }

func NewRegistryAdvisorQueryTelemetry(r *telemetry.Registry) *RegistryAdvisorQueryTelemetry {
	return &RegistryAdvisorQueryTelemetry{Registry: r}
}
func (t *RegistryAdvisorQueryTelemetry) RecordInsufficientData() {
	t.Registry.Counter("advisor_insufficient_data").Add(1)
}
func (t *RegistryAdvisorQueryTelemetry) RecordQueryDuration(d time.Duration) {
	t.Registry.Observe("advisor_query_duration", d)
}

var _ AdvisorQueryTelemetry = (*RegistryAdvisorQueryTelemetry)(nil)

// DefaultAdvisorPolicyVersion is DeterministicAdvisor's default
// AdviceBasis tag - versioned explicitly per ADR 0006's policy-
// versioning requirement, so a historical AdvisorySelection/
// ShadowRecommendation event always records exactly which rule set
// produced it, even after a later policy version changes the rules.
const DefaultAdvisorPolicyVersion = "performance-v1"

// DeterministicAdvisor implements supervisor.AgentDecisionAdvisor using
// only explainable, deterministic rules over this package's existing
// Aggregate (Phase 6) - no ML, no bandits, no learned weights. It never
// sees, and therefore can never recommend, a candidate outside the
// eligible list Scheduler already computed (supervisor.AgentCandidate).
//
// Ranking order (lexicographic, applied only within the "sufficient
// data" bucket - see Advise's cold-start handling):
//  1. higher verified/finalized ratio
//  2. lower rejected/finalized ratio
//  3. lower (failed+timed_out)/observed ratio
//  4. lower total rework count
//  5. lower median duration
//  6. deterministic AgentID tie-break
//
// This specific order is a documented policy choice (verified evidence
// first, then reliability, then rework burden, then latency last) - not
// a general law, and not combined into a single composite score.
type DeterministicAdvisor struct {
	Dataset DecisionDataset
	// Tenant is REQUIRED - this advisor only ever queries its own,
	// explicitly configured tenant's historical data. There is no
	// "all tenants" mode for advice (unlike raw Query/Aggregate, which
	// do support an explicit AllTenants opt-in for operator tooling) -
	// Phase 7B's own instruction is that advisor ranking is tenant-
	// scoped with no exception.
	Tenant string
	// MinimumObservationCount defaults to DefaultMinimumObservationCount
	// if zero.
	MinimumObservationCount int
	// ConfiguredPolicyVersion defaults to DefaultAdvisorPolicyVersion if
	// empty. Renamed from the earlier "PolicyVersion" field name (ADR
	// 0009) to free that identifier for the PolicyVersion() METHOD
	// below, which implements supervisor.PolicyIdentifiable.
	ConfiguredPolicyVersion string
	// Telemetry is optional (defaults to a no-op).
	Telemetry AdvisorQueryTelemetry
	// IncludePairEvidence, if true, appends at most one additional,
	// bounded, observational reason per sufficient-data candidate
	// describing its historically most-used route (ADR 0007's "Agent
	// Advisor enhancement") - never a model-selection statement. Default
	// false: Phase 8 adds this as an explicit opt-in, not a default
	// behavior change to Phase 7B's advisor.
	IncludePairEvidence bool
}

func (a *DeterministicAdvisor) telemetry() AdvisorQueryTelemetry {
	if a.Telemetry != nil {
		return a.Telemetry
	}
	return noopAdvisorQueryTelemetry{}
}

func (a *DeterministicAdvisor) minObs() int {
	if a.MinimumObservationCount > 0 {
		return a.MinimumObservationCount
	}
	return DefaultMinimumObservationCount
}

func (a *DeterministicAdvisor) policyVersion() string {
	if a.ConfiguredPolicyVersion != "" {
		return a.ConfiguredPolicyVersion
	}
	return DefaultAdvisorPolicyVersion
}

// PolicyID/PolicyVersion implement supervisor.PolicyIdentifiable (ADR
// 0009), letting Scheduler.selectAgent capture this advisor's real
// policy identity at decision time instead of only the single combined
// AdviceBasis string. PolicyID is fixed ("performance" - this advisor's
// family); PolicyVersion is whatever policyVersion() resolves to
// (a.ConfiguredPolicyVersion, or DefaultAdvisorPolicyVersion).
func (a *DeterministicAdvisor) PolicyID() string      { return "performance" }
func (a *DeterministicAdvisor) PolicyVersion() string { return a.policyVersion() }

var _ supervisor.PolicyIdentifiable = (*DeterministicAdvisor)(nil)

// Advise never returns an ineligible candidate (it only ever ranks
// members of the `candidates` slice it was given) and never returns an
// error for "no sufficient data" - that is the cold-start case, handled
// by returning an advice whose Ranked either marks every candidate
// insufficient or is empty, both of which Scheduler treats as "use the
// deterministic fallback."
func (a *DeterministicAdvisor) Advise(ctx context.Context, task supervisor.Task, candidates []supervisor.AgentCandidate) (supervisor.AgentAdvice, error) {
	if a.Tenant == "" {
		return supervisor.AgentAdvice{}, fmt.Errorf("advisor requires an explicit tenant scope")
	}
	if task.Tenant != "" && task.Tenant != a.Tenant {
		return supervisor.AgentAdvice{}, fmt.Errorf("task tenant %q does not match advisor tenant scope %q - refusing cross-tenant advice", task.Tenant, a.Tenant)
	}
	taskClass := TaskClass(task.Type)
	if !KnownTaskClass(taskClass) {
		return supervisor.AgentAdvice{}, fmt.Errorf("unknown task class %q - cannot compare performance without a task class", task.Type)
	}
	if len(candidates) == 0 {
		return supervisor.AgentAdvice{}, nil
	}

	queryStart := time.Now()
	defer func() { a.telemetry().RecordQueryDuration(time.Since(queryStart)) }()

	type scored struct {
		AgentID supervisor.AgentID
		Summary AgentPerformanceSummary
	}
	var sufficient, coldStart []scored
	for _, c := range candidates {
		summary, err := Aggregate(ctx, a.Dataset, ObservationFilter{Tenant: a.Tenant}, c.AgentID, taskClass)
		if err != nil {
			// Treat an aggregation error for one candidate as cold-start
			// rather than failing the whole advice - a single data
			// problem must not silently remove an otherwise-eligible
			// agent from consideration.
			a.telemetry().RecordInsufficientData()
			coldStart = append(coldStart, scored{AgentID: c.AgentID, Summary: AgentPerformanceSummary{AgentID: c.AgentID, TaskClass: taskClass, InsufficientData: true}})
			continue
		}
		if summary.InsufficientData {
			a.telemetry().RecordInsufficientData()
			coldStart = append(coldStart, scored{AgentID: c.AgentID, Summary: summary})
		} else {
			sufficient = append(sufficient, scored{AgentID: c.AgentID, Summary: summary})
		}
	}

	// Cold-start policy: if NO candidate has sufficient data, return no
	// ranking at all - Scheduler's existing deterministic fallback
	// (routeAgent) is the right answer here, not a guess dressed up as
	// a recommendation.
	if len(sufficient) == 0 {
		return supervisor.AgentAdvice{Ranked: nil, Basis: "all_cold_start_fallback"}, nil
	}

	sort.Slice(sufficient, func(i, j int) bool {
		si, sj := sufficient[i].Summary, sufficient[j].Summary
		if ri, rj := ratio(si.VerifiedCount, si.FinalizedCount), ratio(sj.VerifiedCount, sj.FinalizedCount); ri != rj {
			return ri > rj // higher verified ratio wins
		}
		if ri, rj := ratio(si.RejectedCount, si.FinalizedCount), ratio(sj.RejectedCount, sj.FinalizedCount); ri != rj {
			return ri < rj // lower rejected ratio wins
		}
		if ri, rj := ratio(si.FailedCount+si.TimedOutCount, si.ObservationCount), ratio(sj.FailedCount+sj.TimedOutCount, sj.ObservationCount); ri != rj {
			return ri < rj // lower failure/timeout frequency wins
		}
		if si.TotalReworkCount != sj.TotalReworkCount {
			return si.TotalReworkCount < sj.TotalReworkCount // less rework wins
		}
		if si.MedianDuration != sj.MedianDuration {
			return si.MedianDuration < sj.MedianDuration // faster wins
		}
		return sufficient[i].AgentID < sufficient[j].AgentID // deterministic tie-break
	})
	sort.Slice(coldStart, func(i, j int) bool { return coldStart[i].AgentID < coldStart[j].AgentID })

	var ranked []supervisor.AgentRecommendation
	for _, s := range sufficient {
		reasons := reasonsFor(s.Summary)
		if a.IncludePairEvidence {
			if extra := pairEvidenceReason(ctx, a.Dataset, a.Tenant, s.AgentID, taskClass, a.minObs()); extra != "" {
				reasons = append(reasons, extra)
			}
		}
		ranked = append(ranked, supervisor.AgentRecommendation{
			AgentID:    s.AgentID,
			Confidence: confidenceFor(s.Summary, a.minObs()),
			Reasons:    reasons,
		})
	}
	for _, s := range coldStart {
		// Cold-start candidates are appended, never penalized into a
		// "bad" position by a fabricated low score - they are simply
		// unranked relative to the sufficient-data candidates, marked
		// explicitly unknown.
		ranked = append(ranked, supervisor.AgentRecommendation{
			AgentID:    s.AgentID,
			Confidence: supervisor.ConfidenceInsufficient,
			Reasons:    []string{fmt.Sprintf("insufficient historical data (%d observations) - cold start, not penalized", s.Summary.ObservationCount)},
		})
	}

	return supervisor.AgentAdvice{Ranked: ranked, Basis: supervisor.AdviceBasis(a.policyVersion())}, nil
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

// confidenceFor assigns a qualitative band from clear, stated rules -
// never a fabricated statistical probability. "Consistency" here means
// the dominant quality outcome (verified or rejected) as a fraction of
// finalized observations - a high, one-sided fraction with a large
// enough sample is "strong"; a merely-sufficient sample is "moderate";
// a sample below the finalized-count threshold is "low" (distinct from
// "insufficient", which is reserved for candidates below the overall
// observation-count minimum entirely).
func confidenceFor(s AgentPerformanceSummary, minObs int) supervisor.ConfidenceBand {
	if s.FinalizedCount < minObs {
		return supervisor.ConfidenceLow
	}
	dominant := s.VerifiedCount
	if s.RejectedCount > dominant {
		dominant = s.RejectedCount
	}
	consistency := ratio(dominant, s.FinalizedCount)
	if s.FinalizedCount >= 2*minObs && consistency >= 0.8 {
		return supervisor.ConfidenceStrong
	}
	return supervisor.ConfidenceModerate
}

// reasonsFor produces bounded, structured, auditable reasons - never raw
// chain-of-thought, never an opaque score.
func reasonsFor(s AgentPerformanceSummary) []string {
	return []string{
		fmt.Sprintf("%d finalized %s observations (n=%d total)", s.FinalizedCount, s.TaskClass, s.ObservationCount),
		fmt.Sprintf("%d verified, %d rejected, %d contested, %d needs_evidence", s.VerifiedCount, s.RejectedCount, s.ContestedCount, s.NeedsEvidenceCount),
		fmt.Sprintf("%d failed, %d timed out", s.FailedCount, s.TimedOutCount),
		fmt.Sprintf("median duration %s, total rework %d", s.MedianDuration, s.TotalReworkCount),
	}
}

// pairEvidenceReason returns at most one bounded, observational sentence
// about the agent's historically most-used route/model for this task
// class - ADR 0007's "Agent Advisor enhancement": the advisor remains an
// AGENT advisor; this sentence never says "select Model X," only what
// was historically observed, and only when the PAIR-level cohort itself
// has sufficient data (reusing the same DefaultMinimumObservationCount
// threshold as everywhere else - no separate pair-specific config).
// Returns "" if there is no model-usage history, or if the pair cohort
// for the most-used route is itself insufficient - a sparse pair must
// never be presented as if it were strong evidence.
func pairEvidenceReason(ctx context.Context, dataset DecisionDataset, tenant string, agentID supervisor.AgentID, taskClass TaskClass, minObs int) string {
	usages, err := AggregateModelUsage(ctx, dataset, ObservationFilter{Tenant: tenant, AgentID: agentID, TaskClass: taskClass})
	if err != nil || len(usages) == 0 {
		return ""
	}
	top := usages[0]
	for _, u := range usages {
		if u.Count > top.Count {
			top = u
		}
	}

	pair, err := AggregatePair(ctx, dataset, ObservationFilter{Tenant: tenant}, agentID, taskClass, top.RouteID, "", top.Model)
	if err != nil || pair.ObservationCount < minObs {
		// Respect the advisor's OWN configured minimum (which may differ
		// from DefaultMinimumObservationCount), not just AggregatePair's
		// hardcoded InsufficientData flag - a sparse pair must never be
		// presented as strong evidence regardless of which threshold is
		// in effect.
		return ""
	}
	return fmt.Sprintf(
		"observed evidence: %d of %d finalized runs involved route %q (%d verified, %d rejected) - observational, not a model selection",
		pair.ObservationCount, pair.FinalizedCount, top.RouteID, pair.VerifiedCount, pair.RejectedCount,
	)
}

var _ supervisor.AgentDecisionAdvisor = (*DeterministicAdvisor)(nil)
