package integrations

import "testing"

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
