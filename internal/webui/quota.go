package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

type quotaSnapshot struct {
	Available     bool     `json:"available"`
	Provider      string   `json:"provider,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	UsedPercent   *float64 `json:"used_percent"`
	NextAvailable string   `json:"next_available_at,omitempty"`
	Remaining     *float64 `json:"remaining,omitempty"`
	Limit         *float64 `json:"limit,omitempty"`
}

func probeTransport(ctx context.Context, endpointEnv, tokenEnv string) map[string]any {
	endpoint := strings.TrimSpace(os.Getenv(endpointEnv))
	if endpoint == "" {
		return map[string]any{"status": "not_configured", "endpoint_configured": false}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return map[string]any{"status": "invalid_endpoint", "endpoint_configured": true}
	}
	if token := strings.TrimSpace(os.Getenv(tokenEnv)); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		return map[string]any{"status": "unavailable", "endpoint_configured": true}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return map[string]any{"status": "unhealthy", "endpoint_configured": true, "http_status": response.StatusCode}
	}
	return map[string]any{"status": "healthy", "endpoint_configured": true, "checked_at": time.Now().UTC()}
}

// providerQuota reads an operator-configured provider quota endpoint. The
// endpoint is deliberately generic: HarnessMesh observes provider allowance
// data but never creates, changes, or bills an account.
func providerQuota(ctx context.Context) quotaSnapshot {
	endpoint := strings.TrimSpace(os.Getenv("HARNESSMESH_PROVIDER_QUOTA_URL"))
	if endpoint == "" {
		return quotaSnapshot{Reason: "provider quota API not configured"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return quotaSnapshot{Reason: "provider quota URL is invalid"}
	}
	if token := strings.TrimSpace(os.Getenv("HARNESSMESH_PROVIDER_QUOTA_TOKEN")); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return quotaSnapshot{Reason: "provider quota API unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return quotaSnapshot{Reason: "provider quota API returned an error"}
	}
	var payload struct {
		Provider      string   `json:"provider"`
		UsedPercent   *float64 `json:"used_percent"`
		NextAvailable string   `json:"next_available_at"`
		Remaining     *float64 `json:"remaining"`
		Limit         *float64 `json:"limit"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return quotaSnapshot{Reason: "provider quota response is invalid"}
	}
	if payload.UsedPercent == nil || *payload.UsedPercent < 0 || *payload.UsedPercent > 100 {
		return quotaSnapshot{Reason: "provider quota response has an invalid used_percent"}
	}
	return quotaSnapshot{Available: true, Provider: payload.Provider, UsedPercent: payload.UsedPercent, NextAvailable: payload.NextAvailable, Remaining: payload.Remaining, Limit: payload.Limit}
}
