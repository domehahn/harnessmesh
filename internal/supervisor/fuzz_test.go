package supervisor

import "testing"

// FuzzDecodePlanResponse feeds arbitrary bytes - standing in for
// adversarial or simply broken LLM output - into the strict decode
// boundary. It must never panic, never return a non-empty proposal list
// alongside a non-nil error, and never produce a proposal whose
// ProposalType() is outside knownProposalTypes (an "unknown proposal
// variant" escaping decode would defeat the entire validation boundary
// downstream).
func FuzzDecodePlanResponse(f *testing.F) {
	seeds := []string{
		`{"proposals":[]}`,
		`{"proposals":[{"type":"assign_task","task_id":"T1","agent_id":"codex","rationale_summary":"r"}]}`,
		`{"proposals":[{"type":"create_task","task":{"id":"T1"},"rationale_summary":"r"}]}`,
		`{"proposals":[{"type":"replan","new_tasks":[{"id":"T1"}],"rationale_summary":"r"}]}`,
		`{"proposals":`,
		`{}`,
		`null`,
		`[]`,
		`{"proposals":[{"type":"assign_task","task_id":"T1","task_id":"T2","agent_id":"codex","rationale_summary":"r"}]}`,
		`{"proposals":[{"type":"unknown_type","rationale_summary":"r"}]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decodePlanResponse panicked on input %q: %v", data, r)
			}
		}()
		proposals, err := decodePlanResponse("plan", data, defaultWireLimits())
		if err != nil {
			if proposals != nil {
				t.Fatalf("decodePlanResponse returned both an error and a non-nil proposal list for input %q", data)
			}
			return
		}
		for _, p := range proposals {
			if p == nil {
				t.Fatalf("decodePlanResponse returned a nil proposal for input %q", data)
			}
			if !knownProposalTypes[p.ProposalType()] {
				t.Fatalf("decodePlanResponse returned an unknown proposal variant %q for input %q", p.ProposalType(), data)
			}
		}
	})
}

// FuzzDecodeSynthesisResponse is the Synthesize-path equivalent.
func FuzzDecodeSynthesisResponse(f *testing.F) {
	seeds := []string{
		`{"task_ids":[],"summary":"","rationale_summary":""}`,
		`{"task_ids":["T1","T2"],"summary":"done","rationale_summary":"r"}`,
		`{"task_ids":`,
		`{}`,
		`null`,
		`{"task_ids":["../etc/passwd"],"summary":"s","rationale_summary":"r"}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("decodeSynthesisResponse panicked on input %q: %v", data, r)
			}
		}()
		_, _ = decodeSynthesisResponse("synthesize", data, defaultWireLimits())
	})
}

// FuzzDetectDuplicateKeys exercises the token-stream walker directly with
// arbitrary bytes, since it is the lowest-level, most panic-sensitive part
// of the decode boundary (manual token/stack bookkeeping).
func FuzzDetectDuplicateKeys(f *testing.F) {
	seeds := []string{
		`{}`, `[]`, `{"a":1}`, `{"a":{"a":1}}`, `{"a":[1,2,3]}`,
		`{"a":1,"a":2}`, `[[[[[]]]]]`, `{`, `]`, `"str"`, `123`, `true`, `null`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("DetectDuplicateKeys panicked on input %q: %v", data, r)
			}
		}()
		_ = DetectDuplicateKeys(data, 32)
	})
}
