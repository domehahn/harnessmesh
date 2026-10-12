package review

import (
	"reflect"
	"strings"
	"testing"
)

func TestFindingProvenance_CoreOwnedFieldsSetFromExecutionContext(t *testing.T) {
	obs, err := NewFindingObservationFromExecution(
		ExecutionProvenance{Tenant: "tenant-a", TaskID: "T1", ExecutionID: "T1@abc123", AgentID: "agent-a"},
		FindingClaim{ID: "O1", FindingID: "F1", Stance: StanceConfirms},
	)
	if err != nil {
		t.Fatalf("NewFindingObservationFromExecution: %v", err)
	}
	if obs.SourceExecutionRef != "T1@abc123" {
		t.Fatalf("expected SourceExecutionRef from Core context, got %q", obs.SourceExecutionRef)
	}
	if obs.SourceExecutionID != "T1" {
		t.Fatalf("expected SourceExecutionID from Core context, got %q", obs.SourceExecutionID)
	}
	if obs.SourceAgentID != "agent-a" {
		t.Fatalf("expected SourceAgentID from Core context, got %q", obs.SourceAgentID)
	}
}

func TestNegative_MissingExecutionIDFailsClosed(t *testing.T) {
	_, err := NewFindingObservationFromExecution(
		ExecutionProvenance{Tenant: "tenant-a", TaskID: "T1", AgentID: "agent-a"}, // no ExecutionID
		FindingClaim{ID: "O1", FindingID: "F1"},
	)
	if err == nil {
		t.Fatalf("expected a fail-closed error for missing ExecutionID")
	}
	if _, ok := err.(*MissingExecutionProvenance); !ok {
		t.Fatalf("expected *MissingExecutionProvenance, got %T", err)
	}
}

func TestNegative_MissingTaskIDFailsClosed(t *testing.T) {
	_, err := NewFindingObservationFromExecution(
		ExecutionProvenance{Tenant: "tenant-a", ExecutionID: "x@1", AgentID: "agent-a"},
		FindingClaim{ID: "O1", FindingID: "F1"},
	)
	if err == nil {
		t.Fatalf("expected a fail-closed error for missing TaskID")
	}
}

func TestNegative_MissingAgentIDFailsClosed(t *testing.T) {
	_, err := NewFindingObservationFromExecution(
		ExecutionProvenance{Tenant: "tenant-a", TaskID: "T1", ExecutionID: "T1@1"},
		FindingClaim{ID: "O1", FindingID: "F1"},
	)
	if err == nil {
		t.Fatalf("expected a fail-closed error for missing AgentID")
	}
}

// TestNegative_ClaimCannotSmuggleProvenance is a structural proof: there
// is no field on FindingClaim named anything that could plausibly carry
// an execution/agent reference - an agent-supplied claim payload has
// nowhere to put a forged SourceExecutionRef/SourceExecutionID/
// SourceAgentID even if it tried.
func TestNegative_ClaimCannotSmuggleProvenance(t *testing.T) {
	typ := reflect.TypeOf(FindingClaim{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "execution") || strings.Contains(name, "sourceagent") {
			t.Fatalf("FindingClaim.%s could be used to smuggle a forged execution/agent reference - provenance must come only from ExecutionProvenance", typ.Field(i).Name)
		}
	}
}

func TestFindingProvenance_HasExecutionProvenance(t *testing.T) {
	withRef := FindingObservation{SourceExecutionRef: "x@1"}
	if !withRef.HasExecutionProvenance() {
		t.Fatalf("expected HasExecutionProvenance true when SourceExecutionRef is set")
	}
	withEvidence := FindingObservation{EvidenceRefs: []string{"EV1"}}
	if !withEvidence.HasExecutionProvenance() {
		t.Fatalf("expected HasExecutionProvenance true when EvidenceRefs is non-empty")
	}
	bare := FindingObservation{SourceExecutionID: "T1"} // bare TaskID alone is NOT sufficient
	if bare.HasExecutionProvenance() {
		t.Fatalf("expected HasExecutionProvenance false for a bare SourceExecutionID with no exact ref")
	}
}
