// Package mcpregistry provides the policy boundary for third-party MCP
// servers. It intentionally does not execute processes: transport adapters
// must ask this package for admission before connecting or forwarding calls.
package mcpregistry

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/domehahn/harnessmesh/internal/config"
)

type Registry struct {
	cfg config.MCPRegistryConfig
}

type Server struct {
	Name string
	config.MCPServerConfig
}

type Decision struct {
	Allowed          bool   `json:"allowed"`
	RequiresApproval bool   `json:"requires_approval"`
	Reason           string `json:"reason,omitempty"`
	Server           string `json:"server,omitempty"`
	Tool             string `json:"tool,omitempty"`
	MatchedPolicy    string `json:"matched_policy,omitempty"`
}

func New(cfg config.MCPRegistryConfig) (*Registry, error) {
	r := &Registry{cfg: cfg}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) Validate() error {
	for name, s := range r.cfg.Servers {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("MCP server name must not be empty")
		}
		typ := strings.ToLower(strings.TrimSpace(s.Type))
		if typ != "local" && typ != "remote" {
			return fmt.Errorf("MCP server %q: type must be local or remote", name)
		}
		if s.Enabled {
			if len(s.AllowedTools) == 0 {
				return fmt.Errorf("MCP server %q: enabled server requires an explicit allowed_tools list", name)
			}
			if typ == "local" && strings.TrimSpace(s.Command) == "" {
				return fmt.Errorf("MCP server %q: enabled local server requires command", name)
			}
			if typ == "remote" {
				u, err := url.Parse(strings.TrimSpace(s.Endpoint))
				if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
					return fmt.Errorf("MCP server %q: enabled remote server requires an http(s) endpoint", name)
				}
			}
		}
		if s.NetworkMode != "" && s.NetworkMode != "none" && s.NetworkMode != "host" {
			return fmt.Errorf("MCP server %q: network_mode must be none or host", name)
		}
		if s.TimeoutSeconds < 0 || s.MaxRequestsMinute < 0 {
			return fmt.Errorf("MCP server %q: limits must not be negative", name)
		}
		for _, tool := range append(append([]string{}, s.AllowedTools...), s.Capabilities...) {
			if strings.TrimSpace(tool) == "" {
				return fmt.Errorf("MCP server %q: tool/capability names must not be empty", name)
			}
		}
	}
	for policy, p := range r.cfg.Policies {
		if strings.TrimSpace(policy) == "" {
			return fmt.Errorf("MCP policy name must not be empty")
		}
		for _, server := range p.Servers {
			if _, ok := r.cfg.Servers[server]; !ok {
				return fmt.Errorf("MCP policy %q references unknown server %q", policy, server)
			}
		}
	}
	return nil
}

func (r *Registry) List() []Server {
	result := make([]Server, 0, len(r.cfg.Servers))
	for name, cfg := range r.cfg.Servers {
		result = append(result, Server{Name: name, MCPServerConfig: cfg})
	}
	// Deterministic ordering without importing a sorting policy into callers.
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j].Name < result[j-1].Name; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

func (r *Registry) Check(server, tool, policy string) Decision {
	d := Decision{Server: server, Tool: tool, MatchedPolicy: policy}
	s, ok := r.cfg.Servers[server]
	if !ok {
		d.Reason = "server is not registered"
		return d
	}
	if !s.Enabled {
		d.Reason = "server is disabled"
		return d
	}
	if !matches(s.AllowedTools, tool) {
		d.Reason = "tool is not allowed by server allowlist"
		return d
	}
	if policy != "" {
		p, exists := r.cfg.Policies[policy]
		if !exists {
			d.Reason = "policy is not registered"
			return d
		}
		if len(p.Servers) > 0 && !matches(p.Servers, server) {
			d.Reason = "server is not allowed by policy"
			return d
		}
		if len(p.AllowedTools) > 0 && !matches(p.AllowedTools, tool) {
			d.Reason = "tool is not allowed by policy"
			return d
		}
		if len(p.DeniedTools) > 0 && matches(p.DeniedTools, tool) {
			d.Reason = "tool is denied by policy"
			return d
		}
		d.RequiresApproval = p.RequireApprovalWrite
	}
	if s.RequiresApproval {
		d.RequiresApproval = true
	}
	if r.cfg.DefaultDeny && policy == "" {
		d.Reason = "default-deny registry requires an explicit policy"
		return d
	}
	d.Allowed = true
	return d
}

// CheckEndpoint applies the same admission policy to a remote destination.
// Callers should invoke this before dialing; redirects must be disabled or
// re-checked by the transport adapter.
func (r *Registry) CheckEndpoint(server, endpoint, policy string) Decision {
	d := Decision{Server: server, MatchedPolicy: policy}
	if r.cfg.DefaultDeny && strings.TrimSpace(policy) == "" {
		d.Reason = "default-deny registry requires an explicit policy for remote discovery"
		return d
	}
	s, ok := r.cfg.Servers[server]
	if !ok {
		d.Reason = "server is not registered"
		return d
	}
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
		d.Reason = "endpoint must be an http(s) URL"
		return d
	}
	allowed := s.AllowedDomains
	if p, exists := r.cfg.Policies[policy]; exists && len(p.AllowedDomains) > 0 {
		allowed = p.AllowedDomains
	}
	if len(allowed) == 0 {
		d.Reason = "remote endpoint requires an explicit domain allowlist"
		return d
	}
	for _, domain := range allowed {
		if strings.EqualFold(strings.TrimSpace(domain), u.Hostname()) {
			d.Allowed = true
			return d
		}
	}
	d.Reason = "endpoint domain is not allowlisted"
	return d
}

func matches(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == value || pattern == "*" {
			return true
		}
		if ok, _ := filepath.Match(pattern, value); ok {
			return true
		}
	}
	return false
}
