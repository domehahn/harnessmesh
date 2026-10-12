package collaboration

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/review"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// ProjectFindingStatus is the one real, Core-owned write path from
// internal/review's authoritative ReviewGroup verdict onto the
// corresponding live protocol.FindingPayload.Status (see
// review.FindingProjector and docs/decisions/0004's documented
// eventual-consistency model). It is a thin, additive wrapper over the
// store's existing UpdateFindingStatus - it does not reimplement finding
// mutation, and it does not touch SubmitFinding/Challenge/Resolve's
// existing conversational-lifecycle logic. Idempotent: writing the same
// status twice is a safe no-op from the caller's perspective (the SQL
// UPDATE simply re-sets the same value).
func (e *Engine) ProjectFindingStatus(ctx context.Context, sessionID, findingID string, status protocol.FindingStatus) error {
	if err := e.store.UpdateFindingStatus(ctx, sessionID, findingID, status); err != nil {
		return err
	}
	_ = e.store.EmitEvent(ctx, sessionID, "review.status_projected", map[string]any{
		"finding_id": findingID,
		"status":     string(status),
	})
	return nil
}

// EngineFindingProjector adapts Engine to review.FindingProjector - the
// real, persisted implementation, as opposed to review.FakeFindingProjector
// used by internal/review's own test suite. Mirrors the one-way
// dependency direction already established by SupervisorAgentDirectory:
// collaboration depends on review's interface; review depends on
// nothing from collaboration.
type EngineFindingProjector struct {
	engine *Engine
}

// NewEngineFindingProjector constructs the adapter over a live Engine.
func NewEngineFindingProjector(e *Engine) *EngineFindingProjector {
	return &EngineFindingProjector{engine: e}
}

func (p *EngineFindingProjector) Project(ctx context.Context, sessionID, findingID string, status protocol.FindingStatus) error {
	return p.engine.ProjectFindingStatus(ctx, sessionID, findingID, status)
}

var _ review.FindingProjector = (*EngineFindingProjector)(nil)

// EngineEvidenceStore adapts Engine's existing, persisted
// protocol.EvidencePayload storage (store.Store.SaveEvidence/GetEvidence)
// to review.EvidenceStore, so Phase 5's authoritative evidence-binding
// checks run against real persistence rather than an in-memory-only
// store, per ADR 0004's explicit instruction to integrate with existing
// persistence rather than invent a parallel evidence world.
//
// review.EvidenceRecord's Phase-5 binding fields (Tenant, Artifact,
// ArtifactVersion, TaskID, ExecutionID, ProducerAgentID, Digest, Status,
// SupersededBy) have no dedicated columns in the existing `evidence`
// SQLite table - adding them would require a schema migration. Instead,
// they round-trip through protocol.EvidencePayload.Metadata
// (map[string]any), which is already a persisted, JSON-encoded column.
// This reuses real, existing persistence without a migration and
// without a parallel store; a future phase that wants first-class
// columns for these fields can migrate the data already present in
// Metadata into them without changing this adapter's external
// behavior.
type EngineEvidenceStore struct {
	engine    *Engine
	sessionID string
}

// NewEngineEvidenceStore constructs the adapter, scoped to one
// collaboration session (protocol.EvidencePayload records are persisted
// per-session in the existing schema).
func NewEngineEvidenceStore(e *Engine, sessionID string) *EngineEvidenceStore {
	return &EngineEvidenceStore{engine: e, sessionID: sessionID}
}

type evidenceBindingMetadata struct {
	Tenant          string `json:"review_tenant,omitempty"`
	Artifact        string `json:"review_artifact,omitempty"`
	ArtifactVersion string `json:"review_artifact_version,omitempty"`
	TaskID          string `json:"review_task_id,omitempty"`
	ExecutionID     string `json:"review_execution_id,omitempty"`
	ProducerAgentID string `json:"review_producer_agent_id,omitempty"`
	Digest          string `json:"review_digest,omitempty"`
	Status          string `json:"review_status,omitempty"`
	SupersededBy    string `json:"review_superseded_by,omitempty"`
}

func (s *EngineEvidenceStore) Put(ctx context.Context, record review.EvidenceRecord) error {
	binding := evidenceBindingMetadata{
		Tenant: record.Tenant, Artifact: record.Artifact, ArtifactVersion: string(record.ArtifactVersion),
		TaskID: string(record.TaskID), ExecutionID: record.ExecutionID, ProducerAgentID: string(record.ProducerAgentID),
		Digest: record.Digest, Status: string(record.Status), SupersededBy: string(record.SupersededBy),
	}
	bindingJSON, err := json.Marshal(binding)
	if err != nil {
		return fmt.Errorf("encode evidence binding metadata: %w", err)
	}
	metadata := map[string]any{"review_binding": json.RawMessage(bindingJSON)}

	payload := &protocol.EvidencePayload{
		ID:          string(record.ID),
		SourceAgent: string(record.ProducerAgentID),
		Type:        record.Kind,
		CreatedAt:   record.CreatedAt,
		Metadata:    metadata,
	}
	return s.engine.store.SaveEvidence(ctx, s.sessionID, payload)
}

func (s *EngineEvidenceStore) Get(ctx context.Context, id review.EvidenceID) (review.EvidenceRecord, error) {
	all, err := s.engine.store.GetEvidence(ctx, s.sessionID)
	if err != nil {
		return review.EvidenceRecord{}, &review.EvidenceNotFound{ID: id, Reason: err.Error()}
	}
	for _, ev := range all {
		if ev.ID != string(id) {
			continue
		}
		return decodeEvidenceRecord(ev)
	}
	return review.EvidenceRecord{}, &review.EvidenceNotFound{ID: id}
}

func (s *EngineEvidenceStore) Exists(ctx context.Context, id review.EvidenceID) (bool, error) {
	_, err := s.Get(ctx, id)
	if err == nil {
		return true, nil
	}
	if _, ok := err.(*review.EvidenceNotFound); ok {
		return false, nil
	}
	return false, err
}

func decodeEvidenceRecord(ev protocol.EvidencePayload) (review.EvidenceRecord, error) {
	var binding evidenceBindingMetadata
	if raw, ok := ev.Metadata["review_binding"]; ok {
		rawBytes, err := json.Marshal(raw)
		if err == nil {
			_ = json.Unmarshal(rawBytes, &binding)
		}
	}
	status := review.EvidenceRecordStatus(binding.Status)
	if status == "" {
		status = review.EvidenceCurrent
	}
	return review.EvidenceRecord{
		ID:              review.EvidenceID(ev.ID),
		Tenant:          binding.Tenant,
		Artifact:        binding.Artifact,
		ArtifactVersion: review.ArtifactVersion(binding.ArtifactVersion),
		TaskID:          supervisor.TaskID(binding.TaskID),
		ExecutionID:     binding.ExecutionID,
		ProducerAgentID: supervisor.AgentID(binding.ProducerAgentID),
		Kind:            ev.Type,
		Digest:          binding.Digest,
		Status:          status,
		SupersededBy:    review.EvidenceID(binding.SupersededBy),
		CreatedAt:       ev.CreatedAt,
	}, nil
}

var _ review.EvidenceStore = (*EngineEvidenceStore)(nil)
