package supervisor

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodePlanResponse_Valid(t *testing.T) {
	raw := `{"proposals":[
		{"type":"create_task","task":{"id":"T1","required_capabilities":["review"]},"rationale_summary":"decompose"},
		{"type":"assign_task","task_id":"T1","agent_id":"codex","required_capabilities":["review"],"rationale_summary":"codex can review"}
	]}`
	proposals, err := decodePlanResponse("plan", []byte(raw), defaultWireLimits())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(proposals) != 2 {
		t.Fatalf("expected 2 proposals, got %d", len(proposals))
	}
	if _, ok := proposals[0].(CreateTaskProposal); !ok {
		t.Fatalf("expected CreateTaskProposal, got %T", proposals[0])
	}
	if _, ok := proposals[1].(AssignTaskProposal); !ok {
		t.Fatalf("expected AssignTaskProposal, got %T", proposals[1])
	}
}

func TestDecodeSynthesisResponse_Valid(t *testing.T) {
	raw := `{"task_ids":["T1","T2"],"summary":"T1 failed, T2 fixed it","rationale_summary":"cross-task synthesis"}`
	synth, err := decodeSynthesisResponse("synthesize", []byte(raw), defaultWireLimits())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(synth.TaskIDs) != 2 || synth.Summary == "" {
		t.Fatalf("unexpected synthesis: %+v", synth)
	}
}

func TestDecodePlanResponse_MalformedJSON(t *testing.T) {
	_, err := decodePlanResponse("plan", []byte(`{"proposals": [`), defaultWireLimits())
	var decodeErr *StructuredOutputDecodeFailure
	if !errors.As(err, &decodeErr) {
		t.Fatalf("expected StructuredOutputDecodeFailure, got %v (%T)", err, err)
	}
}

func TestDecodePlanResponse_UnknownProposalType(t *testing.T) {
	raw := `{"proposals":[{"type":"delete_everything","task_id":"T1","rationale_summary":"r"}]}`
	_, err := decodePlanResponse("plan", []byte(raw), defaultWireLimits())
	var schemaErr *StructuredOutputSchemaFailure
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected StructuredOutputSchemaFailure, got %v (%T)", err, err)
	}
}

func TestDecodePlanResponse_UnknownFieldRejected(t *testing.T) {
	raw := `{"proposals":[{"type":"assign_task","task_id":"T1","agent_id":"codex","rationale_summary":"r","sudo":true}]}`
	_, err := decodePlanResponse("plan", []byte(raw), defaultWireLimits())
	var decodeErr *StructuredOutputDecodeFailure
	if !errors.As(err, &decodeErr) {
		t.Fatalf("expected StructuredOutputDecodeFailure for unknown field, got %v (%T)", err, err)
	}
}

func TestDecodePlanResponse_OversizedRationale(t *testing.T) {
	long := strings.Repeat("x", MaxRationaleLength+1)
	raw := `{"proposals":[{"type":"assign_task","task_id":"T1","agent_id":"codex","rationale_summary":"` + long + `"}]}`
	_, err := decodePlanResponse("plan", []byte(raw), defaultWireLimits())
	var schemaErr *StructuredOutputSchemaFailure
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected StructuredOutputSchemaFailure for oversized rationale, got %v (%T)", err, err)
	}
}

func TestDecodePlanResponse_OversizedProposalCount(t *testing.T) {
	limits := defaultWireLimits()
	limits.MaxProposalsPerBatch = 1
	raw := `{"proposals":[
		{"type":"assign_task","task_id":"T1","agent_id":"codex","rationale_summary":"r1"},
		{"type":"assign_task","task_id":"T2","agent_id":"codex","rationale_summary":"r2"}
	]}`
	_, err := decodePlanResponse("plan", []byte(raw), limits)
	var schemaErr *StructuredOutputSchemaFailure
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected StructuredOutputSchemaFailure for oversized proposal count, got %v (%T)", err, err)
	}
}

func TestDecodePlanResponse_Truncated(t *testing.T) {
	raw := `{"proposals":[{"type":"assign_task","task_id":"T1","agent_id":"codex"`
	_, err := decodePlanResponse("plan", []byte(raw), defaultWireLimits())
	var decodeErr *StructuredOutputDecodeFailure
	if !errors.As(err, &decodeErr) {
		t.Fatalf("expected StructuredOutputDecodeFailure for truncated response, got %v (%T)", err, err)
	}
}

func TestDecodePlanResponse_DuplicateKeys(t *testing.T) {
	raw := `{"proposals":[{"type":"assign_task","task_id":"T1","task_id":"T2","agent_id":"codex","rationale_summary":"r"}]}`
	_, err := decodePlanResponse("plan", []byte(raw), defaultWireLimits())
	var decodeErr *StructuredOutputDecodeFailure
	if !errors.As(err, &decodeErr) {
		t.Fatalf("expected StructuredOutputDecodeFailure for duplicate keys, got %v (%T)", err, err)
	}
}

func TestDecodePlanResponse_TrailingData(t *testing.T) {
	raw := `{"proposals":[]}{"extra":true}`
	_, err := decodePlanResponse("plan", []byte(raw), defaultWireLimits())
	var decodeErr *StructuredOutputDecodeFailure
	if !errors.As(err, &decodeErr) {
		t.Fatalf("expected StructuredOutputDecodeFailure for trailing data, got %v (%T)", err, err)
	}
}

func TestDecodePlanResponse_MalformedID(t *testing.T) {
	raw := `{"proposals":[{"type":"assign_task","task_id":"../../etc/passwd","agent_id":"codex","rationale_summary":"r"}]}`
	_, err := decodePlanResponse("plan", []byte(raw), defaultWireLimits())
	var schemaErr *StructuredOutputSchemaFailure
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected StructuredOutputSchemaFailure for malformed id, got %v (%T)", err, err)
	}
}

func TestDecodePlanResponse_ResponseTooLarge(t *testing.T) {
	limits := defaultWireLimits()
	limits.MaxResponseBytes = 10
	raw := `{"proposals":[{"type":"assign_task","task_id":"T1","agent_id":"codex","rationale_summary":"r"}]}`
	_, err := decodePlanResponse("plan", []byte(raw), limits)
	var tooLarge *SupervisorResponseTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected SupervisorResponseTooLarge, got %v (%T)", err, err)
	}
}

func TestDecodePlanResponse_MissingRequiredFields(t *testing.T) {
	raw := `{"proposals":[{"type":"assign_task","rationale_summary":"r"}]}`
	_, err := decodePlanResponse("plan", []byte(raw), defaultWireLimits())
	var schemaErr *StructuredOutputSchemaFailure
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected StructuredOutputSchemaFailure for missing task_id/agent_id, got %v (%T)", err, err)
	}
}

func TestDecodePlanResponse_NeverPanics_ArbitraryStrings(t *testing.T) {
	inputs := []string{
		"",
		"not json",
		"{",
		"[]",
		"null",
		`{"proposals": null}`,
		`{"proposals": "not an array"}`,
		`{"proposals": [null]}`,
		`{"proposals": [{}]}`,
		strings.Repeat("{", 10000),
	}
	for _, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("decodePlanResponse panicked on input %q: %v", in, r)
				}
			}()
			_, _ = decodePlanResponse("plan", []byte(in), defaultWireLimits())
		}()
	}
}

func TestDetectDuplicateKeys_NestedObjects(t *testing.T) {
	// duplicate key inside a nested "task" object, not the top-level object
	raw := `{"proposals":[{"type":"create_task","task":{"id":"T1","id":"T2"},"rationale_summary":"r"}]}`
	if err := DetectDuplicateKeys([]byte(raw), 32); err == nil {
		t.Fatalf("expected a duplicate-key error for the nested task object")
	}
}

func TestDetectDuplicateKeys_ValidNesting(t *testing.T) {
	raw := `{"a":{"b":1,"c":[1,2,{"d":3}]},"e":2}`
	if err := DetectDuplicateKeys([]byte(raw), 32); err != nil {
		t.Fatalf("unexpected error on valid nested JSON: %v", err)
	}
}

func TestDetectDuplicateKeys_MaxDepthExceeded(t *testing.T) {
	// 40 levels of nested arrays, limit of 10
	raw := strings.Repeat("[", 40) + strings.Repeat("]", 40)
	if err := DetectDuplicateKeys([]byte(raw), 10); err == nil {
		t.Fatalf("expected a max-depth error")
	}
}
