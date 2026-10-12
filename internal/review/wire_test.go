package review

import (
	"strings"
	"testing"
)

func TestDecodeArbitrationResponse_Valid(t *testing.T) {
	raw := `{"claim_id":"RG-1","assessment":"supported","proposals":[{"type":"recommend_verification","group_id":"RG-1","rationale_summary":"two independent confirms"}],"rationale_summary":"looks solid"}`
	proposal, err := DecodeArbitrationResponse([]byte(raw), "RG-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(proposal.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(proposal.Actions))
	}
	if _, ok := proposal.Actions[0].(RecommendVerificationAction); !ok {
		t.Fatalf("expected RecommendVerificationAction, got %T", proposal.Actions[0])
	}
}

func TestDecodeArbitrationResponse_RejectsVerifiedAssessment(t *testing.T) {
	raw := `{"claim_id":"RG-1","assessment":"verified","proposals":[],"rationale_summary":"r"}`
	_, err := DecodeArbitrationResponse([]byte(raw), "RG-1")
	if err == nil {
		t.Fatalf("expected an error: assessment must never be able to claim 'verified' directly")
	}
}

func TestDecodeArbitrationResponse_RejectsRejectedAssessment(t *testing.T) {
	raw := `{"claim_id":"RG-1","assessment":"rejected","proposals":[],"rationale_summary":"r"}`
	_, err := DecodeArbitrationResponse([]byte(raw), "RG-1")
	if err == nil {
		t.Fatalf("expected an error: assessment must never be able to claim 'rejected' directly")
	}
}

func TestDecodeArbitrationResponse_UnknownFieldRejected(t *testing.T) {
	raw := `{"claim_id":"RG-1","assessment":"unconfirmed","proposals":[],"rationale_summary":"r","model":"gpt-5.6"}`
	_, err := DecodeArbitrationResponse([]byte(raw), "RG-1")
	if err == nil {
		t.Fatalf("expected an error: a model-selection attempt must be rejected as an unknown field")
	}
}

func TestDecodeArbitrationResponse_MismatchedClaimIDRejected(t *testing.T) {
	raw := `{"claim_id":"RG-999","assessment":"unconfirmed","proposals":[],"rationale_summary":"r"}`
	_, err := DecodeArbitrationResponse([]byte(raw), "RG-1")
	if err == nil {
		t.Fatalf("expected an error: claim_id must match the requested group")
	}
}

func TestDecodeArbitrationResponse_UnknownProposalTypeRejected(t *testing.T) {
	raw := `{"claim_id":"RG-1","assessment":"unconfirmed","proposals":[{"type":"grant_capability","group_id":"RG-1","rationale_summary":"r"}],"rationale_summary":"r"}`
	_, err := DecodeArbitrationResponse([]byte(raw), "RG-1")
	if err == nil {
		t.Fatalf("expected an error: unknown/capability-granting proposal types must be rejected")
	}
}

func TestDecodeArbitrationResponse_DuplicateKeysRejected(t *testing.T) {
	raw := `{"claim_id":"RG-1","claim_id":"RG-2","assessment":"unconfirmed","proposals":[],"rationale_summary":"r"}`
	_, err := DecodeArbitrationResponse([]byte(raw), "RG-1")
	if err == nil {
		t.Fatalf("expected an error for duplicate keys")
	}
}

func TestDecodeArbitrationResponse_TrailingDataRejected(t *testing.T) {
	raw := `{"claim_id":"RG-1","assessment":"unconfirmed","proposals":[],"rationale_summary":"r"}{"extra":true}`
	_, err := DecodeArbitrationResponse([]byte(raw), "RG-1")
	if err == nil {
		t.Fatalf("expected an error for trailing data")
	}
}

func TestDecodeArbitrationResponse_OversizedRationaleRejected(t *testing.T) {
	long := strings.Repeat("x", MaxRationaleLength+1)
	raw := `{"claim_id":"RG-1","assessment":"unconfirmed","proposals":[],"rationale_summary":"` + long + `"}`
	_, err := DecodeArbitrationResponse([]byte(raw), "RG-1")
	if err == nil {
		t.Fatalf("expected an error for oversized rationale_summary")
	}
}

func TestDecodeArbitrationResponse_MalformedObservationIDRejected(t *testing.T) {
	raw := `{"claim_id":"RG-1","assessment":"unconfirmed","proposals":[{"type":"support_claim","group_id":"RG-1","observation_id":"../../etc/passwd","rationale_summary":"r"}],"rationale_summary":"r"}`
	_, err := DecodeArbitrationResponse([]byte(raw), "RG-1")
	if err == nil {
		t.Fatalf("expected an error for a malformed observation_id")
	}
}

func TestDecodeArbitrationResponse_NeverPanics(t *testing.T) {
	inputs := []string{
		"", "not json", "{", "[]", "null",
		`{"proposals": null}`, `{"proposals": "x"}`, `{"proposals":[null]}`, `{"proposals":[{}]}`,
		strings.Repeat("{", 10000),
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("DecodeArbitrationResponse panicked on input %q: %v", in, r)
				}
			}()
			_, _ = DecodeArbitrationResponse([]byte(in), "RG-1")
		}()
	}
}
