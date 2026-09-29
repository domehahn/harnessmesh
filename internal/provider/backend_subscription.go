package provider

import "context"

// SubscriptionBackend is the interface point for a future, officially
// supported ChatGPT-subscription inference backend. OpenAI does not
// currently publish a documented API for driving model inference through a
// ChatGPT subscription (as opposed to the metered OpenAI API or the MCP
// collaboration surface ChatGPT already uses as a peer - see
// docs/chatgpt-integration.md). HarnessMesh will not implement this via
// browser automation, cookies, DOM scraping, or undocumented chatgpt.com
// endpoints, so every method here returns ErrUnsupportedOfficialBackend
// until such an API exists. Implementing it then means filling in this
// type - the interface boundary (InferenceBackend) does not change.
type SubscriptionBackend struct {
	name string
}

func NewSubscriptionBackend(name string) *SubscriptionBackend {
	return &SubscriptionBackend{name: name}
}

func (b *SubscriptionBackend) Name() string { return b.name }
func (b *SubscriptionBackend) Type() string { return "chatgpt-subscription" }

func (b *SubscriptionBackend) Capabilities() Capabilities { return Capabilities{} }

func (b *SubscriptionBackend) Health(ctx context.Context) error {
	return &ErrUnsupportedOfficialBackend{Reason: "no documented ChatGPT-subscription inference API exists yet"}
}

func (b *SubscriptionBackend) StreamResponse(ctx context.Context, req Request, sink Sink) error {
	return &ErrUnsupportedOfficialBackend{Reason: "no documented ChatGPT-subscription inference API exists yet"}
}
