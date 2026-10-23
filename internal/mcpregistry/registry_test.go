package mcpregistry

import (
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
)

func TestRegistryFailsClosedAndAppliesPolicy(t *testing.T) {
	r, err := New(config.MCPRegistryConfig{DefaultDeny: true, Servers: map[string]config.MCPServerConfig{
		"serena": {Type: "local", Enabled: true, Command: "serena", AllowedTools: []string{"find_symbol", "find_*"}},
	}, Policies: map[string]config.MCPPolicyConfig{
		"developer": {Servers: []string{"serena"}, AllowedTools: []string{"find_*"}, RequireApprovalWrite: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if d := r.Check("serena", "find_symbol", "developer"); !d.Allowed || !d.RequiresApproval {
		t.Fatalf("expected approved read admission, got %+v", d)
	}
	if d := r.Check("serena", "write_file", "developer"); d.Allowed {
		t.Fatalf("expected denied tool, got %+v", d)
	}
	if d := r.Check("serena", "find_symbol", ""); d.Allowed {
		t.Fatalf("expected default-deny decision, got %+v", d)
	}
}

func TestRegistryRejectsUnknownPolicyServer(t *testing.T) {
	if _, err := New(config.MCPRegistryConfig{Policies: map[string]config.MCPPolicyConfig{"p": {Servers: []string{"missing"}}}}); err == nil {
		t.Fatal("expected unknown server validation error")
	}
}
