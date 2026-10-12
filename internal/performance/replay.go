package performance

import (
	"context"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// ReplayStatus classifies one offline replay result.
type ReplayStatus string

const (
	ReplayComplete                ReplayStatus = "complete"
	ReplayIncomplete              ReplayStatus = "incomplete"
	ReplayNotApplicable           ReplayStatus = "not_applicable"
	ReplayInvalidCandidateContext ReplayStatus = "invalid_candidate_context"
	ReplayInsufficientData        ReplayStatus = "insufficient_data"
)

// ReplayMode is explicit and never inferred - a caller must say whether
// it wants TimeFaithful (only information knowable at DecisionTime) or
// Retrospective (today's full data, clearly labeled as such) semantics.
// There is no default/implicit mode.
type ReplayMode string

const (
	ReplayModeTimeFaithful  ReplayMode = "time_faithful"
	ReplayModeRetrospective ReplayMode = "retrospective"
)

// ReplayResult answers ONLY "what would Policy P have recommended" -
// never "what would have happened." There is no field here (or
// anywhere in this package) named PredictedOutcome/ExpectedSuccess/
// WouldHaveVerified, and none should ever be added - ADR 0008's
// explicit "important counterfactual restriction."
type ReplayResult struct {
	DecisionID supervisor.DecisionID
	TaskID     supervisor.TaskID

	HistoricalPolicy RoutingPolicyDescriptor
	ReplayPolicy     RoutingPolicyDescriptor

	HistoricalAgentID supervisor.AgentID
	ReplayAgentID     supervisor.AgentID

	Status ReplayStatus
	Mode   ReplayMode
}

// Replayer simulates routing decisions over historical inputs. It has
// NO field, import, or function signature referencing
// supervisor.Executor or supervisor.ExecutionProvider anywhere in this
// file - structurally incapable of invoking real execution, exactly
// like ShadowEvaluator.
type Replayer struct {
	SnapshotStore CandidateSnapshotStore
	Dataset       DecisionDataset
	Limits        EvaluationLimits
	Telemetry     EvaluationTelemetry
}

func NewReplayer(snapshotStore CandidateSnapshotStore, dataset DecisionDataset, limits EvaluationLimits, tel EvaluationTelemetry) *Replayer {
	if tel == nil {
		tel = noopEvaluationTelemetry{}
	}
	return &Replayer{SnapshotStore: snapshotStore, Dataset: dataset, Limits: limits, Telemetry: tel}
}

// ReplayRequest is one historical decision to replay - DecisionID
// (ADR 0009D), not TaskID, is what selects WHICH decision's
// CandidateSnapshot to replay against, since a task may have had
// several distinct decisions over its lifetime. TaskID/TaskClass remain
// for correlation/query convenience and for constructing the
// RoutingEvaluationInput, but the snapshot lookup itself is
// decision-scoped.
type ReplayRequest struct {
	Tenant            string
	DecisionID        supervisor.DecisionID
	TaskID            supervisor.TaskID
	TaskClass         TaskClass
	DecisionTime      time.Time
	HistoricalPolicy  RoutingPolicyDescriptor
	HistoricalAgentID supervisor.AgentID
	ReplayPolicy      RoutingPolicyDescriptor
	// NewReplayPolicy constructs the policy to replay, given the
	// dataset the Replayer has already chosen for the requested mode
	// (time-faithful-wrapped, or the raw retrospective dataset) - this
	// is how a caller plugs in e.g. a DeterministicAdvisor bound to
	// whichever dataset view is correct for this replay, without the
	// Replayer needing to know how to construct one itself.
	NewReplayPolicy func(dataset DecisionDataset) RoutingPolicy
	Mode            ReplayMode
}

// Replay simulates ONE historical decision. It never calls an
// ExecutionProvider and never touches a live agent registry - the
// candidate context comes EXCLUSIVELY from SnapshotStore, looked up by
// the EXACT (req.Tenant, req.DecisionID) this request names; if no
// snapshot was ever captured for that decision, the result is
// ReplayIncomplete, never a substitution of today's eligible
// candidates (ADR 0008 Part 8's explicit prohibition) and never a
// different decision's snapshot for the same TaskID (ADR 0009D).
func (r *Replayer) Replay(ctx context.Context, req ReplayRequest) (ReplayResult, error) {
	r.Telemetry.RecordReplayStarted()
	result := ReplayResult{
		DecisionID: req.DecisionID, TaskID: req.TaskID, HistoricalPolicy: req.HistoricalPolicy, ReplayPolicy: req.ReplayPolicy,
		HistoricalAgentID: req.HistoricalAgentID, Mode: req.Mode,
	}

	snapshot, found, err := r.SnapshotStore.Get(ctx, req.Tenant, req.DecisionID)
	if err != nil {
		return result, err
	}
	if !found {
		result.Status = ReplayIncomplete
		r.Telemetry.RecordReplayIncomplete()
		return result, nil
	}
	if !snapshot.Contains(req.HistoricalAgentID) {
		// The historically-recorded actual decision isn't even present
		// in its own snapshot - the historical record is internally
		// inconsistent; never trust it as a valid comparison basis.
		result.Status = ReplayInvalidCandidateContext
		r.Telemetry.RecordReplayIncomplete()
		return result, nil
	}

	var dataset DecisionDataset = r.Dataset
	switch req.Mode {
	case ReplayModeTimeFaithful:
		dataset = &TimeFaithfulDataset{Dataset: r.Dataset, AsOf: req.DecisionTime}
		r.Telemetry.RecordTimeFaithfulReplay()
	case ReplayModeRetrospective:
		r.Telemetry.RecordRetrospectiveReplay()
	default:
		result.Status = ReplayNotApplicable
		return result, nil
	}

	policy := req.NewReplayPolicy(dataset)
	input := RoutingEvaluationInput{Tenant: req.Tenant, TaskID: req.TaskID, TaskClass: req.TaskClass, Snapshot: snapshot}
	rec, err := policy.Recommend(ctx, input)
	if err != nil {
		result.Status = ReplayInsufficientData
		r.Telemetry.RecordReplayIncomplete()
		return result, nil
	}
	if rec.Insufficient {
		result.Status = ReplayInsufficientData
		r.Telemetry.RecordReplayIncomplete()
		return result, nil
	}
	if rec.AgentID == "" || !snapshot.Contains(rec.AgentID) {
		result.Status = ReplayInvalidCandidateContext
		r.Telemetry.RecordReplayIncomplete()
		return result, nil
	}

	result.ReplayAgentID = rec.AgentID
	result.Status = ReplayComplete
	r.Telemetry.RecordReplayCompleted()
	return result, nil
}

// ReplayBatch replays several historical decisions, bounded by
// Limits.MaxReplayBatchSize - a resource-abuse guard, never a silent
// truncation.
func (r *Replayer) ReplayBatch(ctx context.Context, requests []ReplayRequest) ([]ReplayResult, error) {
	if len(requests) > r.Limits.maxReplayBatchSize() {
		return nil, &ReplayBatchTooLarge{Count: len(requests), Limit: r.Limits.maxReplayBatchSize()}
	}
	out := make([]ReplayResult, 0, len(requests))
	for _, req := range requests {
		result, err := r.Replay(ctx, req)
		if err != nil {
			return out, err
		}
		out = append(out, result)
	}
	return out, nil
}

// ReplayBatchTooLarge is returned by ReplayBatch when the requested
// batch exceeds Limits.MaxReplayBatchSize.
type ReplayBatchTooLarge struct{ Count, Limit int }

func (e *ReplayBatchTooLarge) Error() string { return "replay batch exceeds the maximum allowed size" }
