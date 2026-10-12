package performance

import (
	"context"
	"sort"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// DefaultMinimumObservationCount is the sample size below which an
// AgentPerformanceSummary is marked InsufficientData - deliberately NOT
// used to produce an artificially bad score for a new/cold-start agent
// (see ADR 0005's "Sample Size / Cold Start" section). It exists purely
// as a signal a FUTURE consumer could use to decide whether to trust a
// summary; this package itself never acts on it.
const DefaultMinimumObservationCount = 10

// AgentPerformanceSummary is a deterministic aggregation over one
// (AgentID, TaskClass) pair - never a single composite score. Every
// count is independently inspectable; PendingCount and FinalizedCount
// are always reported separately so an unreviewed/pending execution can
// never be mistaken for a verified one, even in aggregate.
type AgentPerformanceSummary struct {
	AgentID   supervisor.AgentID
	TaskClass TaskClass

	ObservationCount int
	PendingCount     int
	FinalizedCount   int

	CompletedCount int
	FailedCount    int
	TimedOutCount  int
	CancelledCount int
	BlockedCount   int

	VerifiedCount      int
	RejectedCount      int
	ContestedCount     int
	NeedsEvidenceCount int
	SupportedCount     int
	UnreviewedCount    int

	RemediationCount         int
	RemediationVerifiedCount int

	// TotalReworkCount sums Metrics.ReworkCount across every observation
	// in scope - used by DeterministicAdvisor (advisor.go) as the
	// "rework burden" ranking component.
	TotalReworkCount int

	MeanDuration   time.Duration
	MedianDuration time.Duration

	// InsufficientData is set when ObservationCount is below the
	// configured minimum - a signal, never a reason for this package to
	// hide or suppress the (still-honest) counts above it.
	InsufficientData bool
}

// ModelUsageSummary aggregates how often each (RouteID, Model) pair was
// used within a filtered set of observations - kept separate from
// AgentPerformanceSummary per "Agent Performance != Model Performance":
// a caller wanting a joint Agent+Model view combines an
// AgentPerformanceSummary (filtered to one agent) with a
// ModelUsageSummary computed over the SAME filtered observation set,
// rather than this package building a merged Agent+Model router
// component (explicitly out of scope).
type ModelUsageSummary struct {
	RouteID string
	Model   string
	Count   int
}

// Aggregate computes a deterministic AgentPerformanceSummary for one
// (agentID, taskClass) pair from dataset, honoring filter's tenant scope
// (fail-closed, same as Query). It performs no ranking, no scoring, and
// makes no routing recommendation - see ADR 0005's explicit "No Agent
// Ranking Yet."
func Aggregate(ctx context.Context, dataset DecisionDataset, filter ObservationFilter, agentID supervisor.AgentID, taskClass TaskClass) (AgentPerformanceSummary, error) {
	scoped := filter
	scoped.AgentID = agentID
	scoped.TaskClass = taskClass
	scoped.FinalizedOnly = false // we need pending ones too, to report them honestly

	obs, err := dataset.Observations(ctx, scoped)
	if err != nil {
		return AgentPerformanceSummary{}, err
	}

	summary := AgentPerformanceSummary{AgentID: agentID, TaskClass: taskClass, ObservationCount: len(obs)}
	var durations []time.Duration

	for _, o := range obs {
		switch o.Status {
		case ObservationPending:
			summary.PendingCount++
		case ObservationFinalized:
			summary.FinalizedCount++
		}

		switch o.ExecutionOutcome {
		case ExecutionCompleted:
			summary.CompletedCount++
		case ExecutionFailed:
			summary.FailedCount++
		case ExecutionTimedOut:
			summary.TimedOutCount++
		case ExecutionCancelled:
			summary.CancelledCount++
		case ExecutionBlocked, ExecutionPolicyRejected, ExecutionAuthorizationRejected:
			summary.BlockedCount++
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
		case QualitySupported:
			summary.SupportedCount++
		case QualityUnreviewed, QualityUnconfirmed:
			summary.UnreviewedCount++
		}

		summary.TotalReworkCount += o.Metrics.ReworkCount

		if taskClass == TaskClassRemediation {
			summary.RemediationCount++
			if o.QualityOutcome == QualityVerified {
				summary.RemediationVerifiedCount++
			}
		}

		if o.Metrics.Duration > 0 {
			durations = append(durations, o.Metrics.Duration)
		}
	}

	if len(durations) > 0 {
		var total time.Duration
		for _, d := range durations {
			total += d
		}
		summary.MeanDuration = total / time.Duration(len(durations))
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		summary.MedianDuration = durations[len(durations)/2]
	}

	if summary.ObservationCount < DefaultMinimumObservationCount {
		summary.InsufficientData = true
	}

	return summary, nil
}

// AggregateModelUsage computes a deterministic, agent-independent
// breakdown of model usage across a filtered observation set.
func AggregateModelUsage(ctx context.Context, dataset DecisionDataset, filter ObservationFilter) ([]ModelUsageSummary, error) {
	if err := filter.validate(); err != nil {
		return nil, err
	}
	obs, err := dataset.Observations(ctx, filter)
	if err != nil {
		return nil, err
	}
	counts := map[string]*ModelUsageSummary{}
	for _, o := range obs {
		for _, usage := range o.ModelUsages {
			key := usage.RouteID + "|" + usage.Model
			if counts[key] == nil {
				counts[key] = &ModelUsageSummary{RouteID: usage.RouteID, Model: usage.Model}
			}
			counts[key].Count += usage.Count
		}
	}
	out := make([]ModelUsageSummary, 0, len(counts))
	for _, v := range counts {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RouteID != out[j].RouteID {
			return out[i].RouteID < out[j].RouteID
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}
