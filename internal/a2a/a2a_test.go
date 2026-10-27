package a2a

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoveryAndTenantBoundTask(t *testing.T) {
	s, err := NewServer(Config{Card: AgentCard{Name: "mesh", URL: "https://mesh.test", Version: "1", Skills: []Skill{{ID: "review"}}}, BearerToken: "secret", Executor: func(_ context.Context, req TaskRequest) (string, error) { return strings.ToUpper(req.Input), nil }})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	r := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "review") {
		t.Fatalf("unexpected card: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodPost, "/a2a/v1/tasks", strings.NewReader(`{"tenant":"t1","input":"hello"}`))
	r.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "HELLO") {
		t.Fatalf("unexpected task: %d %s", w.Code, w.Body.String())
	}
}

func TestMissingTenantAndAuthFailClosed(t *testing.T) {
	s, err := NewServer(Config{Card: AgentCard{Name: "mesh", URL: "https://mesh.test"}, BearerToken: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}
