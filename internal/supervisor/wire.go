package supervisor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

// This file is the strict decode/validation boundary between untrusted
// LLM text output and this package's trusted domain types
// (AgentProposal, Synthesis). Nothing downstream of decodePlanResponse /
// decodeSynthesisResponse ever sees raw model text again - every failure
// mode here becomes a typed StructuredOutputDecodeFailure or
// StructuredOutputSchemaFailure, never a silently "repaired" action, and
// never a panic (see fuzz_test.go).

// idPattern bounds every task/agent ID the model may reference: non-empty,
// bounded length, restricted charset. A value that merely looks like an ID
// but fails this pattern is a StructuredOutputSchemaFailure, not a
// best-effort pass-through.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func ValidID(s string) bool { return idPattern.MatchString(s) }

// wireLimits bounds every array/string the model may return. These are
// Core-owned, not model-owned - the model cannot request a larger limit.
type wireLimits struct {
	MaxResponseBytes       int
	MaxProposalsPerBatch   int
	MaxTasksPerBatch       int
	MaxCapabilitiesPerTask int
	MaxDependenciesPerTask int
	MaxTaskIDsPerSynthesis int
	MaxRationaleLength     int
	MaxSummaryLength       int
	MaxJSONDepth           int
}

func defaultWireLimits() wireLimits {
	return wireLimits{
		MaxResponseBytes:       256 * 1024,
		MaxProposalsPerBatch:   32,
		MaxTasksPerBatch:       32,
		MaxCapabilitiesPerTask: 16,
		MaxDependenciesPerTask: 16,
		MaxTaskIDsPerSynthesis: 64,
		MaxRationaleLength:     MaxRationaleLength,
		MaxSummaryLength:       2000,
		MaxJSONDepth:           32,
	}
}

// WireTask is the untrusted wire representation of a Task.
type WireTask struct {
	ID                   string   `json:"id"`
	Type                 string   `json:"type,omitempty"`
	RequiredCapabilities []string `json:"required_capabilities,omitempty"`
	Dependencies         []string `json:"dependencies,omitempty"`
	Risk                 string   `json:"risk,omitempty"`
	Tenant               string   `json:"tenant,omitempty"`
	EstimatedCost        float64  `json:"estimated_cost,omitempty"`
}

// WireProposal is the untrusted wire representation of exactly one
// AgentProposal. Only the fields relevant to WireProposal.Type are
// expected to be populated; fields irrelevant to the given type are
// ignored by the converter (but still subject to the same size bounds).
type WireProposal struct {
	Type                 string     `json:"type"`
	TaskID               string     `json:"task_id,omitempty"`
	AgentID              string     `json:"agent_id,omitempty"`
	ReviewerAgentID      string     `json:"reviewer_agent_id,omitempty"`
	FromAgentID          string     `json:"from_agent_id,omitempty"`
	RequiredCapabilities []string   `json:"required_capabilities,omitempty"`
	Task                 *WireTask  `json:"task,omitempty"`
	NewTasks             []WireTask `json:"new_tasks,omitempty"`
	UpdatedTasks         []WireTask `json:"updated_tasks,omitempty"`
	TaskIDs              []string   `json:"task_ids,omitempty"`
	Reason               string     `json:"reason,omitempty"`
	RationaleSummary     string     `json:"rationale_summary"`
}

// WirePlanResponse is the top-level schema for both Plan and Replan model
// output.
type WirePlanResponse struct {
	Proposals []WireProposal `json:"proposals"`
}

// WireSynthesisResponse is the top-level schema for Synthesize model
// output. It deliberately carries no status/verified field: synthesis is
// structurally incapable of marking anything verified, at the wire level
// as well as the domain level (see TestSecurity_ModelIndependence-style
// checks in llm_supervisor_test.go).
type WireSynthesisResponse struct {
	TaskIDs          []string `json:"task_ids"`
	Summary          string   `json:"summary"`
	RationaleSummary string   `json:"rationale_summary"`
}

// decodeStrict performs the shared syntactic checks: size bound, no
// duplicate keys, no unknown fields, no trailing data. It's the common
// prefix of decodePlanResponse and decodeSynthesisResponse.
func decodeStrict(stage string, raw []byte, limits wireLimits, out any) error {
	if len(raw) > limits.MaxResponseBytes {
		return &SupervisorResponseTooLarge{Size: len(raw), Limit: limits.MaxResponseBytes}
	}
	if err := DetectDuplicateKeys(raw, limits.MaxJSONDepth); err != nil {
		return &StructuredOutputDecodeFailure{Stage: stage, Reason: err.Error()}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return &StructuredOutputDecodeFailure{Stage: stage, Reason: fmt.Sprintf("json decode: %v", err)}
	}
	if dec.More() {
		return &StructuredOutputDecodeFailure{Stage: stage, Reason: "trailing data after JSON value"}
	}
	return nil
}

// decodePlanResponse strictly decodes a Plan/Replan model response and
// converts every wire proposal into a trusted AgentProposal. Any
// syntactic or schema violation fails closed - no partial proposal list
// is ever returned alongside an error.
func decodePlanResponse(stage string, raw []byte, limits wireLimits) ([]AgentProposal, error) {
	var wire WirePlanResponse
	if err := decodeStrict(stage, raw, limits, &wire); err != nil {
		return nil, err
	}
	if len(wire.Proposals) > limits.MaxProposalsPerBatch {
		return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal count %d exceeds limit %d", len(wire.Proposals), limits.MaxProposalsPerBatch)}
	}

	proposals := make([]AgentProposal, 0, len(wire.Proposals))
	for i, w := range wire.Proposals {
		p, err := toDomainProposal(stage, i, w, limits)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, p)
	}
	return proposals, nil
}

// decodeSynthesisResponse strictly decodes a Synthesize model response.
func decodeSynthesisResponse(stage string, raw []byte, limits wireLimits) (Synthesis, error) {
	var wire WireSynthesisResponse
	if err := decodeStrict(stage, raw, limits, &wire); err != nil {
		return Synthesis{}, err
	}
	if len(wire.TaskIDs) > limits.MaxTaskIDsPerSynthesis {
		return Synthesis{}, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("task_ids count %d exceeds limit %d", len(wire.TaskIDs), limits.MaxTaskIDsPerSynthesis)}
	}
	if len(wire.Summary) > limits.MaxSummaryLength {
		return Synthesis{}, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("summary length %d exceeds limit %d", len(wire.Summary), limits.MaxSummaryLength)}
	}
	if len(wire.RationaleSummary) > limits.MaxRationaleLength {
		return Synthesis{}, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("rationale_summary length %d exceeds limit %d", len(wire.RationaleSummary), limits.MaxRationaleLength)}
	}
	taskIDs := make([]TaskID, 0, len(wire.TaskIDs))
	for _, id := range wire.TaskIDs {
		if !ValidID(id) {
			return Synthesis{}, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("malformed task id %q", id)}
		}
		taskIDs = append(taskIDs, TaskID(id))
	}
	return Synthesis{
		TaskIDs:          taskIDs,
		Summary:          wire.Summary,
		RationaleSummary: RationaleSummary(wire.RationaleSummary),
	}, nil
}

func toDomainTask(stage string, index int, w WireTask, limits wireLimits) (Task, error) {
	if !ValidID(w.ID) {
		return Task{}, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: malformed task id %q", index, w.ID)}
	}
	if len(w.RequiredCapabilities) > limits.MaxCapabilitiesPerTask {
		return Task{}, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: too many required_capabilities", index)}
	}
	if len(w.Dependencies) > limits.MaxDependenciesPerTask {
		return Task{}, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: too many dependencies", index)}
	}
	deps := make([]TaskID, 0, len(w.Dependencies))
	for _, d := range w.Dependencies {
		if !ValidID(d) {
			return Task{}, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: malformed dependency id %q", index, d)}
		}
		deps = append(deps, TaskID(d))
	}
	return Task{
		ID:                   TaskID(w.ID),
		Type:                 w.Type,
		RequiredCapabilities: append([]string(nil), w.RequiredCapabilities...),
		Dependencies:         deps,
		Risk:                 w.Risk,
		Tenant:               w.Tenant,
		EstimatedCost:        w.EstimatedCost,
	}, nil
}

// toDomainProposal is the explicit, testable conversion boundary: untrusted
// WireProposal JSON in, trusted AgentProposal out (or a typed schema
// failure - never a partial/best-guess value).
func toDomainProposal(stage string, index int, w WireProposal, limits wireLimits) (AgentProposal, error) {
	if len(w.RationaleSummary) > limits.MaxRationaleLength {
		return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: rationale_summary too long", index)}
	}
	rationale := RationaleSummary(w.RationaleSummary)

	switch ProposalType(w.Type) {
	case ProposalCreateTask:
		if w.Task == nil {
			return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: create_task requires task", index)}
		}
		t, err := toDomainTask(stage, index, *w.Task, limits)
		if err != nil {
			return nil, err
		}
		return CreateTaskProposal{Task: t, RationaleSummary: rationale}, nil

	case ProposalAssignTask:
		if !ValidID(w.TaskID) || !ValidID(w.AgentID) {
			return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: assign_task requires valid task_id and agent_id", index)}
		}
		if len(w.RequiredCapabilities) > limits.MaxCapabilitiesPerTask {
			return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: too many required_capabilities", index)}
		}
		return AssignTaskProposal{
			TaskID:           TaskID(w.TaskID),
			CandidateAgentID: AgentID(w.AgentID),
			RequiredCaps:     append([]string(nil), w.RequiredCapabilities...),
			RationaleSummary: rationale,
		}, nil

	case ProposalRequestReview:
		if !ValidID(w.TaskID) || !ValidID(w.ReviewerAgentID) {
			return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: request_review requires valid task_id and reviewer_agent_id", index)}
		}
		return RequestReviewProposal{TaskID: TaskID(w.TaskID), ReviewerAgentID: AgentID(w.ReviewerAgentID), RationaleSummary: rationale}, nil

	case ProposalRequestEvidence:
		if !ValidID(w.TaskID) || !ValidID(w.FromAgentID) {
			return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: request_evidence requires valid task_id and from_agent_id", index)}
		}
		return RequestEvidenceProposal{TaskID: TaskID(w.TaskID), FromAgentID: AgentID(w.FromAgentID), RationaleSummary: rationale}, nil

	case ProposalRetryTask:
		if !ValidID(w.TaskID) {
			return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: retry_task requires valid task_id", index)}
		}
		return RetryTaskProposal{TaskID: TaskID(w.TaskID), RationaleSummary: rationale}, nil

	case ProposalReplan:
		if len(w.NewTasks) > limits.MaxTasksPerBatch || len(w.UpdatedTasks) > limits.MaxTasksPerBatch {
			return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: too many new/updated tasks", index)}
		}
		newTasks := make([]Task, 0, len(w.NewTasks))
		for _, wt := range w.NewTasks {
			t, err := toDomainTask(stage, index, wt, limits)
			if err != nil {
				return nil, err
			}
			newTasks = append(newTasks, t)
		}
		updatedTasks := make([]Task, 0, len(w.UpdatedTasks))
		for _, wt := range w.UpdatedTasks {
			t, err := toDomainTask(stage, index, wt, limits)
			if err != nil {
				return nil, err
			}
			updatedTasks = append(updatedTasks, t)
		}
		return ReplanProposal{NewTasks: newTasks, UpdatedTasks: updatedTasks, RationaleSummary: rationale}, nil

	case ProposalSynthesize:
		if len(w.TaskIDs) > limits.MaxTaskIDsPerSynthesis {
			return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: too many task_ids", index)}
		}
		ids := make([]TaskID, 0, len(w.TaskIDs))
		for _, id := range w.TaskIDs {
			if !ValidID(id) {
				return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: malformed task id %q", index, id)}
			}
			ids = append(ids, TaskID(id))
		}
		return SynthesizeProposal{TaskIDs: ids, RationaleSummary: rationale}, nil

	case ProposalEscalate:
		if !ValidID(w.TaskID) {
			return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: escalate requires valid task_id", index)}
		}
		return EscalateProposal{TaskID: TaskID(w.TaskID), Reason: w.Reason, RationaleSummary: rationale}, nil

	default:
		return nil, &StructuredOutputSchemaFailure{Stage: stage, Reason: fmt.Sprintf("proposal %d: unknown proposal type %q", index, w.Type)}
	}
}

// DetectDuplicateKeys walks the raw JSON token stream and fails if any
// object in it (at any nesting depth) repeats a key - encoding/json
// silently lets the last occurrence win, which would let a model smuggle
// a second, conflicting value past a naive reviewer of the first. It also
// enforces maxDepth to bound recursion against adversarial deeply-nested
// input (defense in depth alongside the overall size limit).
func DetectDuplicateKeys(data []byte, maxDepth int) error {
	dec := json.NewDecoder(bytes.NewReader(data))

	type frame struct {
		isObject    bool
		awaitingKey bool
		keys        map[string]bool
	}
	var stack []*frame

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				if len(stack) > 0 {
					top := stack[len(stack)-1]
					if top.isObject {
						top.awaitingKey = true
					}
				}
				if len(stack)+1 > maxDepth {
					return fmt.Errorf("json nesting exceeds max depth %d", maxDepth)
				}
				stack = append(stack, &frame{isObject: t == '{', awaitingKey: t == '{', keys: map[string]bool{}})
			case '}', ']':
				if len(stack) == 0 {
					return fmt.Errorf("unbalanced JSON")
				}
				stack = stack[:len(stack)-1]
			}
		default:
			if len(stack) == 0 {
				continue
			}
			top := stack[len(stack)-1]
			if top.isObject {
				if top.awaitingKey {
					key, ok := t.(string)
					if !ok {
						return fmt.Errorf("expected string key, got %T", t)
					}
					if top.keys[key] {
						return fmt.Errorf("duplicate key %q", key)
					}
					top.keys[key] = true
					top.awaitingKey = false
				} else {
					top.awaitingKey = true
				}
			}
		}
	}
	return nil
}
