package review

import (
	"context"
	"errors"
	"testing"
	"time"
)

func mkRecord(id EvidenceID, tenant, artifact string, version ArtifactVersion, digest string) EvidenceRecord {
	return EvidenceRecord{
		ID: id, Tenant: tenant, Artifact: artifact, ArtifactVersion: version,
		Digest: digest, Kind: "test_result", Status: EvidenceCurrent, CreatedAt: time.Now().UTC(),
	}
}

func TestEvidenceStore_PutGetExists(t *testing.T) {
	store := NewMemoryEvidenceStore()
	ctx := context.Background()
	rec := mkRecord("E1", "tenant-a", "auth.go", "v1", "sha256:abc")
	if err := store.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	exists, err := store.Exists(ctx, "E1")
	if err != nil || !exists {
		t.Fatalf("expected E1 to exist, err=%v exists=%v", err, exists)
	}
	got, err := store.Get(ctx, "E1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Digest != "sha256:abc" {
		t.Fatalf("unexpected digest: %q", got.Digest)
	}
}

func TestEvidenceStore_ReplayIsIdempotent(t *testing.T) {
	store := NewMemoryEvidenceStore()
	ctx := context.Background()
	rec := mkRecord("E1", "tenant-a", "auth.go", "v1", "sha256:abc")
	if err := store.Put(ctx, rec); err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	if err := store.Put(ctx, rec); err != nil {
		t.Fatalf("expected identical replay to be a safe no-op, got: %v", err)
	}
}

func TestEvidenceStore_IdentityConflictOnDifferentDigest(t *testing.T) {
	store := NewMemoryEvidenceStore()
	ctx := context.Background()
	store.Put(ctx, mkRecord("E1", "tenant-a", "auth.go", "v1", "sha256:abc"))
	err := store.Put(ctx, mkRecord("E1", "tenant-a", "auth.go", "v1", "sha256:DIFFERENT"))
	var want *EvidenceIdentityConflict
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceIdentityConflict for same ID/different digest (tampered content), got %v (%T)", err, err)
	}
}

func TestEvidenceValidator_NonexistentRefFailsClosed(t *testing.T) {
	validator := NewCoreEvidenceValidator(NewMemoryEvidenceStore())
	_, err := validator.Validate(context.Background(), "E-NOPE", EvidenceExpectation{})
	var want *EvidenceNotFound
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceNotFound, got %v (%T)", err, err)
	}
}

func TestEvidenceValidator_TenantMismatch(t *testing.T) {
	store := NewMemoryEvidenceStore()
	store.Put(context.Background(), mkRecord("E1", "tenant-a", "auth.go", "v1", "sha256:abc"))
	validator := NewCoreEvidenceValidator(store)
	_, err := validator.Validate(context.Background(), "E1", EvidenceExpectation{Tenant: "tenant-b"})
	var want *EvidenceTenantMismatch
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceTenantMismatch, got %v (%T)", err, err)
	}
}

func TestEvidenceValidator_VersionMismatch(t *testing.T) {
	store := NewMemoryEvidenceStore()
	store.Put(context.Background(), mkRecord("E1", "tenant-a", "auth.go", "v1", "sha256:abc"))
	validator := NewCoreEvidenceValidator(store)
	_, err := validator.Validate(context.Background(), "E1", EvidenceExpectation{ArtifactVersion: "v2"})
	var want *EvidenceVersionMismatch
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceVersionMismatch, got %v (%T)", err, err)
	}
}

func TestEvidenceValidator_ExecutionMismatch(t *testing.T) {
	store := NewMemoryEvidenceStore()
	rec := mkRecord("E1", "tenant-a", "auth.go", "v1", "sha256:abc")
	rec.TaskID = "T1"
	store.Put(context.Background(), rec)
	validator := NewCoreEvidenceValidator(store)
	_, err := validator.Validate(context.Background(), "E1", EvidenceExpectation{TaskID: "T-OTHER"})
	var want *EvidenceExecutionMismatch
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceExecutionMismatch, got %v (%T)", err, err)
	}
}

func TestEvidenceValidator_RevokedFailsClosed(t *testing.T) {
	store := NewMemoryEvidenceStore()
	store.Put(context.Background(), mkRecord("E1", "tenant-a", "auth.go", "v1", "sha256:abc"))
	store.Revoke("E1")
	validator := NewCoreEvidenceValidator(store)
	_, err := validator.Validate(context.Background(), "E1", EvidenceExpectation{})
	var want *EvidenceRevokedError
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceRevokedError, got %v (%T)", err, err)
	}
}

func TestEvidenceValidator_SupersededFailsClosed(t *testing.T) {
	store := NewMemoryEvidenceStore()
	store.Put(context.Background(), mkRecord("E1", "tenant-a", "auth.go", "v1", "sha256:abc"))
	store.Put(context.Background(), mkRecord("E2", "tenant-a", "auth.go", "v1", "sha256:def"))
	store.Supersede("E1", "E2")
	validator := NewCoreEvidenceValidator(store)
	_, err := validator.Validate(context.Background(), "E1", EvidenceExpectation{})
	var want *EvidenceStaleError
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceStaleError for superseded evidence, got %v (%T)", err, err)
	}
}

func TestEvidenceValidator_MissingDigestFailsClosed(t *testing.T) {
	store := NewMemoryEvidenceStore()
	rec := mkRecord("E1", "tenant-a", "auth.go", "v1", "")
	store.Put(context.Background(), rec)
	validator := NewCoreEvidenceValidator(store)
	_, err := validator.Validate(context.Background(), "E1", EvidenceExpectation{})
	var want *EvidenceIntegrityFailure
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceIntegrityFailure for a record with no digest, got %v (%T)", err, err)
	}
}

func TestEvidenceValidator_UnacceptableKindRejected(t *testing.T) {
	store := NewMemoryEvidenceStore()
	store.Put(context.Background(), mkRecord("E1", "tenant-a", "auth.go", "v1", "sha256:abc"))
	validator := NewCoreEvidenceValidator(store)
	_, err := validator.Validate(context.Background(), "E1", EvidenceExpectation{AcceptableKinds: []EvidenceKind{"static_analysis"}})
	var want *EvidenceIntegrityFailure
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceIntegrityFailure for an unacceptable evidence kind, got %v (%T)", err, err)
	}
}

// --- Wiring into ReviewAuthorizer ---

func TestReviewAuthorizer_EvidenceValidation_UnknownRefFailsVerification(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	id, _ := state.Ingest(claim, "v1", "", FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E-NOPE"}})
	state.Ingest(claim, "v1", "", FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", ArtifactVersion: "v1", Stance: StanceConfirms})

	store := NewMemoryEvidenceStore()
	authz := NewReviewAuthorizerWithEvidence(NewCoreEvidenceValidator(store))
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), authz, state)

	_, err := applier.Apply(context.Background(), id, []ArbiterAction{
		RecommendVerificationAction{GroupID: id, RationaleSummary: "verify"},
	})
	var want *EvidenceNotFound
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceNotFound to fail the WHOLE verification attempt, got %v (%T)", err, err)
	}
}

func TestReviewAuthorizer_EvidenceValidation_ValidRefsAllowVerification(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	store := NewMemoryEvidenceStore()
	ctx := context.Background()
	store.Put(ctx, mkRecord("E1", "", "auth.go", "v1", "sha256:a"))
	store.Put(ctx, mkRecord("E2", "", "auth.go", "v1", "sha256:b"))

	id, _ := state.Ingest(claim, "v1", "", FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E1"}})
	state.Ingest(claim, "v1", "", FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E2"}})

	authz := NewReviewAuthorizerWithEvidence(NewCoreEvidenceValidator(store))
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), authz, state)

	group, err := applier.Apply(ctx, id, []ArbiterAction{
		RecommendVerificationAction{GroupID: id, RationaleSummary: "verify"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if group.Status != "verified" {
		t.Fatalf("expected verified, got %q", group.Status)
	}
}

// TestSecurity_EvidenceRevokedAfterArbitrationBeforeApply is the
// explicit TOCTOU test: the Arbiter "saw" valid evidence (conceptually -
// this test simulates the window by revoking AFTER ingestion but BEFORE
// the Apply call that would use it), and ReviewAuthorizer must re-fetch
// fresh from the store rather than trusting anything cached, so the
// revocation is honored.
func TestSecurity_EvidenceRevokedAfterArbitrationBeforeApply(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	store := NewMemoryEvidenceStore()
	ctx := context.Background()
	store.Put(ctx, mkRecord("E1", "", "auth.go", "v1", "sha256:a"))
	store.Put(ctx, mkRecord("E2", "", "auth.go", "v1", "sha256:b"))

	id, _ := state.Ingest(claim, "v1", "", FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E1"}})
	state.Ingest(claim, "v1", "", FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E2"}})

	// The Arbiter would have seen E1 as valid at the time it formed its
	// recommendation. Revoke it before Apply actually runs.
	store.Revoke("E1")

	authz := NewReviewAuthorizerWithEvidence(NewCoreEvidenceValidator(store))
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), authz, state)
	_, err := applier.Apply(ctx, id, []ArbiterAction{
		RecommendVerificationAction{GroupID: id, RationaleSummary: "verify using evidence the arbiter saw as valid"},
	})
	var want *EvidenceRevokedError
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceRevokedError (TOCTOU closed: re-fetched fresh, not trusted from when the Arbiter looked), got %v (%T)", err, err)
	}
}

func TestSecurity_ArbiterCannotFabricateEvidenceID(t *testing.T) {
	// WireAction has no field that could set/create evidence at all -
	// decode a payload that TRIES to smuggle one in and confirm it's
	// rejected as an unknown field.
	raw := `{"claim_id":"RG-1","assessment":"supported","proposals":[{"type":"recommend_verification","group_id":"RG-1","evidence_id":"E-FORGED","rationale_summary":"r"}],"rationale_summary":"r"}`
	_, err := DecodeArbitrationResponse([]byte(raw), "RG-1")
	if err == nil {
		t.Fatalf("expected an Arbiter-fabricated evidence_id field to be rejected as unknown")
	}
}

func TestSecurity_ArbiterReferencesEvidenceFromAnotherTenant(t *testing.T) {
	state := NewReviewState(nil)
	claim := ClaimInput{Subject: SubjectRef{Path: "auth.go"}, Category: "security", Line: 42}
	store := NewMemoryEvidenceStore()
	ctx := context.Background()
	store.Put(ctx, mkRecord("E1", "tenant-b", "auth.go", "v1", "sha256:a")) // belongs to a DIFFERENT tenant
	store.Put(ctx, mkRecord("E2", "tenant-a", "auth.go", "v1", "sha256:b"))

	id, _ := state.Ingest(claim, "v1", "tenant-a", FindingObservation{ID: "O1", FindingID: "F1", SourceAgentID: "agent-a", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E1"}})
	state.Ingest(claim, "v1", "tenant-a", FindingObservation{ID: "O2", FindingID: "F2", SourceAgentID: "agent-b", ArtifactVersion: "v1", Stance: StanceConfirms, EvidenceRefs: []string{"E2"}})

	authz := NewReviewAuthorizerWithEvidence(NewCoreEvidenceValidator(store))
	applier := NewReviewApplier(NewReviewValidator(DefaultReviewLimits()), authz, state)
	_, err := applier.Apply(ctx, id, []ArbiterAction{
		RecommendVerificationAction{GroupID: id, RationaleSummary: "verify"},
	})
	var want *EvidenceTenantMismatch
	if !errors.As(err, &want) {
		t.Fatalf("expected EvidenceTenantMismatch, got %v (%T)", err, err)
	}
}

func TestSecurity_TamperedEvidenceContentRejected(t *testing.T) {
	store := NewMemoryEvidenceStore()
	ctx := context.Background()
	original := mkRecord("E1", "tenant-a", "auth.go", "v1", "sha256:original")
	if err := store.Put(ctx, original); err != nil {
		t.Fatalf("Put: %v", err)
	}
	tampered := original
	tampered.Digest = "sha256:tampered"
	err := store.Put(ctx, tampered)
	var want *EvidenceIdentityConflict
	if !errors.As(err, &want) {
		t.Fatalf("expected tampered content (same ID, different digest) to be rejected, got %v (%T)", err, err)
	}
	got, _ := store.Get(ctx, "E1")
	if got.Digest != "sha256:original" {
		t.Fatalf("the ORIGINAL record must survive a rejected tamper attempt, got digest %q", got.Digest)
	}
}
