package supervisor

import "context"

// Supervisor plans, replans, and synthesizes - it never executes, never
// authorizes, and never selects a model/provider. Every method returns
// proposals; only HarnessMesh Core's Validator decides whether they are
// ever acted on. See ADR 0002.
type Supervisor interface {
	Plan(ctx context.Context, goal Goal, state ExecutionState) ([]AgentProposal, error)
	Replan(ctx context.Context, state ExecutionState, event ExecutionEvent) ([]AgentProposal, error)
	Synthesize(ctx context.Context, state ExecutionState) (Synthesis, error)
}

// FakeSupervisor is a deterministic, LLM-free Supervisor implementation for
// tests and for proving the Core/Supervisor trust boundary independent of
// any real model. It is intentionally simple: callers inject the exact
// proposals/synthesis they want returned, so tests can exercise the
// Validator against both well-formed and adversarial Supervisor output
// without depending on a real planner.
type FakeSupervisor struct {
	PlanFunc       func(ctx context.Context, goal Goal, state ExecutionState) ([]AgentProposal, error)
	ReplanFunc     func(ctx context.Context, state ExecutionState, event ExecutionEvent) ([]AgentProposal, error)
	SynthesizeFunc func(ctx context.Context, state ExecutionState) (Synthesis, error)
}

func (f *FakeSupervisor) Plan(ctx context.Context, goal Goal, state ExecutionState) ([]AgentProposal, error) {
	if f.PlanFunc == nil {
		return nil, nil
	}
	return f.PlanFunc(ctx, goal, state)
}

func (f *FakeSupervisor) Replan(ctx context.Context, state ExecutionState, event ExecutionEvent) ([]AgentProposal, error) {
	if f.ReplanFunc == nil {
		return nil, nil
	}
	return f.ReplanFunc(ctx, state, event)
}

func (f *FakeSupervisor) Synthesize(ctx context.Context, state ExecutionState) (Synthesis, error) {
	if f.SynthesizeFunc == nil {
		return Synthesis{}, nil
	}
	return f.SynthesizeFunc(ctx, state)
}

var _ Supervisor = (*FakeSupervisor)(nil)
