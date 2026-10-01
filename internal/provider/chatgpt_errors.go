package provider

import "encoding/json"

// This file maps OpenAI's documented SIWC/ChatGPT-plan-usage error
// taxonomy (developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery)
// onto HarnessMesh's typed errors, so each documented failure mode is
// handled per its documented recovery action rather than collapsed into a
// generic 4xx/5xx.

// mapTokenEndpointError maps a non-200 token-endpoint response per the
// documented "Refresh Token Errors" section: invalid_grant/token_expired/
// refresh_token_expired mean the stored credential is unusable and OAuth
// must be repeated; invalid_client means the client configuration itself
// is wrong (never silently retried, since retrying would fail identically).
func mapTokenEndpointError(status int, body []byte) error {
	var payload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &payload)

	switch payload.Error {
	case "invalid_grant", "token_expired", "refresh_token_expired":
		return &SubscriptionReauthRequiredError{Reason: payload.Error}
	case "invalid_client":
		return &SubscriptionClientMisconfiguredError{Reason: payload.ErrorDescription}
	}
	// No structured OAuth error code present - fall back to the documented
	// generic pre-stream statuses.
	switch status {
	case 401:
		return &UnauthorizedError{}
	case 403:
		return &ForbiddenError{Reason: "the required signed identity or direct permission was not accepted"}
	case 503:
		return &BackendUnavailableError{Backend: "chatgpt-subscription", Reason: "token endpoint temporarily unavailable"}
	default:
		return &UnauthorizedError{}
	}
}

// mapResponsesAPIError maps a non-2xx https://api.openai.com/v1/responses
// response per the documented "Structured Response Errors" table.
func mapResponsesAPIError(status int, body []byte) error {
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	code := payload.Error.Code
	msg := payload.Error.Message

	switch code {
	case "subscription_sharing_user_not_eligible":
		return &SubscriptionUserNotEligibleError{Message: msg}
	case "subscription_sharing_usage_limit_exceeded":
		return &SubscriptionUsageLimitExceededError{Message: msg}
	case "subscription_sharing_usage_unavailable":
		return &SubscriptionUsageUnavailableError{Message: msg}
	case "subscription_sharing_unsupported_capability":
		return &SubscriptionUnsupportedCapabilityError{Param: payload.Error.Param, Message: msg}
	case "subscription_sharing_invalid_user":
		return &SubscriptionInvalidUserError{Message: msg}
	case "chatpass_v2_scope_not_authorized":
		return &SubscriptionScopeNotAuthorizedError{Message: msg}
	}

	// No documented structured code present - fall back to the documented
	// generic pre-stream statuses (401/403/503), then ordinary HTTP status
	// mapping for anything else.
	switch status {
	case 401:
		return &UnauthorizedError{}
	case 403:
		return &ForbiddenError{Reason: "a policy or permission check (e.g. permitted serving region) prevented admission"}
	case 429:
		return &RateLimitedError{}
	case 503:
		return &BackendUnavailableError{Backend: "chatgpt-subscription", Reason: "direct routing is unavailable or not enabled"}
	default:
		return &BackendUnavailableError{Backend: "chatgpt-subscription", Reason: msg}
	}
}
