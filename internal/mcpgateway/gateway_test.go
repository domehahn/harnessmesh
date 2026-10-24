package mcpgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/mcpregistry"
)

func TestGatewayDiscoverAndCallWithPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		switch req["method"] {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"read_status","description":"read status","inputSchema":{"type":"object"}},{"name":"delete_all","description":"delete","inputSchema":{"type":"object"}}]}}`))
		case "tools/call":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"ok"}]}}`))
		default:
			http.Error(w, "bad method", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	reg, err := mcpregistry.New(config.MCPRegistryConfig{DefaultDeny: true, Servers: map[string]config.MCPServerConfig{
		"status": {Type: "remote", Enabled: true, Endpoint: srv.URL, AllowedDomains: []string{"127.0.0.1"}, AllowedTools: []string{"read_status"}},
	}, Policies: map[string]config.MCPPolicyConfig{"read": {Servers: []string{"status"}, AllowedTools: []string{"read_status"}, AllowedDomains: []string{"127.0.0.1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	g := New(reg, "read", nil)
	tools, err := g.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "status__read_status" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	result, err := g.Call(context.Background(), "status__read_status", json.RawMessage(`{}`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected remote result")
	}
	if _, err := g.Call(context.Background(), "status__delete_all", json.RawMessage(`{}`), "test"); err == nil {
		t.Fatal("expected policy denial")
	}
}
