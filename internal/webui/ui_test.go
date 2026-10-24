package webui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardServesSecureShellAndAssets(t *testing.T) {
	h := Handler()
	r := httptest.NewRequest("GET", "/dashboard", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("dashboard status = %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatal("dashboard CSP does not restrict scripts to self")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("dashboard must not be cached")
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("dashboard security headers missing")
	}
	if !strings.Contains(w.Body.String(), "HarnessMesh") {
		t.Fatal("dashboard shell missing brand")
	}

	r = httptest.NewRequest("GET", "/dashboard/assets/app.js", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("asset response = %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("asset security headers missing")
	}
}

func TestDashboardRejectsUnknownPath(t *testing.T) {
	w := httptest.NewRecorder()
	Handler().ServeHTTP(w, httptest.NewRequest("GET", "/dashboard/unknown", nil))
	if w.Code != 404 {
		t.Fatalf("unknown path status = %d", w.Code)
	}
}
