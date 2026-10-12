package performance

import (
	"context"
	"sort"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// PairPerformanceSummary is a deterministic aggregation over one
// (AgentID, TaskClass, Route/Provider/Model) cohort - never a single
// composite score, exactly like AgentPerformanceSummary (ADR 0005).
// Tenant is deliberately NOT a field here: it is a QUERY-TIME scope
// (ObservationFilter.Tenant), consistent with AgentPerformanceSummary,
// which carries no Tenant field either - the repository has no TenantID
// type (tenant is always a plain string filter), so this intentionally
// deviates from ADR 0007's own Go sketch (which used `Tenant TenantID`)
// rather than inventing a type that does not otherwise exist anywhere in
// the codebase.
//
// AgentID == "" denotes an agent-agnostic, model/route-level summary
// (every agent that used this route/model combination, within the task
// class) - the cohort "Which Agent+Model combinations were observed" is
// answered by calling AggregatePair once per candidate agent (via
// existing agent enumeration), not by this package building a full
// cross-product enumeration/reporting engine, which would risk becoming
// the "analytics database" ADR 0007 explicitly says not to build.
type PairPerformanceSummary struct {
	AgentID   supervisor.AgentID
	TaskClass TaskClass

	// RouteAlias/Provider/Model are the cohort's filter values, echoed
	// back for display - empty means "not filtered on this dimension"
	// (a wildcard), not "observed value is empty."
	RouteAlias string
	Provider   string
	Model      string

	// ObservationCount/FinalizedCount use EXECUTION-LEVEL presence
	// semantics: one execution whose ModelUsages contains a matching
	// entry counts ONCE, regardless of how many inference calls that
	// entry's Count represents. See InferenceUsageCount for the OTHER,
	// explicitly distinct semantic (ADR 0007's "execution-level presence
	// vs inference usage count" requirement).
	ObservationCount int
	FinalizedCount   int

	CompletedCount int
	FailedCount    int
	TimeoutCount   int

	VerifiedCount      int
	RejectedCount      int
	ContestedCount     int
	NeedsEvidenceCount int

	// InferenceUsageCount is the SUM of ModelUsageRef.Count across every
	// matching entry in every matching execution - the inference-call-
	// count semantic, kept in a distinctly named field so a caller can
	// never accidentally read "4 executions" when the real meaning is
	// "7 inference calls across those executions" or vice versa.
	InferenceUsageCount int

	MedianDuration time.Duration

	// InsufficientData reuses DefaultMinimumObservationCount (ADR 0005)
	// unchanged - pair-level cohorts are inherently sparser than
	// agent-level ones, and this is deliberately the SAME threshold, not
	// a new, separately-tuned one, per ADR 0007's explicit instruction
	// to reuse rather than invent a second sample-size configuration.
	InsufficientData bool
}

// modelUsageMatches reports whether usage satisfies the given filter
// dimensions - an empty filter field matches anything (wildcard).
func modelUsageMatches(usage ModelUsageRef, routeAlias, provider, model string) bool {
	if routeAlias != "" && usage.RouteID != routeAlias {
		return false
	}
	if provider != "" && usage.Provider != provider {
		return false
	}
	if model != "" && usage.Model != model {
		return false
	}
	return true
}

// observationMatchesCohort reports whether ANY of obs's ModelUsages
// satisfies the filter - this is execution-level presence: the
// execution either involved this route/provider/model cohort at least
// once, or it did not. It says nothing about how many times.
func observationMatchesCohort(obs PerformanceObservation, routeAlias, provider, model string) bool {
	if routeAlias == "" && provider == "" && model == "" {
		return true // no model-dimension filter at all - every observation matches
	}
	for _, u := range obs.ModelUsages {
		if modelUsageMatches(u, routeAlias, provider, model) {
			return true
		}
	}
	return false
}

// AggregatePair computes a deterministic PairPerformanceSummary for one
// (agentID, taskClass, routeAlias/provider/model) cohort from dataset,
// honoring filter's tenant scope (fail-closed, same as Aggregate/Query -
// ADR 0005/0006's tenant discipline, unchanged). agentID may be empty
// for an agent-agnostic, route/model-level cohort across every agent
// within the task class. At least one of routeAlias/provider/model must
// be non-empty, or this degenerates into plain Aggregate (by design: a
// pair query with no model-dimension filter at all is not a pair query).
func AggregatePair(ctx context.Context, dataset DecisionDataset, filter ObservationFilter, agentID supervisor.AgentID, taskClass TaskClass, routeAlias, provider, model string) (PairPerformanceSummary, error) {
	scoped := filter
	scoped.AgentID = agentID
	scoped.TaskClass = taskClass
	scoped.FinalizedOnly = false

	obs, err := dataset.Observations(ctx, scoped)
	if err != nil {
		return PairPerformanceSummary{}, err
	}

	summary := PairPerformanceSummary{AgentID: agentID, TaskClass: taskClass, RouteAlias: routeAlias, Provider: provider, Model: model}
	var durations []time.Duration

	for _, o := range obs {
		if !observationMatchesCohort(o, routeAlias, provider, model) {
			continue
		}
		summary.ObservationCount++
		if o.Status == ObservationFinalized {
			summary.FinalizedCount++
		}

		switch o.ExecutionOutcome {
		case ExecutionCompleted:
			summary.CompletedCount++
		case ExecutionFailed:
			summary.FailedCount++
		case ExecutionTimedOut:
			summary.TimeoutCount++
		}

		switch o.QualityOutcome {
		case QualityVerified:
			summary.VerifiedCount++
		case QualityRejected:
			summary.RejectedCount++
		case QualityContested:
			summary.ContestedCount++
		case QualityNeedsEvidence:
			summary.NeedsEvidenceCount++
		}

		for _, u := range o.ModelUsages {
			if modelUsageMatches(u, routeAlias, provider, model) {
				summary.InferenceUsageCount += u.Count
			}
		}

		if o.Metrics.Duration > 0 {
			durations = append(durations, o.Metrics.Duration)
		}
	}

	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		summary.MedianDuration = durations[len(durations)/2]
	}

	if summary.ObservationCount < DefaultMinimumObservationCount {
		summary.InsufficientData = true
	}

	return summary, nil
}

// CountModelUsage computes BOTH distinct counting semantics across obs
// for one (routeAlias, provider, model) cohort, explicitly separated so
// neither is ever mistaken for the other: executionsInvolving is
// execution-level presence (ADR 0007's required "executions involving
// Model X" query); inferenceUsages is the summed ModelUsageRef.Count
// (the required "number of Model X inference usages" query).
func CountModelUsage(obs []PerformanceObservation, routeAlias, provider, model string) (executionsInvolving, inferenceUsages int) {
	for _, o := range obs {
		matched := false
		for _, u := range o.ModelUsages {
			if modelUsageMatches(u, routeAlias, provider, model) {
				inferenceUsages += u.Count
				matched = true
			}
		}
		if matched {
			executionsInvolving++
		}
	}
	return executionsInvolving, inferenceUsages
}
