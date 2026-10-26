// Package integrations contains narrow, policy-aware clients for optional
// external agent infrastructure. The clients do not persist credentials and
// always send the tenant boundary explicitly.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/contextpack"
)

type MemoryClient struct {
	Endpoint   string
	Token      string
	HTTPClient *http.Client
}

func (c MemoryClient) Put(ctx context.Context, tenant, project, key, value string) error {
	if strings.TrimSpace(tenant) == "" || strings.TrimSpace(project) == "" || strings.TrimSpace(key) == "" {
		return errors.New("tenant, project and key are required")
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("memory endpoint is required")
	}
	body, _ := json.Marshal(map[string]any{"tenant": tenant, "project": project, "key": key, "value": contextpack.RedactSecrets(value)})
	return c.do(ctx, http.MethodPost, "/memories", tenant, body)
}

func (c MemoryClient) Search(ctx context.Context, tenant, project, query string) ([]map[string]any, error) {
	if strings.TrimSpace(tenant) == "" || strings.TrimSpace(project) == "" || strings.TrimSpace(query) == "" {
		return nil, errors.New("tenant, project and query are required")
	}
	u, err := url.Parse(strings.TrimRight(c.Endpoint, "/") + "/memories/search")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("tenant", tenant)
	q.Set("project", project)
	q.Set("q", contextpack.RedactSecrets(query))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	c.headers(req, tenant)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("memory search returned %s", resp.Status)
	}
	var result struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (c MemoryClient) do(ctx context.Context, method, path, tenant string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.headers(req, tenant)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("memory provider returned %s", resp.Status)
	}
	return nil
}
func (c MemoryClient) headers(req *http.Request, tenant string) {
	req.Header.Set("X-HarnessMesh-Tenant", tenant)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}
func (c MemoryClient) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

type GatewayClient struct {
	Endpoint   string
	Token      string
	HTTPClient *http.Client
}

func (c GatewayClient) Health(ctx context.Context) (bool, error) {
	if c.Endpoint == "" {
		return false, errors.New("gateway endpoint is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Endpoint, "/")+"/healthz", nil)
	if err != nil {
		return false, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode/100 == 2, nil
}
func (c GatewayClient) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

type BrowserPolicy struct {
	AllowedDomains  []string
	RequireApproval bool
}

func (p BrowserPolicy) Authorize(rawURL string, approved bool) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return errors.New("browser URL must use https")
	}
	if len(p.AllowedDomains) == 0 {
		return errors.New("browser domain allowlist is empty")
	}
	allowed := false
	for _, domain := range p.AllowedDomains {
		domain = strings.TrimSpace(strings.ToLower(domain))
		host := strings.ToLower(u.Hostname())
		if host == domain || strings.HasSuffix(host, "."+domain) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("browser domain %q is not allowed", u.Hostname())
	}
	if p.RequireApproval && !approved {
		return errors.New("browser action requires approval")
	}
	return nil
}
