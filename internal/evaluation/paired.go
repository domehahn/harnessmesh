package evaluation

import (
	"errors"
	"fmt"
)

type PairedComparison struct {
	Baseline         Aggregate  `json:"baseline"`
	Candidate        Aggregate  `json:"candidate"`
	CandidateOnly    int        `json:"candidate_only_succeeded"`
	BaselineOnly     int        `json:"baseline_only_succeeded"`
	BothSucceeded    int        `json:"both_succeeded"`
	BothFailed       int        `json:"both_failed"`
	BaselineMetrics  RunMetrics `json:"baseline_metrics"`
	CandidateMetrics RunMetrics `json:"candidate_metrics"`
}

// RunMetrics are independently observed run totals. A nil value means at least
// one run was missing that metric, so a comparable cohort mean is unavailable.
type RunMetrics struct {
	MeanWallTimeMS       *float64 `json:"mean_wall_time_ms"`
	MeanCostMicrousd     *float64 `json:"mean_cost_microusd"`
	MeanVerifiedFindings *float64 `json:"mean_verified_findings"`
}

// ComparePaired matches one observation per tenant and task in each cohort.
func ComparePaired(baseline, candidate []Run) (PairedComparison, error) {
	var result PairedComparison
	if len(baseline) == 0 || len(baseline) != len(candidate) {
		return result, errors.New("comparison requires two nonempty cohorts of equal size")
	}

	type taskKey struct{ tenant, task string }
	pending := make(map[taskKey]Run, len(baseline))
	for _, run := range baseline {
		key := taskKey{run.Tenant, run.TaskID}
		if _, exists := pending[key]; exists {
			return result, fmt.Errorf("duplicate baseline task %q in tenant %q", key.task, key.tenant)
		}
		pending[key] = run
	}
	for _, run := range candidate {
		key := taskKey{run.Tenant, run.TaskID}
		base, exists := pending[key]
		if !exists {
			return result, fmt.Errorf("unmatched or duplicate candidate task %q in tenant %q", key.task, key.tenant)
		}
		delete(pending, key)
		if base.Input != run.Input || base.Expected != run.Expected {
			return result, fmt.Errorf("different task inputs or expectations for %q in tenant %q", key.task, key.tenant)
		}
		switch {
		case base.Success && run.Success:
			result.BothSucceeded++
		case run.Success:
			result.CandidateOnly++
		case base.Success:
			result.BaselineOnly++
		default:
			result.BothFailed++
		}
	}
	var err error
	result.Baseline, err = AggregateRuns(baseline)
	if err != nil {
		return PairedComparison{}, err
	}
	result.Candidate, err = AggregateRuns(candidate)
	if err != nil {
		return PairedComparison{}, err
	}

	result.BaselineMetrics, err = meanRunMetrics(baseline)
	if err != nil {
		return PairedComparison{}, err
	}
	result.CandidateMetrics, err = meanRunMetrics(candidate)
	if err != nil {
		return PairedComparison{}, err
	}
	return result, nil
}

func meanRunMetrics(runs []Run) (RunMetrics, error) {
	var totals [3]float64
	var counts [3]int
	for _, run := range runs {
		for i, metric := range []*int64{run.WallTimeMS, run.CostMicrousd, run.VerifiedFindings} {
			if metric == nil {
				continue
			}
			if *metric < 0 {
				return RunMetrics{}, fmt.Errorf("negative comparison metric for task %q", run.TaskID)
			}
			totals[i] += float64(*metric)
			counts[i]++
		}
	}
	var result RunMetrics
	for i, target := range []**float64{&result.MeanWallTimeMS, &result.MeanCostMicrousd, &result.MeanVerifiedFindings} {
		if counts[i] == len(runs) {
			mean := totals[i] / float64(len(runs))
			*target = &mean
		}
	}
	return result, nil
}
