// Package auth contains shared authentication helpers for remote transports.
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TokenInfo is the subset of RFC 7662 introspection data used by HarnessMesh.
// Scopes are normalized from the provider's space-delimited scope claim.
type TokenInfo struct {
	Active    bool
	Subject   string
	Tenant    string
	Roles     map[string]bool
	Scopes    map[string]bool
	ExpiresAt int64
}

// IntrospectToken validates a bearer token at an OAuth 2.0 introspection
// endpoint. Network errors and malformed responses fail closed.
func IntrospectToken(ctx context.Context, endpoint, token, clientSecret string) (TokenInfo, bool) {
	if strings.TrimSpace(endpoint) == "" || strings.TrimSpace(token) == "" {
		return TokenInfo{}, false
	}
	form := url.Values{"token": []string{token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenInfo{}, false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if clientSecret != "" {
		req.Header.Set("Authorization", "Bearer "+clientSecret)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return TokenInfo{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TokenInfo{}, false
	}
	var raw struct {
		Active       bool     `json:"active"`
		Subject      string   `json:"sub"`
		Tenant       string   `json:"tenant"`
		OrgID        string   `json:"org_id"`
		Organization string   `json:"organization"`
		Roles        []string `json:"roles"`
		Scope        string   `json:"scope"`
		ExpiresAt    int64    `json:"exp"`
	}
	if json.NewDecoder(resp.Body).Decode(&raw) != nil || !raw.Active {
		return TokenInfo{}, false
	}
	if raw.ExpiresAt > 0 && raw.ExpiresAt <= time.Now().Unix() {
		return TokenInfo{}, false
	}
	scopes := make(map[string]bool)
	for _, scope := range strings.Fields(raw.Scope) {
		scopes[scope] = true
	}
	roles := make(map[string]bool)
	for _, role := range raw.Roles {
		if strings.TrimSpace(role) != "" {
			roles[strings.TrimSpace(role)] = true
		}
	}
	tenant := strings.TrimSpace(raw.Tenant)
	if tenant == "" {
		tenant = strings.TrimSpace(raw.OrgID)
	}
	if tenant == "" {
		tenant = strings.TrimSpace(raw.Organization)
	}
	return TokenInfo{Active: true, Subject: raw.Subject, Tenant: tenant, Roles: roles, Scopes: scopes, ExpiresAt: raw.ExpiresAt}, true
}

func HasScope(info TokenInfo, required string) bool {
	required = strings.TrimSpace(required)
	if required == "" {
		return true
	}
	return info.Scopes[required]
}
