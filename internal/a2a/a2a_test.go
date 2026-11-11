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

func TestOutboundA2AClientAndSSETaskStream(t *testing.T) {
	s, err := NewServer(Config{
		Card:        AgentCard{Name: "crew-remote", URL: "https://crew.test", Version: "1.0", Skills: []Skill{{ID: "research"}}},
		BearerToken: "fed-secret",
		Executor: func(_ context.Context, req TaskRequest) (string, error) {
			return "handled: " + req.Input, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	client := Client{Endpoint: ts.URL, Token: "fed-secret", HTTPClient: ts.Client()}
	card, err := client.Discover(context.Background())
	if err != nil || card.Name != "crew-remote" {
		t.Fatalf("Discover failed: %v (%#v)", err, card)
	}

	task, err := client.SubmitTask(context.Background(), TaskRequest{
		ID:     "task-42",
		Tenant: "tenant-1",
		Input:  "audit config password=\"supersecretvalue\"",
	})
	if err != nil {
		t.Fatalf("SubmitTask failed: %v", err)
	}
	if strings.Contains(task.Output, "supersecretvalue") {
		t.Fatalf("expected secret redaction in outbound A2A task, got %q", task.Output)
	}

	fetched, err := client.GetTask(context.Background(), "tenant-1", "task-42")
	if err != nil || fetched.ID != "task-42" || fetched.Status != "completed" {
		t.Fatalf("GetTask failed: %v (%#v)", err, fetched)
	}

	// Verify SSE streaming endpoint
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/a2a/v1/tasks/task-42/stream", nil)
	req.Header.Set("Authorization", "Bearer fed-secret")
	req.Header.Set("X-HarnessMesh-Tenant", "tenant-1")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", resp.Header.Get("Content-Type"))
	}
}
