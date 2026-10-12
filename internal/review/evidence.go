package review

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// EvidenceID identifies one EvidenceRecord. A string by itself is never
// trusted as proof of anything - it is a reference, resolved against the
// authoritative EvidenceStore before it can influence any review
// decision. See EvidenceValidator.
type EvidenceID string

// EvidenceKind reuses protocol.EvidenceType rather than introducing a
// parallel evidence-kind vocabulary - test_result/static_analysis/
// code_location/etc. already exist there and already cover what this
// phase needs.
type EvidenceKind = protocol.EvidenceType

// EvidenceRecordStatus is the record's own lifecycle, independent of any
// ReviewGroup's verdict.
type EvidenceRecordStatus string

const (
	EvidenceCurrent    EvidenceRecordStatus = "current"
	EvidenceSuperseded EvidenceRecordStatus = "superseded"
	EvidenceRevoked    EvidenceRecordStatus = "revoked"
)

// EvidenceRecord is the authoritative metadata for one piece of
// evidence. Every binding field (Tenant, Artifact, ArtifactVersion,
// TaskID, ExecutionID, ProducerAgentID) must be set from Core/execution
// context - never from an Arbiter's or any LLM's own output. There is no
// wire decoder anywhere in this package that can construct an
// EvidenceRecord from untrusted JSON; it is only ever built by Core code
// deriving it from a real supervisor.ExecutionResult (see
// internal/collaboration's adapter for the real, persisted path).
type EvidenceRecord struct {
	ID              EvidenceID
	Tenant          string
	Subject         SubjectRef
	Artifact        string // e.g. a repository-relative path or module name
	ArtifactVersion ArtifactVersion
	TaskID          supervisor.TaskID
	ExecutionID     string
	ProducerAgentID supervisor.AgentID
	Kind            EvidenceKind
	Digest          string // content/canonical-metadata digest, e.g. sha256:...
	Status          EvidenceRecordStatus
	SupersededBy    EvidenceID
	CreatedAt       time.Time
}

// EvidenceStore is the authoritative evidence persistence boundary.
// Put/Get/Exists mirror the request's example interface; a production
// implementation integrates with the repository's existing persistent
// store (internal/store.Store's SaveEvidence/GetEvidence, extended via
// protocol.EvidencePayload.Metadata - see
// internal/collaboration/evidence_adapter.go) rather than inventing a
// second persistence mechanism. MemoryEvidenceStore below is the
// deterministic implementation Phase 5's own test suite uses, exactly as
// FakeSupervisor/FakeArbiter stood in before their real counterparts.
type EvidenceStore interface {
	Put(ctx context.Context, record EvidenceRecord) error
	Get(ctx context.Context, id EvidenceID) (EvidenceRecord, error)
	Exists(ctx context.Context, id EvidenceID) (bool, error)
}

// MemoryEvidenceStore is a race-safe, deterministic, in-memory
// EvidenceStore for tests and for proving the evidence-validation
// boundary independent of any real persistence layer.
type MemoryEvidenceStore struct {
	mu      sync.Mutex
	records map[EvidenceID]EvidenceRecord
}

// NewMemoryEvidenceStore constructs an empty MemoryEvidenceStore.
func NewMemoryEvidenceStore() *MemoryEvidenceStore {
	return &MemoryEvidenceStore{records: map[EvidenceID]EvidenceRecord{}}
}

// Put is idempotent on exact duplicates (same ID, same Digest, same
// bindings) and fails closed on an identity conflict (same ID, different
// Digest) - see EvidenceIdentityConflict. This directly implements the
// requested replay/idempotency semantics: a retried evidence-ingestion
// call is a safe no-op; a genuinely different payload reusing an
// existing ID is rejected, never silently overwritten.
func (s *MemoryEvidenceStore) Put(ctx context.Context, record EvidenceRecord) error {
	if record.ID == "" {
		return &EvidenceIntegrityFailure{Reason: "evidence record has no ID"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.records[record.ID]
	if !ok {
		if record.CreatedAt.IsZero() {
			record.CreatedAt = time.Now().UTC()
		}
		if record.Status == "" {
			record.Status = EvidenceCurrent
		}
		s.records[record.ID] = record
		return nil
	}

	if existing.Digest != record.Digest {
		return &EvidenceIdentityConflict{ID: record.ID, ExistingDigest: existing.Digest, NewDigest: record.Digest}
	}
	if existing.Tenant != record.Tenant || existing.Artifact != record.Artifact || existing.ArtifactVersion != record.ArtifactVersion {
		return &EvidenceIdentityConflict{ID: record.ID, ExistingDigest: existing.Digest, NewDigest: record.Digest}
	}
	// Exact duplicate (replay) - safe no-op.
	return nil
}

func (s *MemoryEvidenceStore) Get(ctx context.Context, id EvidenceID) (EvidenceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return EvidenceRecord{}, &EvidenceNotFound{ID: id}
	}
	return r, nil
}

func (s *MemoryEvidenceStore) Exists(ctx context.Context, id EvidenceID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.records[id]
	return ok, nil
}

// Revoke marks an existing record revoked - a Core-only operation (not
// reachable from any Arbiter action type).
func (s *MemoryEvidenceStore) Revoke(id EvidenceID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return &EvidenceNotFound{ID: id}
	}
	r.Status = EvidenceRevoked
	s.records[id] = r
	return nil
}

// Supersede marks `old` superseded by `newID` - used when a later
// evidence record replaces an earlier one for the same binding (e.g. a
// re-run analysis).
func (s *MemoryEvidenceStore) Supersede(old, newID EvidenceID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[old]
	if !ok {
		return &EvidenceNotFound{ID: old}
	}
	r.Status = EvidenceSuperseded
	r.SupersededBy = newID
	s.records[old] = r
	return nil
}

var _ EvidenceStore = (*MemoryEvidenceStore)(nil)

// EvidenceExpectation is what an EvidenceValidator checks a resolved
// EvidenceRecord against. Fields left zero-valued are not checked
// (e.g. an empty ExpectedTaskID means "task binding not required for
// this check").
type EvidenceExpectation struct {
	Tenant          string
	ArtifactVersion ArtifactVersion
	TaskID          supervisor.TaskID // optional
	ExecutionID     string            // optional
	AcceptableKinds []EvidenceKind    // optional; empty means any kind accepted
	MaxAge          time.Duration     // optional; zero means no time-based freshness check
}

// EvidenceValidator is Core's authoritative evidence-validation
// boundary - the step between "a string reference exists in a proposal"
// and "this evidence actually supports the decision being made." Never
// trusts a reference by name alone.
type EvidenceValidator interface {
	Validate(ctx context.Context, id EvidenceID, expected EvidenceExpectation) (EvidenceRecord, error)
}

// CoreEvidenceValidator is the standard EvidenceValidator implementation,
// backed by an EvidenceStore. It always re-fetches from the store - it
// never trusts a cached/snapshotted EvidenceRecord, closing the TOCTOU
// window between when an Arbiter saw a reference and when Core actually
// applies a review transition based on it.
type CoreEvidenceValidator struct {
	Store EvidenceStore
}

// NewCoreEvidenceValidator constructs a CoreEvidenceValidator.
func NewCoreEvidenceValidator(store EvidenceStore) *CoreEvidenceValidator {
	return &CoreEvidenceValidator{Store: store}
}

func (v *CoreEvidenceValidator) Validate(ctx context.Context, id EvidenceID, expected EvidenceExpectation) (EvidenceRecord, error) {
	record, err := v.Store.Get(ctx, id)
	if err != nil {
		if notFound, ok := err.(*EvidenceNotFound); ok {
			return EvidenceRecord{}, notFound
		}
		return EvidenceRecord{}, &EvidenceNotFound{ID: id, Reason: err.Error()}
	}

	if expected.Tenant != "" && record.Tenant != expected.Tenant {
		return EvidenceRecord{}, &EvidenceTenantMismatch{ID: id, Expected: expected.Tenant, Actual: record.Tenant}
	}
	if expected.ArtifactVersion != "" && record.ArtifactVersion != expected.ArtifactVersion {
		return EvidenceRecord{}, &EvidenceVersionMismatch{ID: id, Expected: expected.ArtifactVersion, Actual: record.ArtifactVersion}
	}
	if expected.TaskID != "" && record.TaskID != expected.TaskID {
		return EvidenceRecord{}, &EvidenceExecutionMismatch{ID: id, Reason: fmt.Sprintf("task mismatch: expected %q, got %q", expected.TaskID, record.TaskID)}
	}
	if expected.ExecutionID != "" && record.ExecutionID != expected.ExecutionID {
		return EvidenceRecord{}, &EvidenceExecutionMismatch{ID: id, Reason: fmt.Sprintf("execution mismatch: expected %q, got %q", expected.ExecutionID, record.ExecutionID)}
	}
	if len(expected.AcceptableKinds) > 0 {
		ok := false
		for _, k := range expected.AcceptableKinds {
			if record.Kind == k {
				ok = true
				break
			}
		}
		if !ok {
			return EvidenceRecord{}, &EvidenceIntegrityFailure{Reason: fmt.Sprintf("evidence %q kind %q is not an acceptable kind %v", id, record.Kind, expected.AcceptableKinds)}
		}
	}
	if record.Digest == "" {
		return EvidenceRecord{}, &EvidenceIntegrityFailure{Reason: fmt.Sprintf("evidence %q has no integrity digest", id)}
	}

	switch record.Status {
	case EvidenceRevoked:
		return EvidenceRecord{}, &EvidenceRevokedError{ID: id}
	case EvidenceSuperseded:
		return EvidenceRecord{}, &EvidenceStaleError{ID: id, Reason: fmt.Sprintf("superseded by %q", record.SupersededBy)}
	}

	if expected.MaxAge > 0 && time.Since(record.CreatedAt) > expected.MaxAge {
		return EvidenceRecord{}, &EvidenceStaleError{ID: id, Reason: fmt.Sprintf("older than max age %s", expected.MaxAge)}
	}

	return record, nil
}

var _ EvidenceValidator = (*CoreEvidenceValidator)(nil)
