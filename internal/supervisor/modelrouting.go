package supervisor

import "context"

// ModelRequirements describes what kind of model the Supervisor needs for
// a given call - never which model. Switchyard/modelrouting (ADR 0001)
// owns the concrete provider/model decision; the Supervisor only ever
// states requirements.
type ModelRequirements struct {
	StrongReasoning  bool
	StructuredOutput bool
	LongContext      bool
	ToolUseAware     bool
}

// ModelRoutingRequest is what the Supervisor asks its ModelRouter to
// resolve for one inference call.
type ModelRoutingRequest struct {
	Stage        string // "plan" | "replan" | "synthesize"
	Requirements ModelRequirements
	TaskID       TaskID // optional correlation, empty if not task-specific
}

// ModelRoutingDecision is what the ModelRouter returns. Model/Provider
// here identify the ROUTE the Supervisor will address its inference call
// to, not a Supervisor-chosen concrete model - the route is resolved to an
// actual provider/model entirely inside Switchyard/modelrouting,
// transparently to this package.
type ModelRoutingDecision struct {
	Provider string
	Model    string // the route alias to address, not a concrete model name
	RouteID  string
	Reason   string
}

// ModelRouter is the only way the Supervisor ever learns which route to
// use for an inference call. It never constructs a provider SDK client
// and never hardcodes a model name. Concrete implementations live outside
// this package (e.g. internal/modelrouting), preserving the one-way
// dependency direction established in Phase 2A's AgentDirectory adapter:
// the routing-aware package depends on this interface, this package
// depends on nothing provider-specific.
type ModelRouter interface {
	Route(ctx context.Context, req ModelRoutingRequest) (ModelRoutingDecision, error)
}

// InferenceResult is the raw output of one inference call. Content is
// UNTRUSTED model output and must be strictly decoded (see wire.go) before
// it can become an AgentProposal or Synthesis. ServedModel/ServedProvider
// are observability-only: an echo of what the backend actually used, never
// something the Supervisor requested or can control.
type InferenceResult struct {
	Content        string
	ServedModel    string
	ServedProvider string
}

// InferenceTransport sends a bounded prompt to the routed destination and
// returns raw output. It performs no decoding, no validation, and carries
// no authority - it is purely a transport.
type InferenceTransport interface {
	Complete(ctx context.Context, decision ModelRoutingDecision, systemPrompt, userPrompt string, maxResponseBytes int) (InferenceResult, error)
}

// FakeModelRouter is a deterministic ModelRouter for tests. It never calls
// Switchyard or any provider.
type FakeModelRouter struct {
	RouteFunc func(ctx context.Context, req ModelRoutingRequest) (ModelRoutingDecision, error)
}

func (f *FakeModelRouter) Route(ctx context.Context, req ModelRoutingRequest) (ModelRoutingDecision, error) {
	if f.RouteFunc == nil {
		return ModelRoutingDecision{Provider: "fake", Model: "fake-route", RouteID: "fake-route"}, nil
	}
	return f.RouteFunc(ctx, req)
}

var _ ModelRouter = (*FakeModelRouter)(nil)

// FakeInferenceTransport is a deterministic InferenceTransport for tests.
// It never makes a network call.
type FakeInferenceTransport struct {
	CompleteFunc func(ctx context.Context, decision ModelRoutingDecision, systemPrompt, userPrompt string, maxResponseBytes int) (InferenceResult, error)
}

func (f *FakeInferenceTransport) Complete(ctx context.Context, decision ModelRoutingDecision, systemPrompt, userPrompt string, maxResponseBytes int) (InferenceResult, error) {
	if f.CompleteFunc == nil {
		return InferenceResult{}, nil
	}
	return f.CompleteFunc(ctx, decision, systemPrompt, userPrompt, maxResponseBytes)
}

var _ InferenceTransport = (*FakeInferenceTransport)(nil)

// AgentContextSummary is the minimized, read-only agent information the
// Supervisor's prompt may include. Supplying this to the model implies no
// authority whatsoever: the Validator/Authorizer/Executor independently
// re-check everything against the real AgentDirectory regardless of what
// the model was told.
type AgentContextSummary struct {
	AgentID      AgentID
	Capabilities []string
}

// AgentContextProvider supplies a minimized, relevant slice of the agent
// registry for prompt construction. It is read-only context, never an
// authorization path.
type AgentContextProvider interface {
	RelevantAgents(ctx context.Context, state ExecutionState) ([]AgentContextSummary, error)
}

// FakeAgentContextProvider is a deterministic AgentContextProvider for
// tests.
type FakeAgentContextProvider struct {
	Agents []AgentContextSummary
}

func (f *FakeAgentContextProvider) RelevantAgents(ctx context.Context, state ExecutionState) ([]AgentContextSummary, error) {
	return f.Agents, nil
}

var _ AgentContextProvider = (*FakeAgentContextProvider)(nil)
