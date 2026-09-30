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

// ErrUnsupportedOfficialBackend is a typed error reserved for any inference
// mechanism HarnessMesh deliberately does not implement because no
// supported, documented, officially-sanctioned API exists for it -
// HarnessMesh will not implement one via browser automation, cookies, or
// undocumented endpoints, ever, for any backend. It is no longer returned
// by SubscriptionBackend (backend_subscription.go): as of this mission's
// research (2026-09-30), OpenAI does officially and publicly document
// "Sign in with ChatGPT" for exactly this use case - see
// docs/codex-provider.md and chatgpt_siwc.go's citations. The type remains
// defined for any future backend that turns out to have no official
// mechanism.
type ErrUnsupportedOfficialBackend struct {
	Reason string
}

func (e *ErrUnsupportedOfficialBackend) Error() string {
	return fmt.Sprintf("no official mechanism exists for this backend: %s", e.Reason)
}
