package modelrouting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/domehahn/harnessmesh/internal/supervisor"
)

func TestSwitchyardInferenceRouter_RouteNeverSelectsAConcreteModel(t *testing.T) {
	backend := NewSwitchyardBackend("http://switchyard.invalid", false)
	router := NewSwitchyardInferenceRouter(backend)
	router.Routes = map[string]string{"plan": "supervisor-plan"}

	decision, err := router.Route(context.Background(), supervisor.ModelRoutingRequest{Stage: "plan"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if decision.Model != "supervisor-plan" {
		t.Fatalf("expected the configured route alias, got %q", decision.Model)
	}
	// "supervisor-plan" is a logical route, never a concrete model
	// identifier like "gpt-5.6" or "claude-...".
	if decision.Model == "gpt-5.6" || decision.Model == "claude-sonnet-5-5" {
		t.Fatalf("decision must never contain a hardcoded concrete model name")
	}

	// A stage with no explicit mapping falls back to DefaultRoute/"supervisor".
	fallback, err := router.Route(context.Background(), supervisor.ModelRoutingRequest{Stage: "synthesize"})
	if err != nil {
		t.Fatalf("Route (fallback): %v", err)
	}
	if fallback.Model != "supervisor" {
		t.Fatalf("expected default route %q, got %q", "supervisor", fallback.Model)
	}
}

func TestSwitchyardInferenceRouter_Complete_CallsOpenAICompatibleEndpoint(t *testing.T) {
	var gotPath string
	var gotModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var req chatCompletionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{
			Model: "resolved-by-switchyard-internally",
			Choices: []struct {
				Message chatCompletionMessage `json:"message"`
			}{{Message: chatCompletionMessage{Role: "assistant", Content: `{"proposals":[]}`}}},
		})
	}))
	defer server.Close()

	backend := NewSwitchyardBackend(server.URL, false)
	router := NewSwitchyardInferenceRouter(backend)

	decision, err := router.Route(context.Background(), supervisor.ModelRoutingRequest{Stage: "plan"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	result, err := router.Complete(context.Background(), decision, "system prompt", "user prompt", 4096)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotPath != "/v1/chat/completions" {
		t.Fatalf("expected the OpenAI-compatible chat completions path, got %q", gotPath)
	}
	if gotModel != decision.Model {
		t.Fatalf("expected the request to use the routed alias %q, got %q", decision.Model, gotModel)
	}
	if result.Content != `{"proposals":[]}` {
		t.Fatalf("unexpected content: %q", result.Content)
	}
	// ServedModel is what the backend echoed back - observability only,
	// never fed back into routing.
	if result.ServedModel != "resolved-by-switchyard-internally" {
		t.Fatalf("expected the served model to be captured for observability, got %q", result.ServedModel)
	}
}

func TestSwitchyardInferenceRouter_Complete_ResponseTooLarge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		big := make([]byte, 1000)
		for i := range big {
			big[i] = 'x'
		}
		_ = json.NewEncoder(w).Encode(chatCompletionResponse{
			Choices: []struct {
				Message chatCompletionMessage `json:"message"`
			}{{Message: chatCompletionMessage{Content: string(big)}}},
		})
	}))
	defer server.Close()

	backend := NewSwitchyardBackend(server.URL, false)
	router := NewSwitchyardInferenceRouter(backend)
	decision, _ := router.Route(context.Background(), supervisor.ModelRoutingRequest{Stage: "plan"})

	_, err := router.Complete(context.Background(), decision, "s", "u", 10)
	if err == nil {
		t.Fatalf("expected an error when the response exceeds maxResponseBytes")
	}
}

func TestSwitchyardInferenceRouter_Complete_RedirectsAreNotFollowed(t *testing.T) {
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("the redirect target must never be reached")
	}))
	defer redirectTarget.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
	}))
	defer server.Close()

	backend := NewSwitchyardBackend(server.URL, false)
	router := NewSwitchyardInferenceRouter(backend)
	decision, _ := router.Route(context.Background(), supervisor.ModelRoutingRequest{Stage: "plan"})

	_, err := router.Complete(context.Background(), decision, "s", "u", 4096)
	if err == nil {
		t.Fatalf("expected an error when the switchyard endpoint attempts a redirect")
	}
}

func TestSwitchyardInferenceRouter_Complete_CancellationPropagates(t *testing.T) {
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // released below, after Complete has already returned
	}))
	// Deferred in this order so, on unwind, block is closed (letting the
	// stuck handler goroutine return) BEFORE server.Close() blocks
	// waiting for that same handler to finish - defers run LIFO.
	defer server.Close()
	defer close(block)

	backend := NewSwitchyardBackend(server.URL, false)
	router := NewSwitchyardInferenceRouter(backend)
	decision, _ := router.Route(context.Background(), supervisor.ModelRoutingRequest{Stage: "plan"})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := router.Complete(ctx, decision, "s", "u", 4096)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected an error once the caller's context deadline is exceeded")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Complete did not respect the caller's short context deadline, took %s", elapsed)
	}
}

func TestSwitchyardInferenceRouter_ImplementsSupervisorInterfaces(t *testing.T) {
	var _ supervisor.ModelRouter = (*SwitchyardInferenceRouter)(nil)
	var _ supervisor.InferenceTransport = (*SwitchyardInferenceRouter)(nil)
}
