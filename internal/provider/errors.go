package provider

import "fmt"

// Typed errors for the provider gateway. Each maps to both an HTTP status
// and a Responses-API-shaped error body (see errorToResponseError in
// server.go).

type BackendUnavailableError struct {
	Backend string
	Reason  string
}

func (e *BackendUnavailableError) Error() string {
	return fmt.Sprintf("backend %q is unavailable: %s", e.Backend, e.Reason)
}

type BackendTimeoutError struct {
	Backend string
}

func (e *BackendTimeoutError) Error() string {
	return fmt.Sprintf("backend %q timed out", e.Backend)
}

type BackendUnsupportedCapabilityError struct {
	Backend    string
	Capability string
}

func (e *BackendUnsupportedCapabilityError) Error() string {
	return fmt.Sprintf("backend %q does not support required capability %q", e.Backend, e.Capability)
}

type ProviderPolicyDeniedError struct {
	Reason string
}

func (e *ProviderPolicyDeniedError) Error() string {
	return fmt.Sprintf("ProviderPolicyDenied: %s", e.Reason)
}

// MeteredBackendDeniedError is returned whenever zero-credit policy would
// otherwise be bypassed - the single most important failure mode in this
// package. It must never be silently swallowed or downgraded to a
// fallback.
type MeteredBackendDeniedError struct {
	Backend string
	Reason  string
}

func (e *MeteredBackendDeniedError) Error() string {
	return fmt.Sprintf("MeteredBackendDenied: requested provider %q is forbidden by zero-credit policy: %s", e.Backend, e.Reason)
}

type PayloadTooLargeError struct {
	MaxBytes int
}

func (e *PayloadTooLargeError) Error() string {
	return fmt.Sprintf("request payload exceeds maximum of %d bytes", e.MaxBytes)
}

type RateLimitedError struct{}

func (e *RateLimitedError) Error() string { return "rate limit exceeded" }

type UnauthorizedError struct{}

func (e *UnauthorizedError) Error() string { return "unauthorized" }

type ForbiddenError struct {
	Reason string
}

func (e *ForbiddenError) Error() string { return fmt.Sprintf("forbidden: %s", e.Reason) }

type StreamInterruptedError struct {
	Reason string
}

func (e *StreamInterruptedError) Error() string {
	return fmt.Sprintf("stream interrupted: %s", e.Reason)
}

// ErrUnsupportedOfficialBackend is returned by SubscriptionInferenceBackend
// (subscription.go) for every call: there is currently no supported,
// documented API for driving inference through a ChatGPT subscription
// programmatically, and HarnessMesh will not implement one via browser
// automation, cookies, or undocumented endpoints. See docs/codex-provider.md.
type ErrUnsupportedOfficialBackend struct {
	Reason string
}

func (e *ErrUnsupportedOfficialBackend) Error() string {
	return fmt.Sprintf("chatgpt-subscription backend is not implemented: %s", e.Reason)
}
