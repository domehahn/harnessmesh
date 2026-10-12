package performance

// EvaluationLimits are Core-configured, deterministic guardrails for the
// evaluation plane - never read from Arbiter/Challenger output, mirroring
// supervisor.SupervisorLimits/review.ReviewLimits. Zero-valued fields
// fall back to their Default* constant via the maxX() accessor methods,
// exactly like supervisor.Executor's own MaxExecutionTime fallback
// pattern - so a caller that forgets to set a limit gets a safe,
// explicit default rather than an unbounded value.
type EvaluationLimits struct {
	MaxCandidatesPerSnapshot  int
	MaxReasonCount            int
	MaxReasonLength           int
	MaxChallengersPerDecision int
	MaxReplayBatchSize        int
}

const (
	DefaultMaxCandidatesPerSnapshot  = 200
	DefaultMaxReasonCount            = 8
	DefaultMaxReasonLength           = 300
	DefaultMaxChallengersPerDecision = 5
	DefaultMaxReplayBatchSize        = 1000
)

// DefaultEvaluationLimits returns conservative, explicit defaults.
func DefaultEvaluationLimits() EvaluationLimits {
	return EvaluationLimits{
		MaxCandidatesPerSnapshot:  DefaultMaxCandidatesPerSnapshot,
		MaxReasonCount:            DefaultMaxReasonCount,
		MaxReasonLength:           DefaultMaxReasonLength,
		MaxChallengersPerDecision: DefaultMaxChallengersPerDecision,
		MaxReplayBatchSize:        DefaultMaxReplayBatchSize,
	}
}

func (l EvaluationLimits) maxCandidatesPerSnapshot() int {
	if l.MaxCandidatesPerSnapshot > 0 {
		return l.MaxCandidatesPerSnapshot
	}
	return DefaultMaxCandidatesPerSnapshot
}
func (l EvaluationLimits) maxReasonCount() int {
	if l.MaxReasonCount > 0 {
		return l.MaxReasonCount
	}
	return DefaultMaxReasonCount
}
func (l EvaluationLimits) maxReasonLength() int {
	if l.MaxReasonLength > 0 {
		return l.MaxReasonLength
	}
	return DefaultMaxReasonLength
}
func (l EvaluationLimits) maxChallengersPerDecision() int {
	if l.MaxChallengersPerDecision > 0 {
		return l.MaxChallengersPerDecision
	}
	return DefaultMaxChallengersPerDecision
}
func (l EvaluationLimits) maxReplayBatchSize() int {
	if l.MaxReplayBatchSize > 0 {
		return l.MaxReplayBatchSize
	}
	return DefaultMaxReplayBatchSize
}

// boundReasons truncates a reasons slice to at most maxReasonCount
// entries, each truncated to at most maxReasonLength bytes - ADR 0008's
// explicit "massive challenger explanation must not create store/memory
// amplification" requirement.
func boundReasons(reasons []string, limits EvaluationLimits) []string {
	maxCount := limits.maxReasonCount()
	maxLen := limits.maxReasonLength()
	if len(reasons) > maxCount {
		reasons = reasons[:maxCount]
	}
	out := make([]string, len(reasons))
	for i, r := range reasons {
		if len(r) > maxLen {
			r = r[:maxLen]
		}
		out[i] = r
	}
	return out
}

// SnapshotIdentityConflict (ADR 0008/9C) is SUPERSEDED by
// DecisionSnapshotConflict (ADR 0009D, candidate_snapshot.go) now that
// CandidateSnapshotStore is keyed by (Tenant, DecisionID) rather than
// (Tenant, TaskID) - removed rather than kept as dead code, since
// nothing in this package constructs it anymore and no historical
// persisted data exists anywhere to reference it (ADR 0008/9C never
// shipped a real, non-Memory-backed store).
