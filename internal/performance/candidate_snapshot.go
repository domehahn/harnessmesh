package performance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// CandidateSnapshot is an immutable, bounded record of exactly which
// agents were eligible for ONE ROUTING DECISION, AFTER tenant
// filtering, policy/capability eligibility filtering, and availability
// filtering have already been applied by Core (ADR 0006 Phase 7C's
// eligibleCandidates). A Challenger (or a Replayer) is only ever given
// a CandidateSnapshot, never a live registry handle - this is what
// makes "Challenger can never see an agent Core already removed"
// structural rather than conventional: there is no code path anywhere
// in this package that queries a live supervisor.AgentCatalog.
//
// DecisionID (ADR 0009D) is the snapshot's PERSISTENCE identity -
// TaskID is retained as a queryable, fachlich grouping attribute (one
// task may have many decisions over its lifetime: retries, reselection
// after a failure, etc.) but is explicitly NOT the primary identity of
// a decision context anymore. DecisionID is Core-owned exclusively
// (supervisor.Scheduler.selectAgent generates it via NewDecisionID) -
// never supplied by a caller, Challenger, or agent.
//
// AgentIDs is stored as plain strings (not supervisor.AgentID) only to
// keep this type free of any dependency that could tempt a future
// change to resolve IDs back against a live directory - callers convert
// at the boundary (see Contains/AgentCandidates).
type CandidateSnapshot struct {
	DecisionID supervisor.DecisionID
	Tenant     string
	TaskID     supervisor.TaskID
	TaskClass  TaskClass
	CapturedAt time.Time
	AgentIDs   []string
}

// ErrSnapshotTooLarge is returned by NewCandidateSnapshot when the
// caller's candidate list exceeds MaxCandidatesPerSnapshot - a resource-
// abuse guard (ADR 0008 Part 20/Limits), not a correctness concern.
var ErrSnapshotTooLarge = errors.New("candidate snapshot exceeds the maximum allowed candidate count")

// ErrMissingDecisionID is returned by NewCandidateSnapshot when
// decisionID is empty - a CandidateSnapshot without a Core-owned
// DecisionID has no persistence identity at all (ADR 0009D) and must
// never be constructed.
var ErrMissingDecisionID = errors.New("candidate snapshot requires a non-empty, Core-owned DecisionID")

// NewCandidateSnapshot builds a deterministic, deduplicated, sorted
// snapshot from an already-eligibility-filtered candidate list (e.g.
// supervisor.Scheduler's own eligibleCandidates output, or a historical
// replay source). Never re-derives eligibility itself - whatever
// `candidates` contains IS the snapshot, verbatim (deduplicated/sorted
// only for determinism, never filtered further). decisionID must be
// Core-owned (supervisor.NewDecisionID) - never caller-, Challenger-,
// or agent-supplied.
func NewCandidateSnapshot(decisionID supervisor.DecisionID, tenant string, taskID supervisor.TaskID, taskClass TaskClass, candidates []supervisor.AgentCandidate, capturedAt time.Time, limits EvaluationLimits) (CandidateSnapshot, error) {
	if decisionID == "" {
		return CandidateSnapshot{}, ErrMissingDecisionID
	}
	if len(candidates) > limits.maxCandidatesPerSnapshot() {
		return CandidateSnapshot{}, ErrSnapshotTooLarge
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		id := string(c.AgentID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return CandidateSnapshot{DecisionID: decisionID, Tenant: tenant, TaskID: taskID, TaskClass: taskClass, CapturedAt: capturedAt, AgentIDs: ids}, nil
}

// Contains reports whether agentID is present in the snapshot - the
// ONE check that makes "a recommendation outside the snapshot is
// InvalidRecommendation" enforceable.
func (s CandidateSnapshot) Contains(agentID supervisor.AgentID) bool {
	target := string(agentID)
	for _, id := range s.AgentIDs {
		if id == target {
			return true
		}
	}
	return false
}

// fingerprint derives a deterministic identity for this snapshot's
// CANDIDATE-SEMANTIC CONTENT only - deliberately EXCLUDING DecisionID
// (ADR 0009D's explicit "distinguish snapshot semantic content
// fingerprint from decision identity" instruction). Two DIFFERENT
// decisions for the same task that happen to see the identical eligible
// candidate universe at the same captured moment correctly produce the
// SAME fingerprint (that is a true fact about the candidate universe,
// not an identity collision - the records remain separated by
// DecisionID, their actual persistence key). Used both for
// RoutingEvaluationRecord's idempotency key (combined with DecisionID
// there, see ComputeEvaluationID) and as a content-equality check for
// Save's idempotent-replay-vs-conflict decision.
func (s CandidateSnapshot) fingerprint() string {
	h := sha256.New()
	h.Write([]byte(s.Tenant))
	h.Write([]byte{0})
	h.Write([]byte(s.TaskID))
	h.Write([]byte{0})
	h.Write([]byte(s.TaskClass))
	h.Write([]byte{0})
	h.Write([]byte(s.CapturedAt.UTC().Format(time.RFC3339Nano)))
	h.Write([]byte{0})
	for _, id := range s.AgentIDs {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CandidateSnapshotStore persists CandidateSnapshots keyed by
// (Tenant, DecisionID) - ADR 0009D's decision-scoped persistence
// identity. TaskID remains queryable (ListByTaskID) but is no longer
// the primary key: two decisions D1/D2 for the SAME TaskID, with
// different (or identical) candidate sets, both persist independently
// and never conflict merely because TaskID matches.
//
// A replay for a decision with no stored snapshot must report
// ReplayIncomplete, never substitute today's registry (see replay.go) -
// this store's absence of an entry is the signal that makes that
// possible.
type CandidateSnapshotStore interface {
	Save(ctx context.Context, snap CandidateSnapshot) error
	Get(ctx context.Context, tenant string, decisionID supervisor.DecisionID) (CandidateSnapshot, bool, error)
	// ListByTaskID returns every snapshot captured for taskID within
	// tenant, across however many distinct decisions that task has had
	// - the task-level audit view ADR 0009D Part 9/18 requires, without
	// making TaskID a primary identity anywhere in the store's key
	// schema.
	ListByTaskID(ctx context.Context, tenant string, taskID supervisor.TaskID) ([]CandidateSnapshot, error)
}

// DecisionSnapshotConflict is returned when a CandidateSnapshot is
// saved under an already-used (Tenant, DecisionID) key with DIFFERENT
// content - fails closed rather than silently overwriting a historical
// decision's snapshot, since that would corrupt that decision's
// "what was actually eligible" ground truth. A single DecisionID must
// represent exactly one immutable decision context forever.
type DecisionSnapshotConflict struct {
	Tenant     string
	DecisionID supervisor.DecisionID
}

func (e *DecisionSnapshotConflict) Error() string {
	return fmt.Sprintf("decision snapshot conflict for tenant %q decision %q - a different snapshot already exists for this decision", e.Tenant, e.DecisionID)
}

// MemoryCandidateSnapshotStore is a race-safe, deterministic in-memory
// CandidateSnapshotStore. Save is idempotent on an exact replay (same
// DecisionID, same content) and fails closed on a genuine identity
// conflict (same Tenant+DecisionID, different content) - mirroring the
// Save-idempotency discipline every other store in this package already
// uses.
type MemoryCandidateSnapshotStore struct {
	mu    sync.Mutex
	byKey map[string]CandidateSnapshot // (tenant, decisionID) -> snapshot
	// byTask is a secondary index only - DecisionID via byKey remains
	// the authoritative identity; this exists purely to answer
	// ListByTaskID without a linear scan.
	byTask map[string][]string // (tenant, taskID) -> []decisionKey
}

func NewMemoryCandidateSnapshotStore() *MemoryCandidateSnapshotStore {
	return &MemoryCandidateSnapshotStore{byKey: map[string]CandidateSnapshot{}, byTask: map[string][]string{}}
}

func decisionKey(tenant string, decisionID supervisor.DecisionID) string {
	return tenant + "\x00" + string(decisionID)
}

func taskKey(tenant string, taskID supervisor.TaskID) string {
	return tenant + "\x00" + string(taskID)
}

func (s *MemoryCandidateSnapshotStore) Save(ctx context.Context, snap CandidateSnapshot) error {
	if snap.DecisionID == "" {
		return ErrMissingDecisionID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := decisionKey(snap.Tenant, snap.DecisionID)
	if existing, ok := s.byKey[key]; ok {
		if existing.fingerprint() != snap.fingerprint() || existing.TaskID != snap.TaskID {
			return &DecisionSnapshotConflict{Tenant: snap.Tenant, DecisionID: snap.DecisionID}
		}
		return nil // exact replay of the same decision - safe no-op
	}
	s.byKey[key] = snap
	tk := taskKey(snap.Tenant, snap.TaskID)
	s.byTask[tk] = append(s.byTask[tk], key)
	return nil
}

func (s *MemoryCandidateSnapshotStore) Get(ctx context.Context, tenant string, decisionID supervisor.DecisionID) (CandidateSnapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.byKey[decisionKey(tenant, decisionID)]
	return snap, ok, nil
}

func (s *MemoryCandidateSnapshotStore) ListByTaskID(ctx context.Context, tenant string, taskID supervisor.TaskID) ([]CandidateSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := s.byTask[taskKey(tenant, taskID)]
	out := make([]CandidateSnapshot, 0, len(keys))
	for _, k := range keys {
		if snap, ok := s.byKey[k]; ok {
			out = append(out, snap)
		}
	}
	return out, nil
}

var _ CandidateSnapshotStore = (*MemoryCandidateSnapshotStore)(nil)
