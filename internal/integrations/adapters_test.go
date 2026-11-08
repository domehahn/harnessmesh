package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/domehahn/harnessmesh/internal/assurance"
)

func TestBrowserPolicyFailClosed(t *testing.T) {
	p := BrowserPolicy{AllowedDomains: []string{"example.test"}, RequireApproval: true}
	if err := p.Authorize("http://example.test", true); err == nil {
		t.Fatal("expected http to fail")
	}
	if err := p.Authorize("https://evil.test", true); err == nil {
		t.Fatal("expected domain to fail")
	}
	if err := p.Authorize("https://app.example.test", false); err == nil {
		t.Fatal("expected approval to fail")
	}
	if err := p.Authorize("https://app.example.test", true); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserPolicyAuthorizeActionWithStagehandAndBrowserUse(t *testing.T) {
	p := BrowserPolicy{AllowedDomains: []string{"docs.example.test"}, RequireApproval: true}
	ctx := context.Background()

	// Unapproved Stagehand act must fail closed
	_, err := p.AuthorizeAction(ctx, assurance.Policy{}, BrowserAction{
		Tenant:      "tenant-1",
		Agent:       "codex",
		Provider:    "stagehand",
		Kind:        "act",
		URL:         "https://docs.example.test/login",
		Instruction: "click submit",
		Approved:    false,
	})
	if err == nil {
		t.Fatal("expected unapproved stagehand act to fail")
	}

	// Approved Stagehand extract produces a deterministic ActionDigest
	dec, err := p.AuthorizeAction(ctx, assurance.Policy{}, BrowserAction{
		Tenant:      "tenant-1",
		Project:     "proj-1",
		Agent:       "codex",
		Provider:    "stagehand",
		Kind:        "extract",
		URL:         "https://docs.example.test/reference",
		Instruction: "extract API table",
		Approved:    true,
	})
	if err != nil {
		t.Fatalf("unexpected AuthorizeAction error: %v", err)
	}
	if dec.ActionDigest == "" || !dec.Allowed {
		t.Fatalf("expected allowed decision with digest, got %#v", dec)
	}

	// Browser-use provider is supported
	if _, err := ValidateBrowserProvider("browser-use"); err != nil {
		t.Fatalf("expected browser-use provider to be valid: %v", err)
	}
	if _, err := ValidateBrowserProvider("unknown-browser"); err == nil {
		t.Fatal("expected unknown browser provider to fail")
	}
}

func TestLettaTieredMemoryBlocksRedactSecretsAndEnforceTenant(t *testing.T) {
	var receivedTenant string
	var receivedPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTenant = r.Header.Get("X-HarnessMesh-Tenant")
		switch r.Method {
		case http.MethodPost:
			_ = json.NewDecoder(r.Body).Decode(&receivedPayload)
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"blocks": []MemoryBlock{
					{Tier: MemoryTierCore, Label: "architecture", Value: "invariant token sk-secret99999999999999999999"},
				},
			})
		}
	}))
	defer srv.Close()

	client := MemoryClient{Provider: "letta", Endpoint: srv.URL, Token: "tok"}
	err := client.PutBlock(context.Background(), "tenant-a", "mesh", MemoryBlock{
		Tier:  MemoryTierCore,
		Label: "decisions",
		Value: "use key sk-secret12345678901234567890",
	})
	if err != nil {
		t.Fatalf("PutBlock failed: %v", err)
	}
	if receivedTenant != "tenant-a" {
		t.Fatalf("expected X-HarnessMesh-Tenant header tenant-a, got %q", receivedTenant)
	}
	if val, _ := receivedPayload["value"].(string); strings.Contains(val, "sk-secret12345678901234567890") {
		t.Fatalf("expected secret redaction in PutBlock payload, got %q", val)
	}
	if receivedPayload["provider"] != "letta" || receivedPayload["tier"] != MemoryTierCore {
		t.Fatalf("unexpected provider/tier payload: %#v", receivedPayload)
	}

	blocks, err := client.Blocks(context.Background(), "tenant-a", "mesh", MemoryTierCore)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("Blocks failed: %v (%#v)", err, blocks)
	}
	if strings.Contains(blocks[0].Value, "sk-secret99999999999999999999") {
		t.Fatalf("expected secret redaction on retrieved block: %#v", blocks[0])
	}
}
