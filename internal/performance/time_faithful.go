package performance

import (
	"context"
	"time"
)

// ObservationAsOf returns the view of obs that was knowable at asOf -
// never more. ok is false if the observation did not exist yet at asOf
// (obs.CreatedAt is after it) - it is excluded entirely. If the
// observation DID exist but was finalized (quality determined) AFTER
// asOf, the returned value has its quality dimension rolled back to
// QualityUnreviewed/ObservationPending: at asOf, only the pending
// execution outcome was known, not whatever verdict review later
// reached. This is a conservative under-approximation (it can only ever
// claim to know LESS than full history shows, never more) - there is no
// event-sourced history of every intermediate quality state to
// reconstruct exactly, so erring toward "unknown" rather than guessing
// is the safe direction. This is ADR 0008's "what information counts as
// available at DecisionTime" definition, made executable.
func ObservationAsOf(obs PerformanceObservation, asOf time.Time) (PerformanceObservation, bool) {
	if obs.CreatedAt.After(asOf) {
		return PerformanceObservation{}, false
	}
	if obs.UpdatedAt.After(asOf) {
		view := obs
		view.QualityOutcome = QualityUnreviewed
		view.Status = ObservationPending
		view.ReviewGroupID = ""
		view.ReviewerAgentIDs = nil
		return view, true
	}
	return obs, true
}

// TimeFaithfulDataset wraps a DecisionDataset so every observation it
// returns reflects only what was knowable AS OF a fixed cutoff - ADR
// 0008 Part 7's look-ahead-bias protection. Every existing aggregation
// function (Aggregate, AggregatePair, AggregateModelUsage,
// DeterministicAdvisor.Advise) works unchanged against this wrapper,
// since it only implements DecisionDataset - no aggregation logic was
// duplicated or reimplemented to get time-faithful behavior.
type TimeFaithfulDataset struct {
	Dataset DecisionDataset
	AsOf    time.Time
}

func (d *TimeFaithfulDataset) Observations(ctx context.Context, filter ObservationFilter) ([]PerformanceObservation, error) {
	all, err := d.Dataset.Observations(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := make([]PerformanceObservation, 0, len(all))
	for _, o := range all {
		if view, ok := ObservationAsOf(o, d.AsOf); ok {
			out = append(out, view)
		}
	}
	return out, nil
}

var _ DecisionDataset = (*TimeFaithfulDataset)(nil)
