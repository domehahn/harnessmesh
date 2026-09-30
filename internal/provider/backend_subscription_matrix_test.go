package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/creditguard"
)

// This file is the requirement-8 integration-test matrix: first
// registration, returning registration, declined plan permission, expired
// access token, rotating refresh token, concurrent refresh, usage-limit
// response, revoked grant, unsupported Responses field, and model
// unavailable to the selected account. Each scenario below is a dedicated
// test so the matrix is auditable item by item, not folded into one big
// table that could silently drop coverage of one case.

// structuredErrorServer returns a documented {error:{code,message,param}}
// body for the Responses endpoint, exactly the shape
// errors-and-recovery.md documents.
func structuredErrorServer(status int, code, message, param string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		body := map[string]any{"error": map[string]any{"code": code, "message": message}}
		if param != "" {
			body["error"].(map[string]any)["param"] = param
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
}

// 1. First registration: no client_id persisted yet, so the authorize URL
// must use the documented bootstrap client_id ("dynamic_agent_client").
func TestMatrix_FirstRegistration_UsesBootstrapClientID(t *testing.T) {
	u := buildAuthorizeURL(siwcBootstrapClientID, "http://127.0.0.1:0/cb", "s", "n", "c", "urn:uuid:host-1")
	if want := "client_id=" + siwcBootstrapClientID; !containsQueryParam(u, want) {
		t.Fatalf("expected first-registration authorize URL to use the bootstrap client_id, got %s", u)
	}
}

// 2. Returning registration: a previously issued client_id must be reused
// verbatim in the authorize URL rather than re-bootstrapping.
func TestMatrix_ReturningRegistration_ReusesIssuedClientID(t *testing.T) {
	u := buildAuthorizeURL("oaiapp_previously_issued", "http://127.0.0.1:0/cb", "s", "n", "c", "urn:uuid:host-1")
	if !containsQueryParam(u, "client_id=oaiapp_previously_issued") {
		t.Fatalf("expected returning-registration authorize URL to reuse the issued client_id, got %s", u)
	}
}

// 3. Declined plan permission: the token response grants scopes that do NOT
// include chatgpt.tokens.use.direct - HasChatGPTPlanUsageGrant must report
// false so the login flow can refuse to save a credential that can't
// actually do ChatGPT-plan inference.
func TestMatrix_DeclinedPlanPermission_GrantCheckFails(t *testing.T) {
	ts := &SIWCTokenSet{AccessToken: "a", Scopes: []string{"openid", "profile", "email", "offline_access", "resource.invoke"}}
	if ts.HasChatGPTPlanUsageGrant() {
		t.Fatalf("expected HasChatGPTPlanUsageGrant to be false when chatgpt.tokens.use.direct was not granted")
	}
}

// 4. Expired access token triggers a refresh (already covered in detail by
// TestSubscriptionBackend_ExpiredTokenIsRefreshed in backend_subscription_test.go).
// This test additionally proves the refreshed token set's expiry is used
// going forward, i.e. ensureValidToken doesn't refresh twice in a row.
func TestMatrix_ExpiredAccessToken_RefreshedOnce(t *testing.T) {
	responsesSrv := newFakeResponsesServer("normal")
	defer responsesSrv.Close()
	var refreshes atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			refreshes.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed", "refresh_token": "refreshed-r", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	tokenPath := t.TempDir() + "/auth.json"
	_ = saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_x", AccessToken: "old", RefreshToken: "old-r", ExpiresAt: time.Now().Add(-time.Minute)})
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: tokenSrv.URL, responsesURL: responsesSrv.URL, httpClient: http.DefaultClient}

	for i := 0; i < 2; i++ {
		sink := newCollectingSink()
		if err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink); err != nil {
			t.Fatalf("StreamResponse #%d: %v", i, err)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("expected exactly 1 refresh across 2 requests once the token is fresh, got %d", refreshes.Load())
	}
}

// 5. Rotating refresh token: the server issues a NEW refresh token on
// refresh, and that replacement must be the one persisted (not the old one
// kept around), per the documented refresh-token rotation behavior.
func TestMatrix_RotatingRefreshToken_ReplacementPersisted(t *testing.T) {
	responsesSrv := newFakeResponsesServer("normal")
	defer responsesSrv.Close()
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "rotated-refresh-token", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	tokenPath := t.TempDir() + "/auth.json"
	_ = saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_x", AccessToken: "old", RefreshToken: "original-refresh-token", ExpiresAt: time.Now().Add(-time.Minute)})
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: tokenSrv.URL, responsesURL: responsesSrv.URL, httpClient: http.DefaultClient}

	sink := newCollectingSink()
	if err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink); err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}
	persisted, err := loadSIWCTokenSet(tokenPath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if persisted.RefreshToken != "rotated-refresh-token" {
		t.Fatalf("expected the rotated refresh token to be persisted, got %q", persisted.RefreshToken)
	}
}

// 6. Concurrent refresh: two goroutines racing to use an expired token must
// serialize through ensureValidToken's mutex, producing exactly one
// refresh_token grant, not two.
func TestMatrix_ConcurrentRefresh_Serialized(t *testing.T) {
	responsesSrv := newFakeResponsesServer("normal")
	defer responsesSrv.Close()
	var refreshes atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" {
			refreshes.Add(1)
			time.Sleep(50 * time.Millisecond) // widen the race window
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "refreshed", "refresh_token": "refreshed-r", "expires_in": 3600})
	}))
	defer tokenSrv.Close()

	tokenPath := t.TempDir() + "/auth.json"
	_ = saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_x", AccessToken: "old", RefreshToken: "old-r", ExpiresAt: time.Now().Add(-time.Minute)})
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: tokenSrv.URL, responsesURL: responsesSrv.URL, httpClient: http.DefaultClient}

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sink := newCollectingSink()
			errs[idx] = b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: StreamResponse: %v", i, err)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("expected concurrent expired-token requests to serialize into exactly 1 refresh, got %d", refreshes.Load())
	}
}

// 7. Usage-limit response: subscription_sharing_usage_limit_exceeded (429)
// must map to SubscriptionUsageLimitExceededError, not a generic RateLimitedError.
func TestMatrix_UsageLimitExceeded_MapsToTypedError(t *testing.T) {
	creditguard.ResetForTest()
	srv := structuredErrorServer(429, "subscription_sharing_usage_limit_exceeded", "ChatGPT plan usage limit reached", "")
	defer srv.Close()
	b := newTestSubscriptionBackend(t, &fakeResponsesServer{Server: srv}, nil)

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink)
	if _, ok := err.(*SubscriptionUsageLimitExceededError); !ok {
		t.Fatalf("expected SubscriptionUsageLimitExceededError, got %T: %v", err, err)
	}
}

// Also directly exercises subscription_sharing_usage_unavailable (503).
func TestMatrix_UsageUnavailable_MapsToTypedError(t *testing.T) {
	creditguard.ResetForTest()
	srv := structuredErrorServer(503, "subscription_sharing_usage_unavailable", "usage availability could not be checked", "")
	defer srv.Close()
	b := newTestSubscriptionBackend(t, &fakeResponsesServer{Server: srv}, nil)

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink)
	if _, ok := err.(*SubscriptionUsageUnavailableError); !ok {
		t.Fatalf("expected SubscriptionUsageUnavailableError, got %T: %v", err, err)
	}
}

// 8. Revoked grant: the token endpoint returns invalid_grant on refresh -
// this must surface as SubscriptionReauthRequiredError (documented recovery:
// clear the unusable token and repeat OAuth), not a generic UnauthorizedError.
func TestMatrix_RevokedGrant_RequiresReauth(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "refresh token has been revoked"})
	}))
	defer tokenSrv.Close()

	tokenPath := t.TempDir() + "/auth.json"
	_ = saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_x", AccessToken: "old", RefreshToken: "revoked-refresh", ExpiresAt: time.Now().Add(-time.Minute)})
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: tokenSrv.URL, responsesURL: tokenSrv.URL, httpClient: http.DefaultClient}

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink)
	if _, ok := err.(*SubscriptionReauthRequiredError); !ok {
		t.Fatalf("expected SubscriptionReauthRequiredError for a revoked grant, got %T: %v", err, err)
	}
}

// 9. Unsupported Responses field: subscription_sharing_unsupported_capability
// (400) must map to SubscriptionUnsupportedCapabilityError and preserve the
// documented error.param so callers know exactly what to remove.
func TestMatrix_UnsupportedResponsesField_MapsToTypedErrorWithParam(t *testing.T) {
	creditguard.ResetForTest()
	srv := structuredErrorServer(400, "subscription_sharing_unsupported_capability", "previous_response_id is not supported", "previous_response_id")
	defer srv.Close()
	b := newTestSubscriptionBackend(t, &fakeResponsesServer{Server: srv}, nil)

	sink := newCollectingSink()
	err := b.StreamResponse(context.Background(), Request{Model: "x", Input: InputItems{{Type: "message", Role: "user", Content: []ContentPart{{Type: "input_text", Text: "hi"}}}}}, sink)
	typed, ok := err.(*SubscriptionUnsupportedCapabilityError)
	if !ok {
		t.Fatalf("expected SubscriptionUnsupportedCapabilityError, got %T: %v", err, err)
	}
	if typed.Param != "previous_response_id" {
		t.Fatalf("expected error.param to be preserved, got %q", typed.Param)
	}
}

// 10. Model unavailable to the selected account: ListModels must filter
// strictly to visibility:"list" entries, so a model absent from (or
// hidden in) the authenticated account's catalog is never offered.
func TestMatrix_ModelUnavailableToAccount_FilteredFromCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]string{
				{"slug": "gpt-5-chatgpt", "display_name": "GPT-5 (ChatGPT)", "visibility": "list"},
				{"slug": "gpt-5-pro-internal", "display_name": "GPT-5 Pro (internal)", "visibility": "hidden"},
			},
		})
	}))
	defer srv.Close()

	tokenPath := t.TempDir() + "/auth.json"
	_ = saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "oaiapp_x", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)})
	b := NewSubscriptionBackendWithTokenPath("chatgpt-subscription", tokenPath)
	b.client = &siwcTokenClient{tokenURL: srv.URL, responsesURL: srv.URL + "/responses", httpClient: http.DefaultClient}

	models, err := b.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].Slug != "gpt-5-chatgpt" {
		t.Fatalf("expected only the visibility:list model to be offered, got %+v", models)
	}
	for _, m := range models {
		if m.Slug == "gpt-5-pro-internal" {
			t.Fatalf("a model not visible to this account must never be offered for selection")
		}
	}
}

func containsQueryParam(rawURL, kv string) bool {
	return strings.Contains(rawURL, kv)
}
