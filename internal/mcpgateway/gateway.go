// Package mcpgateway is the guarded outbound MCP transport used to expose
// approved external tools through HarnessMesh' own MCP endpoint.
package mcpgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/mcpregistry"
)

type AuditEvent struct {
	Server           string    `json:"server"`
	Tool             string    `json:"tool"`
	Caller           string    `json:"caller,omitempty"`
	Allowed          bool      `json:"allowed"`
	ApprovalRequired bool      `json:"approval_required"`
	Error            string    `json:"error,omitempty"`
	At               time.Time `json:"at"`
}

type AuditSink func(AuditEvent)

type Gateway struct {
	registry  *mcpregistry.Registry
	policy    string
	client    *http.Client
	audit     AuditSink
	mu        sync.Mutex
	clients   map[string]*Client
	requestID atomic.Uint64
}

func New(registry *mcpregistry.Registry, policy string, sink AuditSink) *Gateway {
	return &Gateway{registry: registry, policy: policy, client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return fmt.Errorf("MCP gateway refuses redirects; configure the final allowlisted endpoint explicitly")
	}}, audit: sink, clients: make(map[string]*Client)}
}

func (g *Gateway) SetHTTPClient(client *http.Client) {
	if client != nil {
		g.client = client
	}
}

func (g *Gateway) clientFor(name string) (*Client, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c := g.clients[name]; c != nil {
		return c, nil
	}
	var found *config.MCPServerConfig
	for _, server := range g.registry.List() {
		if server.Name == name {
			copy := server.MCPServerConfig
			found = &copy
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("MCP server %q is not registered", name)
	}
	if found.Type != "remote" {
		return nil, fmt.Errorf("MCP server %q is not a remote server", name)
	}
	if decision := g.registry.CheckEndpoint(name, found.Endpoint, g.policy); !decision.Allowed {
		return nil, fmt.Errorf("external MCP endpoint denied for %q: %s", name, decision.Reason)
	}
	token := ""
	if found.TokenEnv != "" {
		token = strings.TrimSpace(os.Getenv(found.TokenEnv))
	}
	c := &Client{endpoint: found.Endpoint, token: token, http: g.client, nextID: &g.requestID, timeout: time.Duration(found.TimeoutSeconds) * time.Second}
	if c.timeout <= 0 {
		c.timeout = 30 * time.Second
	}
	g.clients[name] = c
	return c, nil
}

type Tool struct {
	Name, Description string
	InputSchema       map[string]any
}

func (g *Gateway) ListTools(ctx context.Context) ([]Tool, error) {
	var all []Tool
	for _, server := range g.registry.List() {
		if !server.Enabled || server.Type != "remote" {
			continue
		}
		client, err := g.clientFor(server.Name)
		if err != nil {
			return nil, err
		}
		tools, err := client.ListTools(ctx)
		if err != nil {
			return nil, fmt.Errorf("list tools from %s: %w", server.Name, err)
		}
		for _, tool := range tools {
			decision := g.registry.Check(server.Name, tool.Name, g.policy)
			if !decision.Allowed {
				continue
			}
			all = append(all, Tool{Name: server.Name + "__" + tool.Name, Description: "External MCP " + server.Name + ": " + tool.Description, InputSchema: tool.InputSchema})
		}
	}
	return all, nil
}

func (g *Gateway) Call(ctx context.Context, exposedName string, args json.RawMessage, caller string) (any, error) {
	parts := strings.SplitN(exposedName, "__", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid external MCP tool name %q", exposedName)
	}
	serverName, toolName := parts[0], parts[1]
	decision := g.registry.Check(serverName, toolName, g.policy)
	event := AuditEvent{Server: serverName, Tool: toolName, Caller: caller, Allowed: decision.Allowed, ApprovalRequired: decision.RequiresApproval, At: time.Now().UTC()}
	defer func() {
		if g.audit != nil {
			g.audit(event)
		}
	}()
	if !decision.Allowed {
		event.Error = decision.Reason
		return nil, fmt.Errorf("external MCP policy denied %s: %s", exposedName, decision.Reason)
	}
	if decision.RequiresApproval {
		event.Error = "human approval required"
		return nil, fmt.Errorf("external MCP tool %s requires human approval", exposedName)
	}
	client, err := g.clientFor(serverName)
	if err != nil {
		event.Error = err.Error()
		return nil, err
	}
	result, err := client.Call(ctx, toolName, args)
	if err != nil {
		event.Error = err.Error()
		return nil, err
	}
	return result, nil
}

type Client struct {
	endpoint, token string
	http            *http.Client
	nextID          *atomic.Uint64
	timeout         time.Duration
	mu              sync.Mutex
	session         string
}

func (c *Client) request(ctx context.Context, method string, params any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID.Add(1)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("remote MCP returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.session = sid
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if e, ok := result["error"].(map[string]any); ok {
		return nil, fmt.Errorf("remote MCP error: %v", e["message"])
	}
	return result, nil
}

func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	if c.session == "" {
		if _, err := c.request(ctx, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "harnessmesh-gateway", "version": "1"}}); err != nil {
			return nil, err
		}
	}
	result, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	inner, _ := result["result"].(map[string]any)
	raw, _ := json.Marshal(inner["tools"])
	var tools []Tool
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, err
	}
	return tools, nil
}

func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (any, error) {
	if c.session == "" {
		if _, err := c.request(ctx, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "harnessmesh-gateway", "version": "1"}}); err != nil {
			return nil, err
		}
	}
	var arguments any
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return nil, err
		}
	}
	result, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return nil, err
	}
	return result["result"], nil
}
