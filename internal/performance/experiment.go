package performance

// Phase 10 durable experiment control-plane primitives.  This file contains
// only deterministic control and admission logic; it deliberately does not
// implement promotion, learning, bandits, or automatic policy mutation.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

type ExperimentID string
type ExperimentState string

const (
	ExperimentDraft     ExperimentState = "draft"
	ExperimentEnabled   ExperimentState = "enabled"
	ExperimentPaused    ExperimentState = "paused"
	ExperimentStopped   ExperimentState = "stopped"
	ExperimentExhausted ExperimentState = "exhausted"
	ExperimentCompleted ExperimentState = "completed"
	ExperimentKilled    ExperimentState = "killed"
)

type AllocationMode string

const AllocationSemanticsV1 AllocationMode = "hash-v1"

type ExperimentDefinition struct {
	ID                  ExperimentID
	Tenant              string
	TaskClass           string
	Risk                string
	ChampionPolicy      RoutingPolicyDescriptor
	ChallengerPolicy    RoutingPolicyDescriptor
	ChallengerAgentID   supervisor.AgentID
	MaxExecutions       int
	MaxConcurrent       int
	AllocationPercent   int
	AllocationSemantics AllocationMode
	StopAfterExecutions int
	// MaxChallengerFailures is Part 11/31/46's deterministic automatic
	// stop condition: once TotalChallengerFailures reaches this bound,
	// the experiment auto-transitions to Stopped and all future
	// admissions fall back to Champion. Zero disables this condition.
	// Deliberately NOT a statistical/ML threshold (no "challenger seems
	// worse by X%" comparison exists anywhere in this package).
	MaxChallengerFailures int
	CreatedAt             time.Time
}

type Experiment struct {
	Definition ExperimentDefinition
	State      ExperimentState
	Executed   int
	Reserved   int
	Consumed   int
	Released   int
	UpdatedAt  time.Time

	// The fields below back ExperimentSummary (Part 29). They are
	// best-effort OBSERVABILITY counters only - never consulted by any
	// admission/budget/security decision (that is exclusively Reserved/
	// Consumed/Released via the store's atomic Reserve/Consume/Release).
	// Updated via a plain Get-mutate-Save cycle, so a narrow concurrent
	// double-count is possible under high contention; that is an
	// accepted tradeoff for summary counters, never accepted for budget.
	EligibleDecisions   int
	ChampionRouted      int
	ChallengerCompleted int
	ChallengerFailed    int
	ChallengerTimedOut  int
	ChallengerVerified  int
	ChallengerRejected  int
	ChallengerContested int
	MissingOutcomeCount int

	// ConsecutiveChallengerFailures/TotalChallengerFailures back the
	// deterministic stop condition (Part 11/31/46): MaxChallengerFailures
	// authoritative Challenger failures (not timeouts) auto-transitions
	// the experiment to Stopped. Explicitly NOT a statistical/ML
	// condition - a plain bounded counter threshold.
	TotalChallengerFailures int
}

// ExperimentSummary is Part 29's deterministic, non-composite rollup of
// an experiment's state. It deliberately contains no composite score,
// no utility metric, and no expected-value field of any kind - only
// plain, independently-meaningful counts.
type ExperimentSummary struct {
	ExperimentID             ExperimentID
	EligibleDecisionCount    int
	ChampionExecutionCount   int
	ChallengerExecutionCount int
	BudgetReserved           int
	BudgetConsumed           int
	ChallengerCompleted      int
	ChallengerFailed         int
	ChallengerTimedOut       int
	ChallengerVerified       int
	ChallengerRejected       int
	ChallengerContested      int
	MissingOutcomeCount      int
	Status                   ExperimentState
}

type AdmissionRequest struct {
	ExperimentID      ExperimentID
	DecisionID        supervisor.DecisionID
	Tenant            string
	Task              supervisor.Task
	Snapshot          CandidateSnapshot
	ChampionAgentID   supervisor.AgentID
	ChallengerAgentID supervisor.AgentID
	ChallengerPolicy  RoutingPolicyDescriptor
	Risk              string
	Now               time.Time
}

// ExperimentEligibilityReason is a bounded, closed enum of admission
// outcomes. Admission control is a security decision and must branch on
// a typed, exhaustively-known value - never on a free-form string that
// could silently drift or be matched inconsistently across call sites.
type ExperimentEligibilityReason string

const (
	EligibilityEligible                 ExperimentEligibilityReason = "eligible"
	EligibilityDisabled                 ExperimentEligibilityReason = "disabled"
	EligibilityWrongTenant              ExperimentEligibilityReason = "wrong_tenant"
	EligibilityTaskClassNotAllowed      ExperimentEligibilityReason = "task_class_not_allowed"
	EligibilityRiskNotAllowed           ExperimentEligibilityReason = "risk_not_allowed"
	EligibilityMissingDecisionContext   ExperimentEligibilityReason = "missing_decision_context"
	EligibilityMissingEvaluationContext ExperimentEligibilityReason = "missing_evaluation_context"
	EligibilityNonCanonicalPolicy       ExperimentEligibilityReason = "non_canonical_policy"
	EligibilityCandidateNotInSnapshot   ExperimentEligibilityReason = "candidate_not_in_snapshot"
	EligibilityBudgetExhausted          ExperimentEligibilityReason = "budget_exhausted"
	EligibilityConcurrencyLimit         ExperimentEligibilityReason = "concurrency_limit"
	EligibilityExperimentPaused         ExperimentEligibilityReason = "experiment_paused"
	EligibilityExperimentStopped        ExperimentEligibilityReason = "experiment_stopped"
	EligibilityExperimentKilled         ExperimentEligibilityReason = "experiment_killed"
	EligibilityLiveRevalidationFailed   ExperimentEligibilityReason = "live_revalidation_failed"
	// EligibilityControlPlaneUnavailable is not part of the mandate's
	// minimum enum but is required by the same "no free strings" rule
	// for this package's own infrastructure-failure paths (experiment
	// lookup failed, persistence failed) - these must fail closed to
	// Champion exactly like any other denial, never fail-open.
	EligibilityControlPlaneUnavailable ExperimentEligibilityReason = "control_plane_unavailable"
)

// ExperimentEligibilityResult is the typed admission verdict. Eligible
// means the decision satisfied every scope/context/policy precondition
// for experimentation - it does NOT mean a Challenger was selected:
// allocation and budget still apply after eligibility, and an eligible
// decision can still end up routed to Champion by deterministic
// allocation or budget exhaustion.
type ExperimentEligibilityResult struct {
	Eligible bool
	Reason   ExperimentEligibilityReason
}

type Admission struct {
	ExperimentID ExperimentID
	DecisionID   supervisor.DecisionID
	Selected     supervisor.AgentID
	IsChallenger bool
	Reservation  string
	Reason       string
	ReasonCode   ExperimentEligibilityReason
	CreatedAt    time.Time
}

type ExperimentAudit struct {
	ExperimentID ExperimentID
	DecisionID   supervisor.DecisionID
	Event        string
	Reason       string
	At           time.Time
}

type ExperimentStore interface {
	Create(ctx context.Context, experiment Experiment) error
	Get(ctx context.Context, id ExperimentID) (Experiment, error)
	Save(ctx context.Context, experiment Experiment) error
	Reserve(ctx context.Context, id ExperimentID, decisionID supervisor.DecisionID, now time.Time) (bool, error)
	Release(ctx context.Context, id ExperimentID, decisionID supervisor.DecisionID, now time.Time) error
	Consume(ctx context.Context, id ExperimentID, decisionID supervisor.DecisionID, now time.Time) error
	SaveAdmission(ctx context.Context, admission Admission) error
	GetAdmission(ctx context.Context, id ExperimentID, decisionID supervisor.DecisionID) (Admission, error)
	Audit(ctx context.Context, audit ExperimentAudit) error
	// UpdateSummary atomically applies mutate to the CURRENT, freshly
	// read Experiment and persists only the result - unlike Save (a
	// blind full-record overwrite of a caller-held copy), an
	// implementation of UpdateSummary MUST perform the read and the
	// write as one atomic/serialized unit so that a concurrent
	// Reserve/Consume/Release can never be lost underneath it. It exists
	// specifically so the high-frequency Part 29 summary counters (not
	// security/budget-critical) can be updated on every admission
	// decision WITHOUT ever risking the budget ledger's correctness -
	// see ExperimentController.mutateCounters.
	UpdateSummary(ctx context.Context, id ExperimentID, mutate func(*Experiment)) error
}

var (
	ErrExperimentNotFound      = errors.New("experiment not found")
	ErrExperimentNotAdmissible = errors.New("experiment is not admissible")
	ErrExperimentConflict      = errors.New("experiment admission conflict")
)

type MemoryExperimentStore struct {
	mu           sync.Mutex
	experiments  map[ExperimentID]Experiment
	reservations map[string]bool
	admissions   map[string]Admission
	audit        []ExperimentAudit
}

func NewMemoryExperimentStore() *MemoryExperimentStore {
	return &MemoryExperimentStore{experiments: map[ExperimentID]Experiment{}, reservations: map[string]bool{}, admissions: map[string]Admission{}}
}
func admissionKey(id ExperimentID, decision supervisor.DecisionID) string {
	return string(id) + "\x00" + string(decision)
}
func (s *MemoryExperimentStore) Create(_ context.Context, e Experiment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.experiments[e.Definition.ID]; ok {
		return ErrExperimentConflict
	}
	s.experiments[e.Definition.ID] = e
	return nil
}
func (s *MemoryExperimentStore) Get(_ context.Context, id ExperimentID) (Experiment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.experiments[id]
	if !ok {
		return Experiment{}, ErrExperimentNotFound
	}
	return e, nil
}
func (s *MemoryExperimentStore) Save(_ context.Context, e Experiment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.experiments[e.Definition.ID]; !ok {
		return ErrExperimentNotFound
	}
	s.experiments[e.Definition.ID] = e
	return nil
}
func (s *MemoryExperimentStore) Reserve(_ context.Context, id ExperimentID, d supervisor.DecisionID, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.experiments[id]
	if !ok {
		return false, ErrExperimentNotFound
	}
	k := admissionKey(id, d)
	if s.reservations[k] {
		return true, nil
	}
	if e.State != ExperimentEnabled || e.Definition.MaxExecutions > 0 && e.Reserved >= e.Definition.MaxExecutions || e.Definition.MaxConcurrent > 0 && e.Reserved-e.Consumed >= e.Definition.MaxConcurrent {
		return false, nil
	}
	e.Reserved++
	e.UpdatedAt = now
	s.experiments[id] = e
	s.reservations[k] = true
	return true, nil
}
func (s *MemoryExperimentStore) Release(_ context.Context, id ExperimentID, d supervisor.DecisionID, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.experiments[id]
	if !ok {
		return ErrExperimentNotFound
	}
	k := admissionKey(id, d)
	if s.reservations[k] {
		delete(s.reservations, k)
		e.Reserved--
		e.Released++
		e.UpdatedAt = now
		s.experiments[id] = e
	}
	return nil
}
func (s *MemoryExperimentStore) Consume(_ context.Context, id ExperimentID, d supervisor.DecisionID, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.experiments[id]
	if !ok {
		return ErrExperimentNotFound
	}
	k := admissionKey(id, d)
	if !s.reservations[k] {
		return ErrExperimentConflict
	}
	delete(s.reservations, k)
	e.Consumed++
	e.Executed++
	e.UpdatedAt = now
	if e.Definition.MaxExecutions > 0 && e.Consumed >= e.Definition.MaxExecutions || e.Definition.StopAfterExecutions > 0 && e.Consumed >= e.Definition.StopAfterExecutions {
		e.State = ExperimentExhausted
	}
	s.experiments[id] = e
	return nil
}
func (s *MemoryExperimentStore) SaveAdmission(_ context.Context, a Admission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := admissionKey(a.ExperimentID, a.DecisionID)
	if old, ok := s.admissions[k]; ok && old != a {
		return ErrExperimentConflict
	}
	s.admissions[k] = a
	return nil
}
func (s *MemoryExperimentStore) GetAdmission(_ context.Context, id ExperimentID, d supervisor.DecisionID) (Admission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.admissions[admissionKey(id, d)]
	if !ok {
		return Admission{}, ErrExperimentNotFound
	}
	return a, nil
}
func (s *MemoryExperimentStore) Audit(_ context.Context, a ExperimentAudit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, a)
	return nil
}
func (s *MemoryExperimentStore) UpdateSummary(_ context.Context, id ExperimentID, mutate func(*Experiment)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.experiments[id]
	if !ok {
		return ErrExperimentNotFound
	}
	mutate(&e)
	s.experiments[id] = e
	return nil
}

type ExperimentController struct {
	Store           ExperimentStore
	SnapshotStore   CandidateSnapshotStore
	EvaluationStore EvaluationStore
	ExperimentID    ExperimentID
	Limits          EvaluationLimits
}

func NewExperimentController(store ExperimentStore) *ExperimentController {
	return &ExperimentController{Store: store}
}

// SchedulerAdmissionAdapter is the only bridge from the experiment control
// plane into Core dispatch. It never calls an executor. The existing
// decision-scoped snapshot/evaluation stores remain mandatory; absent or
// incomplete context therefore returns Champion.
type SchedulerAdmissionAdapter struct{ Controller *ExperimentController }

func (a *SchedulerAdmissionAdapter) Admit(ctx context.Context, task supervisor.Task, decisionID supervisor.DecisionID, candidates []supervisor.AgentCandidate, champion supervisor.AgentID, policy supervisor.PolicyProvenance) (supervisor.AgentID, bool, error) {
	if a == nil || a.Controller == nil || a.Controller.SnapshotStore == nil || a.Controller.EvaluationStore == nil {
		return champion, false, nil
	}
	e, err := a.Controller.Store.Get(ctx, a.Controller.ExperimentID)
	if err != nil {
		return champion, false, err
	}
	cs := make([]supervisor.AgentCandidate, len(candidates))
	copy(cs, candidates)
	snap, found, err := a.Controller.SnapshotStore.Get(ctx, task.Tenant, decisionID)
	if err != nil {
		return champion, false, err
	}
	if !found {
		snap, err = NewCandidateSnapshot(decisionID, task.Tenant, task.ID, TaskClass(task.Type), cs, time.Now().UTC(), a.Controller.Limits)
		if err != nil {
			return champion, false, err
		}
		if err = a.Controller.SnapshotStore.Save(ctx, snap); err != nil {
			return champion, false, err
		}
	}
	challenger := e.Definition.ChallengerAgentID
	challengerPolicy := e.Definition.ChallengerPolicy
	if err = ValidateExperimentContext(ctx, a.Controller.SnapshotStore, a.Controller.EvaluationStore, task.Tenant, decisionID, challengerPolicy, challenger); err != nil {
		return champion, false, nil
	}
	admission, err := a.Controller.Admit(ctx, AdmissionRequest{ExperimentID: a.Controller.ExperimentID, DecisionID: decisionID, Tenant: task.Tenant, Task: task, Snapshot: snap, ChampionAgentID: champion, ChallengerAgentID: challenger, ChallengerPolicy: challengerPolicy, Risk: e.Definition.Risk, Now: time.Now().UTC()})
	if err != nil {
		return champion, false, err
	}
	return admission.Selected, admission.IsChallenger, nil
}

// AllocationBucket deterministically maps (ExperimentID, DecisionID)
// onto [0, 100) - the SAME scale ExperimentDefinition.AllocationPercent
// uses, so "AllocationPercent = 100" means "always admitted" and
// "AllocationPercent = 0" means "never admitted," exactly as a
// percentage should behave. A prior version returned a raw hash byte
// (0-255) compared directly against a 0-100 percentage, which silently
// admitted roughly 100/256 (~39%) of decisions when 100% was
// configured - a real defect found during this phase's security
// closure review and fixed here. Two hash bytes (not one) are combined
// before the modulo to keep the [0,100) distribution reasonably
// uniform.
func AllocationBucket(experimentID ExperimentID, decisionID supervisor.DecisionID) int {
	h := sha256.Sum256([]byte(string(experimentID) + "|" + string(decisionID) + "|" + string(AllocationSemanticsV1)))
	combined := int(h[0])<<8 | int(h[1])
	return combined % 100
}

func (c *ExperimentController) Admit(ctx context.Context, r AdmissionRequest) (Admission, error) {
	if c == nil || c.Store == nil {
		return Admission{Selected: r.ChampionAgentID, Reason: string(EligibilityControlPlaneUnavailable), ReasonCode: EligibilityControlPlaneUnavailable}, nil
	}
	e, err := c.Store.Get(ctx, r.ExperimentID)
	if err != nil {
		return Admission{Selected: r.ChampionAgentID, Reason: string(EligibilityControlPlaneUnavailable), ReasonCode: EligibilityControlPlaneUnavailable}, err
	}
	deny := func(code ExperimentEligibilityReason) (Admission, error) {
		_ = c.Store.Audit(ctx, ExperimentAudit{ExperimentID: r.ExperimentID, DecisionID: r.DecisionID, Event: "admission_denied", Reason: string(code), At: r.Now})
		c.mutateCounters(ctx, r.ExperimentID, r.Now, func(e *Experiment) { e.ChampionRouted++ })
		return Admission{ExperimentID: r.ExperimentID, DecisionID: r.DecisionID, Selected: r.ChampionAgentID, Reason: string(code), ReasonCode: code}, nil
	}
	switch e.State {
	case ExperimentEnabled:
		// fall through to the remaining gates below.
	case ExperimentPaused:
		return deny(EligibilityExperimentPaused)
	case ExperimentStopped, ExperimentCompleted:
		return deny(EligibilityExperimentStopped)
	case ExperimentKilled:
		return deny(EligibilityExperimentKilled)
	case ExperimentExhausted:
		return deny(EligibilityBudgetExhausted)
	default: // Draft, or any future/unknown state - fail closed.
		return deny(EligibilityDisabled)
	}
	if r.Tenant == "" || r.Tenant != e.Definition.Tenant || r.Task.Tenant != e.Definition.Tenant {
		return deny(EligibilityWrongTenant)
	}
	if e.Definition.TaskClass != "" && r.Task.Type != e.Definition.TaskClass {
		return deny(EligibilityTaskClassNotAllowed)
	}
	if e.Definition.Risk != "" && r.Risk != e.Definition.Risk {
		return deny(EligibilityRiskNotAllowed)
	}
	if r.DecisionID == "" || r.Snapshot.DecisionID != r.DecisionID {
		return deny(EligibilityMissingDecisionContext)
	}
	if err := ValidateExperimentPolicyIdentity(r.ChallengerPolicy); err != nil {
		return deny(EligibilityNonCanonicalPolicy)
	}
	if r.ChallengerAgentID == "" || !r.Snapshot.Contains(r.ChallengerAgentID) {
		return deny(EligibilityCandidateNotInSnapshot)
	}
	// Every non-budget, non-allocation gate passed - this decision was
	// genuinely eligible for experimentation, independent of whatever
	// allocation/budget decide next.
	c.mutateCounters(ctx, r.ExperimentID, r.Now, func(e *Experiment) { e.EligibleDecisions++ })
	if AllocationBucket(r.ExperimentID, r.DecisionID) >= e.Definition.AllocationPercent {
		// The decision was fully eligible; deterministic allocation simply
		// did not select the Challenger this time. This is NOT an
		// eligibility failure - it is the expected outcome for the
		// (100-AllocationPercent)% of eligible decisions, so the
		// ReasonCode stays Eligible even though Champion is selected.
		_ = c.Store.Audit(ctx, ExperimentAudit{ExperimentID: r.ExperimentID, DecisionID: r.DecisionID, Event: "allocation_excluded", Reason: string(EligibilityEligible), At: r.Now})
		c.mutateCounters(ctx, r.ExperimentID, r.Now, func(e *Experiment) { e.ChampionRouted++ })
		return Admission{ExperimentID: r.ExperimentID, DecisionID: r.DecisionID, Selected: r.ChampionAgentID, Reason: "allocation did not select challenger", ReasonCode: EligibilityEligible}, nil
	}
	// Distinguish lifetime-budget exhaustion from in-flight-concurrency
	// limiting using the same thresholds ExperimentStore.Reserve itself
	// enforces, so the audit trail names the right cause even though the
	// reservation outcome (true/false) alone can't say which one fired.
	budgetCode := EligibilityBudgetExhausted
	if e.Definition.MaxConcurrent > 0 && e.Reserved-e.Consumed >= e.Definition.MaxConcurrent &&
		!(e.Definition.MaxExecutions > 0 && e.Reserved >= e.Definition.MaxExecutions) {
		budgetCode = EligibilityConcurrencyLimit
	}
	ok, err := c.Store.Reserve(ctx, r.ExperimentID, r.DecisionID, r.Now)
	if err != nil || !ok {
		return deny(budgetCode)
	}
	a := Admission{ExperimentID: r.ExperimentID, DecisionID: r.DecisionID, Selected: r.ChallengerAgentID, IsChallenger: true, Reservation: admissionKey(r.ExperimentID, r.DecisionID), Reason: "admitted", ReasonCode: EligibilityEligible, CreatedAt: r.Now}
	if err := c.Store.SaveAdmission(ctx, a); err != nil {
		_ = c.Store.Release(ctx, r.ExperimentID, r.DecisionID, r.Now)
		return Admission{Selected: r.ChampionAgentID, Reason: string(EligibilityControlPlaneUnavailable), ReasonCode: EligibilityControlPlaneUnavailable}, err
	}
	_ = c.Store.Audit(ctx, ExperimentAudit{ExperimentID: r.ExperimentID, DecisionID: r.DecisionID, Event: "admitted", Reason: "challenger selected after Core gates", At: r.Now})
	// An admitted Challenger now owes a real outcome - tracked as
	// "missing" until RecordChallengerExecutionOutcome observes it (at
	// execution time) or the reservation is released without ever
	// dispatching. This deliberately stays elevated if Executor's own
	// dispatch-time TOCTOU rejects the Challenger before ObserveExecution
	// ever runs (the documented residual gap) - that is exactly the
	// "missing outcome" this counter exists to surface.
	c.mutateCounters(ctx, r.ExperimentID, r.Now, func(e *Experiment) { e.MissingOutcomeCount++ })
	return a, nil
}

// mutateCounters applies an atomic read-mutate-write to the
// experiment's best-effort observability counters (see Experiment's doc
// comment) via ExperimentStore.UpdateSummary - never Save, which would
// blindly overwrite the budget ledger with a stale copy and risk
// clobbering a concurrent Reserve/Consume/Release (a real regression
// this package's own E2E concurrency test caught - see ADR 0011's
// completion report). Errors are swallowed: a failed counter update
// must never affect admission, budget, or control-plane correctness.
func (c *ExperimentController) mutateCounters(ctx context.Context, id ExperimentID, now time.Time, mutate func(*Experiment)) {
	_ = c.Store.UpdateSummary(ctx, id, func(e *Experiment) {
		mutate(e)
		e.UpdatedAt = now
	})
}

// RecordChallengerExecutionOutcome attaches the authoritative,
// execution-time ExecutionOutcome of an actually-dispatched Challenger
// to the experiment's summary counters, and evaluates the deterministic
// MaxChallengerFailures stop condition (Part 11/31/46). Only ever called
// for a decision where ExperimentController already admitted the
// Challenger (a.IsChallenger == true) - Champion outcomes are never
// routed here.
func (c *ExperimentController) RecordChallengerExecutionOutcome(ctx context.Context, id ExperimentID, outcome ExecutionOutcome, now time.Time) {
	c.mutateCounters(ctx, id, now, func(e *Experiment) {
		if e.MissingOutcomeCount > 0 {
			e.MissingOutcomeCount--
		}
		switch outcome {
		case ExecutionCompleted:
			e.ChallengerCompleted++
		case ExecutionTimedOut:
			e.ChallengerTimedOut++
		default:
			e.ChallengerFailed++
			e.TotalChallengerFailures++
		}
		if e.Definition.MaxChallengerFailures > 0 && e.TotalChallengerFailures >= e.Definition.MaxChallengerFailures && e.State == ExperimentEnabled {
			e.State = ExperimentStopped
		}
	})
	// Known, bounded residual gap: this Get-mutate-Save cycle is not a
	// single atomic store transaction, so two failures landing in the
	// same narrow window could both read the pre-stop state before
	// either write lands, delaying - never preventing - the eventual
	// Stopped transition by at most one extra admission window. This is
	// weaker than the budget ledger's hard atomicity guarantee
	// (Reserve/Consume/Release, which ARE single store-atomic calls) and
	// is an accepted tradeoff for this stop condition specifically.
	if outcome != ExecutionCompleted && outcome != ExecutionTimedOut {
		_ = c.Store.Audit(ctx, ExperimentAudit{ExperimentID: id, Event: "challenger_execution_failed", Reason: string(outcome), At: now})
	}
}

// RecordChallengerReviewOutcome attaches the later, independent
// QUALITY verdict (Verified/Rejected/Contested) of an actually-executed
// Challenger. Only Verified/Rejected/Contested are terminal for this
// counter set - Unreviewed/Unconfirmed/Supported/NeedsEvidence are not
// yet a final verdict and are intentionally not counted.
func (c *ExperimentController) RecordChallengerReviewOutcome(ctx context.Context, id ExperimentID, quality QualityOutcome, now time.Time) {
	switch quality {
	case QualityVerified:
		c.mutateCounters(ctx, id, now, func(e *Experiment) { e.ChallengerVerified++ })
	case QualityRejected:
		c.mutateCounters(ctx, id, now, func(e *Experiment) { e.ChallengerRejected++ })
	case QualityContested:
		c.mutateCounters(ctx, id, now, func(e *Experiment) { e.ChallengerContested++ })
	}
}

// Summary returns the deterministic Part 29 rollup for an experiment.
func (c *ExperimentController) Summary(ctx context.Context, id ExperimentID) (ExperimentSummary, error) {
	e, err := c.Store.Get(ctx, id)
	if err != nil {
		return ExperimentSummary{}, err
	}
	return ExperimentSummary{
		ExperimentID:             id,
		EligibleDecisionCount:    e.EligibleDecisions,
		ChampionExecutionCount:   e.ChampionRouted,
		ChallengerExecutionCount: e.Consumed,
		BudgetReserved:           e.Reserved,
		BudgetConsumed:           e.Consumed,
		ChallengerCompleted:      e.ChallengerCompleted,
		ChallengerFailed:         e.ChallengerFailed,
		ChallengerTimedOut:       e.ChallengerTimedOut,
		ChallengerVerified:       e.ChallengerVerified,
		ChallengerRejected:       e.ChallengerRejected,
		ChallengerContested:      e.ChallengerContested,
		MissingOutcomeCount:      e.MissingOutcomeCount,
		Status:                   e.State,
	}, nil
}

func (c *ExperimentController) Complete(ctx context.Context, a Admission, now time.Time) error {
	if !a.IsChallenger {
		return nil
	}
	return c.Store.Consume(ctx, a.ExperimentID, a.DecisionID, now)
}
func (c *ExperimentController) Cancel(ctx context.Context, a Admission, now time.Time) error {
	if !a.IsChallenger {
		return nil
	}
	return c.Store.Release(ctx, a.ExperimentID, a.DecisionID, now)
}

func (d ExperimentDefinition) Validate() error {
	if d.ID == "" || d.Tenant == "" || d.ChallengerAgentID == "" {
		return fmt.Errorf("experiment requires id, tenant, challenger")
	}
	if d.AllocationPercent < 0 || d.AllocationPercent > 100 {
		return fmt.Errorf("allocation percent out of range")
	}
	if d.AllocationSemantics != "" && d.AllocationSemantics != AllocationSemanticsV1 {
		return fmt.Errorf("unsupported allocation semantics")
	}
	return nil
}
