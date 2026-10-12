package review

import (
	"context"
	"fmt"
	"sync"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

// ReviewState is Core's authoritative, in-memory store of ReviewGroups -
// the review-layer analogue of supervisor.CoreState. It is the only
// place ReviewGroup.Status is ever mutated. Both Ingest (new
// observations) and Apply (Arbiter action batches) run under one lock,
// against state read fresh inside that same critical section, closing
// the same concurrent-duplicate/stale-validation window the Phase 2A/2B
// closure review found and fixed in supervisor.CoreState.applyGated.
type ReviewState struct {
	mu      sync.Mutex
	groups  map[ReviewGroupID]ReviewGroup
	nextSeq int
	persist func([]ReviewGroup) error
	// limits bounds Ingest-time growth (MaxIndependentReviews,
	// MaxReviewDepth) - Core-owned, defaulted to DefaultReviewLimits()
	// and overridable via SetLimits, never read from Arbiter output.
	limits ReviewLimits
}

// NewReviewState constructs an empty ReviewState with
// DefaultReviewLimits(). persist may be nil. Use SetLimits to configure
// different Ingest-time limits.
func NewReviewState(persist func([]ReviewGroup) error) *ReviewState {
	return &ReviewState{groups: map[ReviewGroupID]ReviewGroup{}, persist: persist, limits: DefaultReviewLimits()}
}

// SetLimits overrides the Ingest-time limits (MaxIndependentReviews,
// MaxReviewDepth) this ReviewState enforces. Safe to call before any
// concurrent Ingest/Apply activity begins; not safe to race against it.
func (s *ReviewState) SetLimits(limits ReviewLimits) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits = limits
}

// Snapshot returns a defensively-copied list of every ReviewGroup - safe
// to hand to an Arbiter (untrusted) without risking aliasing mutation.
func (s *ReviewState) Snapshot() []ReviewGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ReviewGroup, 0, len(s.groups))
	for _, g := range s.groups {
		out = append(out, cloneGroup(g))
	}
	return out
}

// SnapshotForTenant returns a defensively-copied list of every
// ReviewGroup belonging to exactly the given tenant - the tenant-scoped
// primitive flagged as missing in ADR 0003's residual risks. Snapshot()
// (unscoped, all tenants) remains available for genuinely cross-tenant
// callers (operator tooling, tests), but any tenant-scoped flow -
// including anything that might one day feed a Snapshot into an
// Arbiter's context - should use this instead. An empty tenant argument
// matches only groups with Tenant == "" (unscoped groups), not every
// group - callers must not pass "" expecting an unscoped superset.
func (s *ReviewState) SnapshotForTenant(tenant string) []ReviewGroup {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ReviewGroup
	for _, g := range s.groups {
		if g.Tenant == tenant {
			out = append(out, cloneGroup(g))
		}
	}
	return out
}

// Group returns one ReviewGroup by ID, defensively copied.
func (s *ReviewState) Group(id ReviewGroupID) (ReviewGroup, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.groups[id]
	if !ok {
		return ReviewGroup{}, false
	}
	return cloneGroup(g), true
}

func cloneGroup(g ReviewGroup) ReviewGroup {
	out := g
	out.Observations = append([]FindingObservation(nil), g.Observations...)
	for i := range out.Observations {
		out.Observations[i].EvidenceRefs = append([]string(nil), g.Observations[i].EvidenceRefs...)
		out.Observations[i].ConsumedFindingIDs = append([]string(nil), g.Observations[i].ConsumedFindingIDs...)
	}
	return out
}

// Ingest atomically correlates a new FindingObservation into an existing
// ReviewGroup (same fingerprint + artifact version + tenant) or creates
// a new one, then recomputes DeriveStatus. It never requires an Arbiter
// call - agreement/conflict/insufficient-evidence are deterministic, per
// ADR 0002 Phase 4.
func (s *ReviewState) Ingest(claim ClaimInput, version ArtifactVersion, tenant string, obs FindingObservation) (ReviewGroupID, error) {
	if obs.SourceAgentID == "" {
		return "", &FindingCorrelationFailure{FindingID: obs.FindingID, Reason: "observation has no source agent"}
	}
	if obs.ID == "" {
		return "", &FindingCorrelationFailure{FindingID: obs.FindingID, Reason: "observation has no ID"}
	}

	fingerprint := ComputeClaimFingerprint(claim)

	s.mu.Lock()
	defer s.mu.Unlock()

	var groups []ReviewGroup
	for _, g := range s.groups {
		groups = append(groups, g)
	}
	existing, found := FindGroup(groups, fingerprint, version, tenant)

	if found {
		for _, o := range existing.Observations {
			if o.ID == obs.ID {
				return "", &FindingCorrelationFailure{FindingID: obs.FindingID, Reason: fmt.Sprintf("duplicate observation ID %q (replay)", obs.ID)}
			}
		}
		if s.limits.MaxIndependentReviews > 0 {
			alreadyPresent := false
			for _, o := range existing.Observations {
				if o.SourceAgentID == obs.SourceAgentID {
					alreadyPresent = true
					break
				}
			}
			if !alreadyPresent {
				next := countDistinctAgents(existing) + 1
				if next > s.limits.MaxIndependentReviews {
					return "", &IndependentReviewLimitExceeded{Limit: s.limits.MaxIndependentReviews, Actual: next}
				}
			}
		}
		existing.Observations = append(existing.Observations, obs)
		existing.Status = DeriveStatus(existing)
		s.groups[existing.ID] = existing
		if err := s.persistLocked(); err != nil {
			return "", err
		}
		return existing.ID, nil
	}

	s.nextSeq++
	id := ReviewGroupID(fmt.Sprintf("RG-%d", s.nextSeq))
	group := ReviewGroup{
		ID:              id,
		Claim:           fingerprint,
		Subject:         claim.Subject,
		ArtifactVersion: version,
		Tenant:          tenant,
		Status:          protocol.FindingUnconfirmed,
		Observations:    []FindingObservation{obs},
	}
	group.Status = DeriveStatus(group)
	s.groups[id] = group
	if err := s.persistLocked(); err != nil {
		return "", err
	}
	return id, nil
}

// IngestSuccessor is like Ingest, but explicitly records that the new
// (or joined) group is a remediation successor of predecessorID -
// setting Depth = predecessor.Depth + 1 and enforcing MaxReviewDepth at
// creation time (ValidateActions also enforces it again before any
// Arbiter action on the resulting group, defense in depth). Used when a
// claim is being re-reviewed against a new artifact version produced by
// remediating the predecessor's claim - the new group's evidence/
// observations remain entirely distinct from the predecessor's (per
// ADR 0003/0004: "v2 evidence != v1 evidence", even when v2 remediates
// v1).
func (s *ReviewState) IngestSuccessor(predecessorID ReviewGroupID, claim ClaimInput, version ArtifactVersion, tenant string, obs FindingObservation) (ReviewGroupID, error) {
	s.mu.Lock()
	predecessor, ok := s.groups[predecessorID]
	limit := s.limits.MaxReviewDepth
	s.mu.Unlock()
	if !ok {
		return "", &FindingCorrelationFailure{FindingID: obs.FindingID, Reason: fmt.Sprintf("unknown predecessor group %q", predecessorID)}
	}
	depth := predecessor.Depth + 1
	if limit > 0 && depth > limit {
		return "", &ReviewDepthLimitExceeded{Limit: limit, Actual: depth}
	}

	id, err := s.Ingest(claim, version, tenant, obs)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[id]
	if g.PredecessorGroupID == "" {
		g.PredecessorGroupID = predecessorID
		g.Depth = depth
		s.groups[id] = g
	}
	return id, nil
}

func (s *ReviewState) persistLocked() error {
	if s.persist == nil {
		return nil
	}
	out := make([]ReviewGroup, 0, len(s.groups))
	for _, g := range s.groups {
		out = append(out, g)
	}
	return s.persist(out)
}

// ReviewApplier runs validate -> authorize -> atomic apply for one
// ReviewGroup's Arbiter action batch, mirroring supervisor.Applier
// exactly: a structurally valid batch is not yet authorized, and
// nothing is ever partially applied.
type ReviewApplier struct {
	Validator  *ReviewValidator
	Authorizer *ReviewAuthorizer
	State      *ReviewState
	// Projector, if non-nil, is called after a status change commits
	// (see Apply) to project it onto the corresponding
	// protocol.FindingPayload.Status. Projection happens AFTER the
	// review-state commit, never before and never atomically with it
	// (see docs/decisions/0004 for the documented eventual-consistency
	// model across the two stores) - a projection failure does not roll
	// back the review transition, and Project is idempotent so it is
	// always safe to retry.
	Projector FindingProjector
	// Observer, if non-nil, is notified of every committed status
	// change - the Core-owned hook ADR 0006 (Phase 7A) uses to finalize
	// a PerformanceObservation. It is called AFTER the review-state
	// commit, from authoritative ReviewGroup state only (never from
	// Arbiter output directly), and its method returns no error: a
	// finalization-observer failure can never roll back or alter the
	// already-committed review transition, exactly like Projector.
	Observer ReviewFinalizationObserver
	// FailureSink, if non-nil, is notified when Observer itself panics -
	// see ReviewObserverFailureSink. Wired automatically by WithObserver
	// when the attached observer also implements the sink interface.
	FailureSink ReviewObserverFailureSink
}

// ReviewFinalizationObserver is the Phase 7A hook interface - defined
// here (not in internal/performance) so internal/review does not depend
// on the observability package; internal/performance implements this
// interface instead, preserving the one-way dependency direction
// established throughout ADR 0002-0005.
type ReviewFinalizationObserver interface {
	ObserveReviewTransition(ctx context.Context, group ReviewGroup, previousStatus protocol.FindingStatus)
}

// ReviewObserverFailureSink is an optional, narrow hook ReviewApplier
// calls when ReviewFinalizationObserver.ObserveReviewTransition itself
// panics - ADR 0006 Phase 7C's observer-failure-visibility closure. Only
// ever receives a bounded classification, never the recovered value
// itself, so an arbitrary/sensitive panic payload can never reach
// telemetry unfiltered.
type ReviewObserverFailureSink interface {
	RecordReviewObserverPanic()
}

// NewReviewApplier constructs a ReviewApplier with explicit, required
// dependencies. Projector/Observer may be nil (no projection/observation
// configured).
func NewReviewApplier(v *ReviewValidator, a *ReviewAuthorizer, s *ReviewState) *ReviewApplier {
	return &ReviewApplier{Validator: v, Authorizer: a, State: s}
}

// WithProjector returns a copy of ap configured to project status
// changes through projector.
func (ap *ReviewApplier) WithProjector(projector FindingProjector) *ReviewApplier {
	out := *ap
	out.Projector = projector
	return &out
}

// WithObserver returns a copy of ap configured to notify observer of
// every committed status change. If observer also implements
// ReviewObserverFailureSink, it is automatically wired as the
// FailureSink too.
func (ap *ReviewApplier) WithObserver(observer ReviewFinalizationObserver) *ReviewApplier {
	out := *ap
	out.Observer = observer
	if sink, ok := observer.(ReviewObserverFailureSink); ok {
		out.FailureSink = sink
	}
	return &out
}

// Apply validates and authorizes actions against FRESH state read
// inside the lock, then commits any resulting status change atomically.
// A rejected batch leaves the group's state completely unchanged.
func (ap *ReviewApplier) Apply(ctx context.Context, groupID ReviewGroupID, actions []ArbiterAction) (ReviewGroup, error) {
	ap.State.mu.Lock()
	defer ap.State.mu.Unlock()

	current, ok := ap.State.groups[groupID]
	if !ok {
		return ReviewGroup{}, &ReviewValidationFailure{Reason: fmt.Sprintf("unknown review group %q", groupID)}
	}
	current = cloneGroup(current)

	if err := ap.Validator.ValidateActions(current, actions); err != nil {
		return current, err
	}

	work := cloneGroup(current)
	for _, action := range actions {
		status, err := ap.Authorizer.Authorize(ctx, work, action)
		if err != nil {
			return current, err
		}
		work.CostSpent += DefaultActionCost(action.ActionType())
		switch act := action.(type) {
		case RequestEvidenceAction:
			work.EvidenceRequests++
			work.Status = status
		case RecommendVerificationAction, RejectClaimAction:
			work.Status = status
		default:
			_ = act
		}
	}
	if len(actions) > 0 {
		work.ArbitrationRounds++
	}

	if ap.State.persist != nil {
		all := make([]ReviewGroup, 0, len(ap.State.groups))
		for id, g := range ap.State.groups {
			if id == groupID {
				all = append(all, work)
				continue
			}
			all = append(all, g)
		}
		if err := ap.State.persist(all); err != nil {
			return current, &ReviewValidationFailure{Reason: fmt.Sprintf("persistence failed, state unchanged: %v", err)}
		}
	}

	ap.State.groups[groupID] = work

	// Projection happens AFTER the review-state commit above, outside
	// any further mutation of ap.State - a projection failure is
	// reported but never undoes the already-committed review decision
	// (see Projector's doc comment / docs/decisions/0004).
	if ap.Projector != nil && work.Status != current.Status && work.SessionID != "" && work.FindingID != "" {
		if err := ap.Projector.Project(ctx, work.SessionID, work.FindingID, work.Status); err != nil {
			return cloneGroup(work), &FindingProjectionFailure{FindingID: work.FindingID, Reason: err.Error()}
		}
	}

	// Observer notification: same "after commit, never rolls anything
	// back" discipline as Projector above, and panic-isolated for the
	// same reason supervisor.Executor isolates its ExecutionObserver -
	// an observability hook must never be able to crash or corrupt an
	// already-authoritative state transition.
	if ap.Observer != nil && work.Status != current.Status {
		func() {
			defer func() {
				if r := recover(); r != nil && ap.FailureSink != nil {
					func() {
						defer func() { _ = recover() }() // the sink itself must never be able to crash Apply, recursively
						ap.FailureSink.RecordReviewObserverPanic()
					}()
				}
			}()
			ap.Observer.ObserveReviewTransition(ctx, cloneGroup(work), current.Status)
		}()
	}
	return cloneGroup(work), nil
}
