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

// MeteredBackendDeniedError is returned whenever zero-API-billing policy would
// otherwise be bypassed - the single most important failure mode in this
// package. It must never be silently swallowed or downgraded to a
// fallback.
type MeteredBackendDeniedError struct {
	Backend string
	Reason  string
}

func (e *MeteredBackendDeniedError) Error() string {
	return fmt.Sprintf("MeteredBackendDenied: requested provider %q is forbidden by zero_api_billing_mode: %s", e.Backend, e.Reason)
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

// The following typed errors map OpenAI's documented SIWC/ChatGPT-plan-usage
// structured error codes (developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery),
// so each documented failure mode is distinguishable in code and in tests,
// not collapsed into a generic 4xx/5xx.

// SubscriptionUserNotEligibleError maps subscription_sharing_user_not_eligible
// (HTTP 403): "ChatGPT plan usage is unavailable for the selected user,
// workspace, or policy." Documented recovery: explain the restriction; do
// not repeat the same request or loop through OAuth.
type SubscriptionUserNotEligibleError struct{ Message string }

func (e *SubscriptionUserNotEligibleError) Error() string {
	return fmt.Sprintf("subscription_sharing_user_not_eligible: %s", e.Message)
}

// SubscriptionUsageLimitExceededError maps
// subscription_sharing_usage_limit_exceeded (HTTP 429): the user's ChatGPT
// plan usage quota has been reached. Documented recovery: pause new
// requests; direct the user to ChatGPT settings -> Usage.
type SubscriptionUsageLimitExceededError struct{ Message string }

func (e *SubscriptionUsageLimitExceededError) Error() string {
	return fmt.Sprintf("subscription_sharing_usage_limit_exceeded: ChatGPT-plan usage limit reached; see ChatGPT settings -> Usage: %s", e.Message)
}

// SubscriptionUsageUnavailableError maps
// subscription_sharing_usage_unavailable (HTTP 503): "Usage availability
// could not be checked." Documented recovery: preserve credentials and
// retry later with bounded backoff.
type SubscriptionUsageUnavailableError struct{ Message string }

func (e *SubscriptionUsageUnavailableError) Error() string {
	return fmt.Sprintf("subscription_sharing_usage_unavailable: %s", e.Message)
}

// SubscriptionUnsupportedCapabilityError maps
// subscription_sharing_unsupported_capability (HTTP 400): an unsupported
// input or model parameter was sent. Documented recovery: inspect
// error.param and remove the unsupported input; do not retry the same body.
type SubscriptionUnsupportedCapabilityError struct {
	Param   string
	Message string
}

func (e *SubscriptionUnsupportedCapabilityError) Error() string {
	return fmt.Sprintf("subscription_sharing_unsupported_capability (param=%q): %s", e.Param, e.Message)
}

// SubscriptionInvalidUserError maps subscription_sharing_invalid_user
// (HTTP 401): "The subscriber context could not be validated." Documented
// recovery: ask the user to sign in again after confirmed revocation.
type SubscriptionInvalidUserError struct{ Message string }

func (e *SubscriptionInvalidUserError) Error() string {
	return fmt.Sprintf("subscription_sharing_invalid_user: %s", e.Message)
}

// SubscriptionScopeNotAuthorizedError maps chatpass_v2_scope_not_authorized
// (HTTP 403): a permission mismatch. Documented recovery: check the client
// and grant configuration rather than retrying.
type SubscriptionScopeNotAuthorizedError struct{ Message string }

func (e *SubscriptionScopeNotAuthorizedError) Error() string {
	return fmt.Sprintf("chatpass_v2_scope_not_authorized: %s", e.Message)
}

// SubscriptionReauthRequiredError maps the documented refresh-token error
// family (invalid_grant, token_expired, refresh_token_expired): the stored
// credential is unusable. Documented recovery: clear unusable tokens and
// repeat OAuth with the saved issued client ID.
type SubscriptionReauthRequiredError struct{ Reason string }

func (e *SubscriptionReauthRequiredError) Error() string {
	return fmt.Sprintf("credential unusable (%s); run 'harnessmesh provider auth chatgpt' again", e.Reason)
}

// SubscriptionClientMisconfiguredError maps the documented invalid_client
// token-endpoint error. Documented recovery: fix the client configuration
// (do not silently retry the OAuth flow, which would fail the same way).
type SubscriptionClientMisconfiguredError struct{ Reason string }

func (e *SubscriptionClientMisconfiguredError) Error() string {
	return fmt.Sprintf("invalid_client: %s", e.Reason)
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
