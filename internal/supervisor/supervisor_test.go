package supervisor

import (
	"context"
	"errors"
	"testing"
)

func TestFakeSupervisor_PlanReplanSynthesize(t *testing.T) {
	var sv Supervisor = &FakeSupervisor{
		PlanFunc: func(ctx context.Context, goal Goal, state ExecutionState) ([]AgentProposal, error) {
			return []AgentProposal{
				CreateTaskProposal{Task: Task{ID: "T1"}, RationaleSummary: "decompose goal"},
			}, nil
		},
		ReplanFunc: func(ctx context.Context, state ExecutionState, event ExecutionEvent) ([]AgentProposal, error) {
			return []AgentProposal{
				RetryTaskProposal{TaskID: event.TaskID, RationaleSummary: RationaleSummary("retry after " + string(event.Kind))},
			}, nil
		},
		SynthesizeFunc: func(ctx context.Context, state ExecutionState) (Synthesis, error) {
			return Synthesis{Summary: "done"}, nil
		},
	}

	proposals, err := sv.Plan(context.Background(), Goal{ID: "G1"}, ExecutionState{})
	if err != nil || len(proposals) != 1 {
		t.Fatalf("unexpected Plan result: %v, %v", proposals, err)
	}

	replanned, err := sv.Replan(context.Background(), ExecutionState{}, ExecutionEvent{Kind: EventAgentFailed, TaskID: "T1"})
	if err != nil || len(replanned) != 1 {
		t.Fatalf("unexpected Replan result: %v, %v", replanned, err)
	}

	synth, err := sv.Synthesize(context.Background(), ExecutionState{})
	if err != nil || synth.Summary != "done" {
		t.Fatalf("unexpected Synthesize result: %v, %v", synth, err)
	}
}

func TestFakeSupervisor_NilFuncsReturnEmpty(t *testing.T) {
	sv := &FakeSupervisor{}
	if p, err := sv.Plan(context.Background(), Goal{}, ExecutionState{}); err != nil || p != nil {
		t.Fatalf("expected nil, nil from unset PlanFunc, got %v, %v", p, err)
	}
}

func TestProposalType_AllConcreteTypesAreKnown(t *testing.T) {
	proposals := []AgentProposal{
		CreateTaskProposal{}, AssignTaskProposal{}, RequestReviewProposal{},
		RequestEvidenceProposal{}, RetryTaskProposal{}, ReplanProposal{},
		SynthesizeProposal{}, EscalateProposal{},
	}
	for _, p := range proposals {
		if !knownProposalTypes[p.ProposalType()] {
			t.Fatalf("%T's ProposalType() %q is not in knownProposalTypes", p, p.ProposalType())
		}
	}
}

// unknownProposal is a type outside the known union, used to prove the
// validator rejects closed on anything it does not explicitly recognize -
// including a well-formed AgentProposal implementation it has never seen.
type unknownProposal struct{}

func (unknownProposal) ProposalType() ProposalType { return "mystery_proposal" }

func TestValidateBatch_UnknownProposalTypeRejectedClosed(t *testing.T) {
	v := testValidator(map[AgentID][]string{}, DefaultSupervisorLimits())
	err := v.ValidateBatch(ExecutionState{}, []AgentProposal{unknownProposal{}})
	var want *ProposalValidationFailure
	if !errors.As(err, &want) {
		t.Fatalf("expected ProposalValidationFailure for unknown proposal type, got %v (%T)", err, err)
	}
}
