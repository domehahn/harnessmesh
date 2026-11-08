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

	"github.com/domehahn/harnessmesh/internal/assurance"
	"github.com/domehahn/harnessmesh/internal/contextpack"
)

const (
	MemoryTierCore     = "core"
	MemoryTierArchival = "archival"
	MemoryTierRecall   = "recall"
)

// MemoryBlock represents a structured, hierarchical memory block compatible
// with stateful agent memory providers such as Letta, Cognee and Haystack.
type MemoryBlock struct {
	Tier     string `json:"tier"`
	Label    string `json:"label"`
	Value    string `json:"value"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

func ValidateMemoryTier(tier string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(tier))
	if norm == "" {
		return MemoryTierCore, nil
	}
	switch norm {
	case MemoryTierCore, MemoryTierArchival, MemoryTierRecall:
		return norm, nil
	default:
		return "", fmt.Errorf("unsupported memory tier %q (expected core, archival or recall)", tier)
	}
}

type MemoryClient struct {
	Provider   string
	Endpoint   string
	Token      string
	HTTPClient *http.Client
}

func (c MemoryClient) PutBlock(ctx context.Context, tenant, project string, block MemoryBlock) error {
	if strings.TrimSpace(tenant) == "" || strings.TrimSpace(project) == "" || strings.TrimSpace(block.Label) == "" {
		return errors.New("tenant, project and block label are required")
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("memory endpoint is required")
	}
	tier, err := ValidateMemoryTier(block.Tier)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"tenant":    tenant,
		"project":   project,
		"tier":      tier,
		"label":     strings.TrimSpace(block.Label),
		"value":     contextpack.RedactSecrets(block.Value),
		"read_only": block.ReadOnly,
	}
	if p := strings.TrimSpace(c.Provider); p != "" {
		payload["provider"] = strings.ToLower(p)
	}
	body, _ := json.Marshal(payload)
	return c.do(ctx, http.MethodPost, "/memories/blocks", tenant, body)
}

func (c MemoryClient) Blocks(ctx context.Context, tenant, project, tier string) ([]MemoryBlock, error) {
	if strings.TrimSpace(tenant) == "" || strings.TrimSpace(project) == "" {
		return nil, errors.New("tenant and project are required")
	}
	normTier, err := ValidateMemoryTier(tier)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimRight(c.Endpoint, "/") + "/memories/blocks")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("tenant", tenant)
	q.Set("project", project)
	q.Set("tier", normTier)
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
		return nil, fmt.Errorf("memory blocks returned %s", resp.Status)
	}
	var result struct {
		Blocks []MemoryBlock `json:"blocks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	for i := range result.Blocks {
		result.Blocks[i].Value = contextpack.RedactSecrets(result.Blocks[i].Value)
	}
	return result.Blocks, nil
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

// BrowserAction describes a structured web interaction step for agentic browser
// providers such as Playwright, Browser Use and Stagehand.
type BrowserAction struct {
	SessionID   string `json:"session_id,omitempty"`
	Tenant      string `json:"tenant"`
	Project     string `json:"project,omitempty"`
	Agent       string `json:"agent"`
	Provider    string `json:"provider"` // playwright, browser-use, stagehand
	Kind        string `json:"kind"`     // navigate, act, extract, observe
	URL         string `json:"url"`
	Instruction string `json:"instruction,omitempty"`
	Approved    bool   `json:"approved"`
}

func ValidateBrowserProvider(provider string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(provider))
	if norm == "" {
		return "playwright", nil
	}
	switch norm {
	case "playwright", "browser-use", "stagehand":
		return norm, nil
	default:
		return "", fmt.Errorf("unsupported browser provider %q (expected playwright, browser-use or stagehand)", provider)
	}
}

// AuthorizeAction enforces HTTPS, domain allowlists, approval gates (always
// required for mutating "act" steps), secret redaction and the HarnessMesh
// Action Firewall before a browser adapter executes a step.
func (p BrowserPolicy) AuthorizeAction(ctx context.Context, firewallPolicy assurance.Policy, action BrowserAction) (assurance.ActionDecision, error) {
	provider, err := ValidateBrowserProvider(action.Provider)
	if err != nil {
		return assurance.ActionDecision{}, err
	}
	kind := strings.ToLower(strings.TrimSpace(action.Kind))
	if kind == "" {
		kind = "navigate"
	}
	switch kind {
	case "navigate", "act", "extract", "observe":
	default:
		return assurance.ActionDecision{}, fmt.Errorf("unsupported browser action kind %q (expected navigate, act, extract or observe)", action.Kind)
	}
	if kind == "act" && !action.Approved {
		return assurance.ActionDecision{}, errors.New("browser act step requires explicit approval")
	}
	if err := p.Authorize(action.URL, action.Approved); err != nil {
		return assurance.ActionDecision{}, err
	}
	u, _ := url.Parse(action.URL)
	host := strings.ToLower(u.Hostname())
	redactedInstruction := contextpack.RedactSecrets(action.Instruction)
	fwAction := assurance.Action{
		SessionID: action.SessionID,
		Tenant:    action.Tenant,
		Project:   action.Project,
		Agent:     action.Agent,
		Tool:      "browser." + provider,
		Operation: "browser." + kind,
		Target:    host,
		Path:      u.Path,
		Network:   "https:" + host,
		Content:   redactedInstruction,
		DataClass: "web",
	}
	flows := []assurance.DataFlow{
		{
			Source:      action.Tenant + "." + action.Agent,
			Destination: host,
			DataClass:   "web",
			Allowed:     true,
		},
	}
	return assurance.EvaluateAction(ctx, firewallPolicy, fwAction, flows)
}
