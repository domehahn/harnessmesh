package provider

import "context"

// Capabilities describes what a backend can actually do, so the gateway can
// fail clearly or degrade explicitly instead of pretending unsupported
// features exist.
type Capabilities struct {
	Streaming         bool
	Tools             bool
	ParallelToolCalls bool
	Reasoning         bool
	StructuredOutputs bool
	Vision            bool
	MaxContextTokens  int
	MaxOutputTokens   int
}

// InferenceBackend is the pluggable model-execution boundary. HarnessMesh's
// provider gateway (server.go) never talks to a model directly - it always
// goes through exactly one InferenceBackend, selected by explicit
// configuration (internal/config's ProviderGatewayConfig), never inferred
// from which credentials happen to be present in the environment.
type InferenceBackend interface {
	Name() string
	Type() string
	Capabilities() Capabilities
	Health(ctx context.Context) error

	// StreamResponse executes req and sends every resulting StreamEvent to
	// sink, in order, ending with exactly one of response.completed or
	// response.failed. It is the sole execution path - CreateResponse-style
	// "give me the whole thing at once" callers are served by wrapping this
	// in a buffering sink (see stream.go), so streaming is never faked by a
	// second, parallel non-streaming implementation that could drift from
	// the streaming one.
	StreamResponse(ctx context.Context, req Request, sink Sink) error
}
