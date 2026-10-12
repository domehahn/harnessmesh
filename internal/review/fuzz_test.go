package review

import (
	"context"
	"testing"
)

// FuzzDecodeArbitrationResponse feeds arbitrary bytes - standing in for
// adversarial or broken Arbiter LLM output - into the strict decode
// boundary. It must never panic and never return an action list
// alongside a non-nil error.
func FuzzDecodeArbitrationResponse(f *testing.F) {
	seeds := []string{
		`{"claim_id":"RG-1","assessment":"unconfirmed","proposals":[],"rationale_summary":"r"}`,
		`{"claim_id":"RG-1","assessment":"supported","proposals":[{"type":"recommend_verification","group_id":"RG-1","rationale_summary":"r"}],"rationale_summary":"r"}`,
		`{"claim_id":"RG-1","assessment":"verified","proposals":[],"rationale_summary":"r"}`,
		`{"claim_id":"RG-1","claim_id":"RG-2","assessment":"unconfirmed","proposals":[],"rationale_summary":"r"}`,
		`{"claim_id":`,
		`{}`,
		`null`,
		`[]`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("DecodeArbitrationResponse panicked on input %q: %v", data, r)
			}
		}()
		proposal, err := DecodeArbitrationResponse(data, "RG-1")
		if err != nil {
			if proposal.Actions != nil {
				t.Fatalf("DecodeArbitrationResponse returned both an error and non-nil actions for input %q", data)
			}
			return
		}
		for _, a := range proposal.Actions {
			if a == nil {
				t.Fatalf("DecodeArbitrationResponse returned a nil action for input %q", data)
			}
			if !knownActionTypes[a.ActionType()] {
				t.Fatalf("DecodeArbitrationResponse returned an unknown action variant %q for input %q", a.ActionType(), data)
			}
		}
	})
}

// FuzzEvidenceValidate feeds arbitrary strings as an EvidenceID into
// CoreEvidenceValidator.Validate - which must never panic, regardless of
// what garbage identifier string an untrusted caller supplies, and must
// never return a success result for an ID that was never Put into the
// store.
func FuzzEvidenceValidate(f *testing.F) {
	seeds := []string{
		"", "E1", "../../etc/passwd", "E1\x00E2", "E1\nE2",
		"a very long id " + string(make([]byte, 1000)),
		"🔥evidence🔥", "E1'; DROP TABLE evidence;--",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	store := NewMemoryEvidenceStore()
	store.Put(context.Background(), EvidenceRecord{ID: "E1", ArtifactVersion: "v1", Digest: "sha256:a"})
	validator := NewCoreEvidenceValidator(store)

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("CoreEvidenceValidator.Validate panicked on input %q: %v", data, r)
			}
		}()
		id := EvidenceID(string(data))
		record, err := validator.Validate(context.Background(), id, EvidenceExpectation{ArtifactVersion: "v1"})
		if err == nil && id != "E1" {
			t.Fatalf("Validate succeeded for an ID that was never Put: %q -> %+v", id, record)
		}
	})
}
