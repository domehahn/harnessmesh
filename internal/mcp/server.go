package mcp

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/knowledge"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/telemetry"
)

type Server struct {
	engine          *collaboration.Engine
	sessionID       string
	caller          string
	mu              sync.Mutex
	requests        atomic.Uint64
	errors          atomic.Uint64
	allowedProjects map[string]struct{}
	allowedCallers  map[string]struct{}
	rateMu          sync.Mutex
	rateWindow      time.Time
	rateCount       int
	rateLimit       int
	metrics         *telemetry.Registry
}

// ServeHTTP exposes the same JSON-RPC MCP server for remote harnesses.
// A non-empty token is mandatory; callers must send Authorization: Bearer <token>.
func (s *Server) ServeHTTP(ctx context.Context, listen, token string) error {
	return s.serveHTTP(ctx, listen, token, "", "")
}

// ServeHTTPWithTLS enables native TLS when certificate and key paths are supplied.
func (s *Server) ServeHTTPWithTLS(ctx context.Context, listen, token, certFile, keyFile string) error {
	return s.serveHTTP(ctx, listen, token, certFile, keyFile)
}

func (s *Server) serveHTTP(ctx context.Context, listen, token, certFile, keyFile string) error {
	introspectionURL := os.Getenv("HARNESSMESH_MCP_OAUTH_INTROSPECTION_URL")
	if strings.TrimSpace(token) == "" && introspectionURL == "" {
		return fmt.Errorf("remote MCP requires a bearer token or OAuth introspection URL")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "harnessmesh_mcp_requests_total %d\nharnessmesh_mcp_errors_total %d\n%s", s.requests.Load(), s.errors.Load(), s.metrics.Prometheus())
	})
	mux.HandleFunc("/admin/knowledge", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !s.adminAuthorized(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if s.engine == nil {
			http.Error(w, "engine unavailable", http.StatusServiceUnavailable)
			return
		}
		stats, err := s.engine.KnowledgeStats(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stats)
	})
	mux.HandleFunc("/admin/operations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !s.adminAuthorized(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		health, err := s.engine.AgentHealth(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		retries, err := s.engine.Store().ListRetries(r.Context(), 200)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		dead, err := s.engine.Store().ListDeadLetters(r.Context(), 200)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"agents": health, "retry_queue": retries, "dead_letters": dead, "metrics": s.engine.OperationalMetrics()})
	})
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !s.adminAuthorized(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'")
		_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>HarnessMesh Operations</title><style>body{font:14px system-ui;margin:2rem;background:#111;color:#eee}pre{background:#222;padding:1rem;white-space:pre-wrap}a{color:#8cf}</style><h1>HarnessMesh Operations</h1><p><a href="/admin/knowledge">Knowledge</a> · <a href="/metrics">Metrics</a></p><pre id="out">Loading…</pre><script>fetch('/admin/operations',{headers:{'Authorization':localStorage.getItem('harnessmesh_token')||''}}).then(r=>r.text()).then(t=>{document.getElementById('out').textContent=t}).catch(e=>document.getElementById('out').textContent=e)</script>`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !s.allowRequest(time.Now()) {
			s.errors.Add(1)
			w.Header().Set("Retry-After", "60")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		authorized := token != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
		if !authorized && introspectionURL != "" {
			authorized = introspectOAuthToken(r.Context(), introspectionURL, provided, os.Getenv("HARNESSMESH_MCP_OAUTH_CLIENT_SECRET"))
		}
		if !authorized {
			s.errors.Add(1)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.requests.Add(1)
		s.metrics.Counter("harnessmesh_mcp_requests_total").Add(1)
		body := http.MaxBytesReader(w, r.Body, 10<<20)
		defer body.Close()
		raw, err := io.ReadAll(body)
		if err != nil {
			s.errors.Add(1)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		resp, err := s.HandleMessage(r.Context(), raw)
		if err != nil {
			s.errors.Add(1)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	server := &http.Server{Addr: listen, Handler: securityHeaders(mux), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	var err error
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return fmt.Errorf("both TLS certificate and key are required")
		}
		err = server.ListenAndServeTLS(certFile, keyFile)
	} else {
		err = server.ListenAndServe()
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) adminAuthorized(r *http.Request, token string) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
		return false
	}
	allowed := parseACL(os.Getenv("HARNESSMESH_MCP_ADMIN_CALLERS"))
	if len(allowed) == 0 {
		return true
	}
	_, ok := allowed[s.caller]
	return ok
}

func introspectOAuthToken(ctx context.Context, endpoint, token, clientSecret string) bool {
	if token == "" {
		return false
	}
	form := url.Values{"token": []string{token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if clientSecret != "" {
		req.Header.Set("Authorization", "Bearer "+clientSecret)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var result struct {
		Active bool `json:"active"`
	}
	return json.NewDecoder(resp.Body).Decode(&result) == nil && result.Active
}

func NewServer(engine *collaboration.Engine, sessionID, caller string) *Server {
	if caller == "" {
		caller = "participant"
	}
	server := &Server{
		engine:    engine,
		sessionID: sessionID,
		caller:    caller,
	}
	server.metrics = telemetry.NewRegistry()
	server.allowedProjects = parseACL(os.Getenv("HARNESSMESH_MCP_PROJECTS"))
	server.allowedCallers = parseACL(os.Getenv("HARNESSMESH_MCP_CALLERS"))
	server.rateLimit = 120
	if configured, err := strconv.Atoi(os.Getenv("HARNESSMESH_MCP_RATE_LIMIT")); err == nil && configured > 0 {
		server.rateLimit = configured
	}
	return server
}

func (s *Server) allowRequest(now time.Time) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.rateWindow.IsZero() || now.Sub(s.rateWindow) >= time.Minute {
		s.rateWindow, s.rateCount = now, 0
	}
	if s.rateCount >= s.rateLimit {
		return false
	}
	s.rateCount++
	return true
}

func parseACL(raw string) map[string]struct{} {
	allowed := make(map[string]struct{})
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			allowed[value] = struct{}{}
		}
	}
	return allowed
}

func (s *Server) authorizeProject(projectID string) error {
	if len(s.allowedProjects) == 0 || projectID == "" {
		return nil
	}
	if _, ok := s.allowedProjects[projectID]; !ok {
		return fmt.Errorf("project %q is not allowed for this MCP server", projectID)
	}
	return nil
}

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type ToolCallContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolCallResult struct {
	Content []ToolCallContent `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

func (s *Server) ListTools() []ToolDefinition {
	tools := []ToolDefinition{
		{
			Name:        "peer.list",
			Description: "List all known peer agents, their adapter types, roles, and status in the session.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{
						"type":        "string",
						"description": "Optional session ID",
					},
				},
			},
		},
		{
			Name:        "peer.capabilities",
			Description: "Discover peer agents matching a required capability or role.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"capability": map[string]any{
						"type":        "string",
						"description": "Required capability or role (e.g. 'security_review', 'performance_review', 'review', 'answer_questions')",
					},
				},
				"required": []string{"capability"},
			},
		},
		{
			Name:        "peer.ask",
			Description: "Ask a peer agent for targeted help or code inspection while working.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"peer": map[string]any{
						"type":        "string",
						"description": "Name of the target peer agent (e.g. 'codex', 'antigravity')",
					},
					"capability": map[string]any{
						"type":        "string",
						"description": "Optional capability to route the question to if peer is omitted",
					},
					"question": map[string]any{
						"type":        "string",
						"description": "The specific question or inspection request",
					},
					"scope": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Repository file paths or directories to inspect",
					},
					"context": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"include_diff":       map[string]any{"type": "boolean"},
							"include_tests":      map[string]any{"type": "boolean"},
							"include_git_status": map[string]any{"type": "boolean"},
						},
					},
					"idempotency_key": map[string]any{
						"type":        "string",
						"description": "Optional idempotency key to prevent duplicate expensive executions",
					},
				},
				"required": []string{"question"},
			},
		},
		{
			Name:        "peer.request_review",
			Description: "Request a structured review of work-in-progress or completed changes from one or more peer reviewers.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"peer": map[string]any{
						"type":        "string",
						"description": "Name of the target peer reviewer",
					},
					"capability": map[string]any{
						"type":        "string",
						"description": "Optional capability to resolve a reviewer if peer is omitted",
					},
					"reviewers": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"peer":       map[string]any{"type": "string"},
								"capability": map[string]any{"type": "string"},
								"focus":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							},
						},
						"description": "Optional list of multiple reviewers for parallel multi-review",
					},
					"scope": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "File paths or package scopes under review",
					},
					"focus": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Review focus areas (e.g. 'correctness', 'concurrency', 'security', 'test coverage')",
					},
					"changed_files_only": map[string]any{"type": "boolean"},
					"include_diff":       map[string]any{"type": "boolean"},
					"include_tests":      map[string]any{"type": "boolean"},
					"minimum_severity":   map[string]any{"type": "string"},
					"idempotency_key":    map[string]any{"type": "string"},
				},
			},
		},
		{
			Name:        "peer.submit_finding",
			Description: "Publish a structured finding into the shared collaboration session.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":             map[string]any{"type": "string"},
					"severity":       map[string]any{"type": "string", "enum": []string{"info", "low", "medium", "high", "critical"}},
					"category":       map[string]any{"type": "string"},
					"claim":          map[string]any{"type": "string"},
					"evidence":       map[string]any{"type": "string"},
					"recommendation": map[string]any{"type": "string"},
					"file":           map[string]any{"type": "string"},
					"line":           map[string]any{"type": "integer"},
					"status":         map[string]any{"type": "string", "enum": []string{"open", "acknowledged", "disputed", "resolved", "dismissed"}},
				},
				"required": []string{"severity", "claim", "evidence", "recommendation"},
			},
		},
		{
			Name:        "peer.submit_evidence",
			Description: "Submit verifiable repository evidence (test results, diffs, static analysis) linked to findings.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"finding_id": map[string]any{"type": "string"},
					"type":       map[string]any{"type": "string"},
					"command":    map[string]any{"type": "string"},
					"result":     map[string]any{"type": "string"},
					"excerpt":    map[string]any{"type": "string"},
					"exit_code":  map[string]any{"type": "integer"},
				},
				"required": []string{"type"},
			},
		},
		{
			Name:        "peer.challenge",
			Description: "Formally challenge a peer finding with counter-claims and evidence, marking it as disputed.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"finding_id":             map[string]any{"type": "string"},
					"claim":                  map[string]any{"type": "string"},
					"evidence":               map[string]any{"type": "string"},
					"requested_verification": map[string]any{"type": "string"},
				},
				"required": []string{"finding_id", "claim", "evidence"},
			},
		},
		{
			Name:        "peer.resolve",
			Description: "Resolve an open or disputed finding with evidence-driven rationale.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"finding_id":          map[string]any{"type": "string"},
					"status":              map[string]any{"type": "string", "enum": []string{"confirmed", "rejected", "partially_confirmed", "superseded", "requires_human"}},
					"rationale":           map[string]any{"type": "string"},
					"evidence_references": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"finding_id", "status", "rationale"},
			},
		},
		{
			Name:        "peer.reply",
			Description: "Provide an explicit structured reply to a parent peer message.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"parent_message_id":      map[string]any{"type": "string"},
					"target_peer":            map[string]any{"type": "string"},
					"message":                map[string]any{"type": "string"},
					"evidence_references":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"finding_references":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"expected_response_type": map[string]any{"type": "string"},
				},
				"required": []string{"parent_message_id", "target_peer", "message"},
			},
		},
		{
			Name:        "peer.converse",
			Description: "Converse interactively with a peer agent (e.g. OpenAI Codex) for a second opinion, architecture check, code review, debugging help, security analysis, or validation. Seamlessly maintains peer thread context across multiple turns.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"peer": map[string]any{
						"type":        "string",
						"description": "Name of target peer agent (e.g. 'codex', 'openai-reviewer'). If omitted, routes by capability.",
					},
					"capability": map[string]any{
						"type":        "string",
						"description": "Optional capability or role to route to if peer name is omitted (e.g. 'review', 'architecture', 'security')",
					},
					"message": map[string]any{
						"type":        "string",
						"description": "The message, review request, architectural question, or follow-up to the peer",
					},
					"scope": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Repository file paths or directories to inspect or focus on",
					},
					"context": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"include_diff":       map[string]any{"type": "boolean"},
							"include_tests":      map[string]any{"type": "boolean"},
							"include_git_status": map[string]any{"type": "boolean"},
							"files":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						},
					},
					"expected_outcome": map[string]any{
						"type":        "string",
						"description": "Expected outcome: 'second_opinion', 'review', 'architecture_check', 'debugging_help', 'security_analysis', 'validation'",
					},
					"causation_id": map[string]any{
						"type":        "string",
						"description": "Optional message ID being responded to or followed up on",
					},
					"idempotency_key": map[string]any{
						"type":        "string",
						"description": "Optional idempotency key to prevent duplicate calls",
					},
					"approval_id": map[string]any{
						"type":        "string",
						"description": "Approval ID returned when the operation requires human approval",
					},
				},
				"required": []string{"message"},
			},
		},
		{
			Name:        "peer.status",
			Description: "Retrieve current collaboration session status, findings counts, and remaining budget/rounds.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "collaboration.publish",
			Description: "Publish a message, question, finding, or proposal to a channel or thread in the persistent collaboration space.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":  map[string]any{"type": "string", "description": "Collaboration space ID (defaults to active session)"},
					"channel":   map[string]any{"type": "string", "description": "Channel name or ID (e.g. 'general', 'architecture', 'security', 'findings', 'decisions')"},
					"thread_id": map[string]any{"type": "string", "description": "Optional thread ID to continue an existing thread"},
					"subject":   map[string]any{"type": "string", "description": "Subject or title for a new thread"},
					"message":   map[string]any{"type": "string", "description": "The message text or query"},
					"mentions":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Participants to mention and activate (e.g. ['codex', 'copilot'])"},
					"scope":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Repository files or paths relevant to this message"},
					"metadata":  map[string]any{"type": "object", "description": "Optional arbitrary metadata"},
				},
				"required": []string{"message"},
			},
		},
		{
			Name:        "collaboration.reply",
			Description: "Post a reply in an existing collaboration thread.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":  map[string]any{"type": "string", "description": "Collaboration space ID"},
					"channel":   map[string]any{"type": "string", "description": "Channel name or ID"},
					"thread_id": map[string]any{"type": "string", "description": "Thread ID to reply to"},
					"message":   map[string]any{"type": "string", "description": "The reply message text"},
					"mentions":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional mentions"},
					"scope":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional file scope"},
				},
				"required": []string{"thread_id", "message"},
			},
		},
		{
			Name:        "collaboration.inbox",
			Description: "Retrieve messages, mentions, and notifications from the participant's inbox.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":    map[string]any{"type": "string", "description": "Collaboration space ID"},
					"unread_only": map[string]any{"type": "boolean", "description": "Only return unread messages"},
				},
			},
		},
		{
			Name:        "collaboration.channels",
			Description: "List all channels in the collaboration space.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id": map[string]any{"type": "string", "description": "Collaboration space ID"},
				},
			},
		},
		{
			Name:        "collaboration.thread",
			Description: "Retrieve the full message transcript of a thread.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":  map[string]any{"type": "string", "description": "Collaboration space ID"},
					"channel":   map[string]any{"type": "string", "description": "Channel name or ID"},
					"thread_id": map[string]any{"type": "string", "description": "Thread ID"},
				},
				"required": []string{"thread_id"},
			},
		},
		{
			Name:        "collaboration.subscribe",
			Description: "Subscribe to specific events or channels in the collaboration space.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":       map[string]any{"type": "string", "description": "Collaboration space ID"},
					"channels":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Channels to subscribe to"},
					"event_types":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Event types to subscribe to (e.g. 'repository.changed', 'finding.created')"},
					"scope_patterns": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Glob patterns for file scope"},
					"mode":           map[string]any{"type": "string", "description": "Participant activity mode: 'active', 'passive', 'on_demand', 'paused'"},
				},
			},
		},
		{
			Name:        "collaboration.unsubscribe",
			Description: "Remove an event subscription.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"subscription_id": map[string]any{"type": "string", "description": "Subscription ID to delete"},
				},
				"required": []string{"subscription_id"},
			},
		},
		{
			Name:        "collaboration.decide",
			Description: "Propose or accept an evidence-backed architectural or engineering decision.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":            map[string]any{"type": "string", "description": "Collaboration space ID"},
					"action":              map[string]any{"type": "string", "description": "Action: 'propose' or 'accept'"},
					"decision_id":         map[string]any{"type": "string", "description": "Decision ID (required for 'accept')"},
					"title":               map[string]any{"type": "string", "description": "Title of the decision (required for 'propose')"},
					"statement":           map[string]any{"type": "string", "description": "Clear statement of the decision (required for 'propose')"},
					"rationale":           map[string]any{"type": "string", "description": "Technical rationale (required for 'propose')"},
					"evidence_references": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Evidence IDs supporting the decision"},
				},
				"required": []string{"action"},
			},
		},
		{
			Name:        "collaboration.status",
			Description: "Retrieve comprehensive collaboration space status, participants, channels, and decisions.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id": map[string]any{"type": "string", "description": "Collaboration space ID"},
				},
			},
		},
		{
			Name:        "knowledge.search",
			Description: "Search the compressed HarnessMesh knowledge archive for prior discussions, findings, evidence, decisions, and agent events.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":          map[string]any{"type": "string", "description": "All terms must match; empty query lists recent archive records."},
					"session_id":     map[string]any{"type": "string"},
					"space_id":       map[string]any{"type": "string"},
					"project_id":     map[string]any{"type": "string"},
					"kind":           map[string]any{"type": "string", "description": "Optional kind, e.g. message, finding, evidence, decision, or event type."},
					"verified_only":  map[string]any{"type": "boolean"},
					"min_confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
					"limit":          map[string]any{"type": "integer", "maximum": 1000},
					"offset":         map[string]any{"type": "integer", "minimum": 0},
				},
			},
		},
		{
			Name:        "knowledge.context",
			Description: "Return search results formatted as a bounded context block suitable for RAG prompt augmentation.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":          map[string]any{"type": "string"},
					"session_id":     map[string]any{"type": "string"},
					"space_id":       map[string]any{"type": "string"},
					"project_id":     map[string]any{"type": "string"},
					"kind":           map[string]any{"type": "string"},
					"verified_only":  map[string]any{"type": "boolean"},
					"min_confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
					"limit":          map[string]any{"type": "integer", "maximum": 1000},
					"max_chars":      map[string]any{"type": "integer", "maximum": 200000},
				},
			},
		},
		{
			Name:        "knowledge.stats",
			Description: "Return compressed archive path and size information.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        "knowledge.remember",
			Description: "Store an explicit durable lesson, decision, problem, or solution in the knowledge archive.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":           map[string]any{"type": "string"},
					"kind":           map[string]any{"type": "string"},
					"source":         map[string]any{"type": "string"},
					"project_id":     map[string]any{"type": "string"},
					"verified_only":  map[string]any{"type": "boolean"},
					"min_confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
					"tags":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"text"},
			},
		},
		{
			Name:        "knowledge.import",
			Description: "Import an externally captured Claude, ChatGPT, or Markdown transcript into the archive.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":       map[string]any{"type": "string"},
					"source":     map[string]any{"type": "string"},
					"project_id": map[string]any{"type": "string"},
					"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"text"},
			},
		},
		{
			Name:        "knowledge.compact",
			Description: "Apply retention by atomically removing archive records older than the supplied RFC3339 timestamp.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"before": map[string]any{"type": "string", "description": "RFC3339 cutoff timestamp"}},
				"required":   []string{"before"},
			},
		},
		{
			Name:        "knowledge.summary",
			Description: "Create a bounded, evidence-oriented summary of matching historical knowledge for a new task.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":      map[string]any{"type": "string"},
					"project_id": map[string]any{"type": "string"},
					"limit":      map[string]any{"type": "integer", "maximum": 1000},
					"max_chars":  map[string]any{"type": "integer", "maximum": 200000},
				},
			},
		},
		{
			Name:        "knowledge.quality",
			Description: "Assess verification, confidence, expiry, duplicate, and conflict signals in matching knowledge.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":      map[string]any{"type": "string"},
					"project_id": map[string]any{"type": "string"},
					"limit":      map[string]any{"type": "integer", "maximum": 1000},
				},
			},
		},
	}
	tools = append(tools,
		ToolDefinition{Name: "operations.status", Description: "Show agent health, quota waits, circuit breakers, retry backlog, and operational metrics.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}},
		ToolDefinition{Name: "operations.approvals", Description: "List pending or completed human approval requests.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"status": map[string]any{"type": "string"}}}},
		ToolDefinition{Name: "operations.approve", Description: "Approve or reject a pending expensive or sensitive operation.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"approval_id": map[string]any{"type": "string"}, "approved": map[string]any{"type": "boolean"}}, "required": []string{"approval_id", "approved"}}},
		ToolDefinition{
			Name: "knowledge.search_advanced", Description: "Search knowledge with time, verification, confidence, and source filters.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string"}, "since": map[string]any{"type": "string"},
				"until": map[string]any{"type": "string"}, "agent": map[string]any{"type": "string"},
				"source": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
			}},
		},
		ToolDefinition{Name: "operations.dead_letters", Description: "List permanently failed delivery jobs for operator recovery.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "integer"}}}},
		ToolDefinition{Name: "operations.requeue_dead_letter", Description: "Requeue a dead-letter delivery with an optional priority.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "priority": map[string]any{"type": "integer"}}, "required": []string{"id"}}},
	)
	return tools
}

func (s *Server) HandleMessage(ctx context.Context, raw []byte) (*JSONRPCResponse, error) {
	if len(s.allowedCallers) > 0 {
		if _, ok := s.allowedCallers[s.caller]; !ok {
			return nil, fmt.Errorf("caller %q is not allowed for this MCP server", s.caller)
		}
	}
	var req JSONRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   &JSONRPCError{Code: -32700, Message: "Parse error"},
		}, nil
	}

	switch req.Method {
	case "initialize":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "harnessmesh",
					"version": "0.3.0",
				},
			},
		}, nil

	case "notifications/initialized":
		return nil, nil // No response for notifications

	case "ping":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}, nil

	case "tools/list":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": s.ListTools(),
			},
		}, nil

	case "tools/call":
		var callParams struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &JSONRPCError{Code: -32602, Message: "Invalid params"},
			}, nil
		}

		res, err := s.executeTool(ctx, callParams.Name, callParams.Arguments)
		if err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: ToolCallResult{
					Content: []ToolCallContent{
						{Type: "text", Text: fmt.Sprintf("Error: %v", err)},
					},
					IsError: true,
				},
			}, nil
		}

		resJSON, _ := json.MarshalIndent(res, "", "  ")
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ToolCallResult{
				Content: []ToolCallContent{
					{Type: "text", Text: string(resJSON)},
				},
			},
		}, nil

	default:
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &JSONRPCError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)},
		}, nil
	}
}

func (s *Server) executeTool(ctx context.Context, toolName string, argsJSON json.RawMessage) (any, error) {
	if s.sessionID == "" {
		// Auto-create or resolve active session if not provided
		sess, err := s.engine.CreateSession(ctx, "", "MCP live collaboration")
		if err != nil {
			return nil, err
		}
		s.sessionID = sess.ID
	}

	switch toolName {
	case "peer.list":
		var listReq struct {
			SessionID string `json:"session_id"`
		}
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &listReq)
		}
		sessID := listReq.SessionID
		if sessID == "" {
			sessID = s.sessionID
		}
		return s.engine.ListParticipants(ctx, sessID)

	case "peer.capabilities":
		var capReq protocol.PeerCapabilitiesRequest
		if err := json.Unmarshal(argsJSON, &capReq); err != nil {
			return nil, fmt.Errorf("parse peer.capabilities args: %w", err)
		}
		return s.engine.DiscoverCapabilities(ctx, capReq)

	case "peer.ask":
		var askReq struct {
			Peer           string                     `json:"peer"`
			Capability     string                     `json:"capability"`
			Question       string                     `json:"question"`
			Scope          []string                   `json:"scope"`
			Context        protocol.AskContextOptions `json:"context"`
			IdempotencyKey string                     `json:"idempotency_key"`
		}
		if err := json.Unmarshal(argsJSON, &askReq); err != nil {
			return nil, fmt.Errorf("parse peer.ask args: %w", err)
		}
		return s.engine.Ask(ctx, s.sessionID, s.caller, protocol.AskRequest{
			Peer:       askReq.Peer,
			Capability: askReq.Capability,
			Question:   askReq.Question,
			Scope:      askReq.Scope,
			Context:    askReq.Context,
		}, 1, askReq.IdempotencyKey)

	case "peer.request_review":
		var revReq struct {
			Peer             string                  `json:"peer"`
			Capability       string                  `json:"capability"`
			Reviewers        []protocol.ReviewerSpec `json:"reviewers"`
			Scope            []string                `json:"scope"`
			Focus            []string                `json:"focus"`
			ChangedFilesOnly bool                    `json:"changed_files_only"`
			IncludeDiff      bool                    `json:"include_diff"`
			IncludeTests     bool                    `json:"include_tests"`
			MinimumSeverity  string                  `json:"minimum_severity"`
			IdempotencyKey   string                  `json:"idempotency_key"`
		}
		if err := json.Unmarshal(argsJSON, &revReq); err != nil {
			return nil, fmt.Errorf("parse peer.request_review args: %w", err)
		}
		return s.engine.RequestReview(ctx, s.sessionID, s.caller, protocol.ReviewRequestPayload{
			Peer:             revReq.Peer,
			Capability:       revReq.Capability,
			Reviewers:        revReq.Reviewers,
			Scope:            revReq.Scope,
			Focus:            revReq.Focus,
			ChangedFilesOnly: revReq.ChangedFilesOnly,
			IncludeDiff:      revReq.IncludeDiff,
			IncludeTests:     revReq.IncludeTests,
			MinimumSeverity:  revReq.MinimumSeverity,
		}, 1, revReq.IdempotencyKey)

	case "peer.submit_finding":
		var fp protocol.FindingPayload
		if err := json.Unmarshal(argsJSON, &fp); err != nil {
			return nil, fmt.Errorf("parse peer.submit_finding args: %w", err)
		}
		return s.engine.SubmitFinding(ctx, s.sessionID, s.caller, fp)

	case "peer.submit_evidence":
		var ev protocol.EvidencePayload
		if err := json.Unmarshal(argsJSON, &ev); err != nil {
			return nil, fmt.Errorf("parse peer.submit_evidence args: %w", err)
		}
		return s.engine.SubmitEvidence(ctx, s.sessionID, s.caller, ev)

	case "peer.challenge":
		var ch protocol.ChallengePayload
		if err := json.Unmarshal(argsJSON, &ch); err != nil {
			return nil, fmt.Errorf("parse peer.challenge args: %w", err)
		}
		return s.engine.Challenge(ctx, s.sessionID, s.caller, ch)

	case "peer.resolve":
		var res protocol.ResolutionPayload
		if err := json.Unmarshal(argsJSON, &res); err != nil {
			return nil, fmt.Errorf("parse peer.resolve args: %w", err)
		}
		return s.engine.Resolve(ctx, s.sessionID, s.caller, res)

	case "peer.reply":
		var rep protocol.ReplyPayload
		if err := json.Unmarshal(argsJSON, &rep); err != nil {
			return nil, fmt.Errorf("parse peer.reply args: %w", err)
		}
		return s.engine.Reply(ctx, s.sessionID, s.caller, rep, 1, "")

	case "peer.converse":
		var convReq struct {
			Peer                 string                          `json:"peer"`
			Capability           string                          `json:"capability"`
			Message              string                          `json:"message"`
			Scope                []string                        `json:"scope"`
			Context              protocol.ConverseContextOptions `json:"context"`
			ExpectedResponseType string                          `json:"expected_outcome"`
			CausationID          string                          `json:"causation_id"`
			IdempotencyKey       string                          `json:"idempotency_key"`
		}
		if err := json.Unmarshal(argsJSON, &convReq); err != nil {
			return nil, fmt.Errorf("parse peer.converse args: %w", err)
		}
		return s.engine.Converse(ctx, s.sessionID, s.caller, protocol.ConverseRequest{
			Peer:                 convReq.Peer,
			Capability:           convReq.Capability,
			Message:              convReq.Message,
			Scope:                convReq.Scope,
			Context:              convReq.Context,
			ExpectedResponseType: convReq.ExpectedResponseType,
			CausationID:          convReq.CausationID,
		}, 1, convReq.IdempotencyKey)

	case "peer.status":
		return s.engine.Status(ctx, s.sessionID)

	case "collaboration.publish":
		var pubReq protocol.PublishRequest
		if err := json.Unmarshal(argsJSON, &pubReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.publish args: %w", err)
		}
		if pubReq.SpaceID == "" {
			pubReq.SpaceID = s.sessionID
		}
		if pubReq.From == "" {
			pubReq.From = s.caller
		}
		return s.engine.Publish(ctx, &pubReq)

	case "collaboration.reply":
		var repReq struct {
			SpaceID  string   `json:"space_id"`
			Channel  string   `json:"channel"`
			ThreadID string   `json:"thread_id"`
			Message  string   `json:"message"`
			Mentions []string `json:"mentions"`
			Scope    []string `json:"scope"`
		}
		if err := json.Unmarshal(argsJSON, &repReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.reply args: %w", err)
		}
		spaceID := repReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		return s.engine.PublishReply(ctx, spaceID, repReq.Channel, repReq.ThreadID, s.caller, repReq.Message, repReq.Mentions, repReq.Scope)

	case "collaboration.inbox":
		var inReq struct {
			SpaceID    string `json:"space_id"`
			UnreadOnly bool   `json:"unread_only"`
		}
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &inReq)
		}
		spaceID := inReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		return s.engine.GetInbox(ctx, spaceID, s.caller, nil, inReq.UnreadOnly)

	case "collaboration.channels":
		var chReq struct {
			SpaceID string `json:"space_id"`
		}
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &chReq)
		}
		spaceID := chReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		return s.engine.SpaceService().ListChannels(ctx, spaceID)

	case "collaboration.thread":
		var thReq struct {
			SpaceID  string `json:"space_id"`
			Channel  string `json:"channel"`
			ThreadID string `json:"thread_id"`
		}
		if err := json.Unmarshal(argsJSON, &thReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.thread args: %w", err)
		}
		spaceID := thReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		return s.engine.Store().GetSpaceMessages(ctx, spaceID, thReq.Channel, thReq.ThreadID, 100)

	case "collaboration.subscribe":
		var sub protocol.Subscription
		if err := json.Unmarshal(argsJSON, &sub); err != nil {
			return nil, fmt.Errorf("parse collaboration.subscribe args: %w", err)
		}
		if sub.SpaceID == "" {
			sub.SpaceID = s.sessionID
		}
		if sub.ParticipantID == "" {
			sub.ParticipantID = s.caller
		}
		if err := s.engine.Subscribe(ctx, &sub); err != nil {
			return nil, err
		}
		return map[string]any{"status": "subscribed", "subscription_id": sub.ID}, nil

	case "collaboration.unsubscribe":
		var unsubReq struct {
			SpaceID        string `json:"space_id"`
			SubscriptionID string `json:"subscription_id"`
		}
		if err := json.Unmarshal(argsJSON, &unsubReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.unsubscribe args: %w", err)
		}
		if err := s.engine.Unsubscribe(ctx, unsubReq.SpaceID, unsubReq.SubscriptionID); err != nil {
			return nil, err
		}
		return map[string]any{"status": "unsubscribed", "space_id": unsubReq.SpaceID, "subscription_id": unsubReq.SubscriptionID}, nil

	case "collaboration.decide":
		var decReq struct {
			SpaceID            string   `json:"space_id"`
			Action             string   `json:"action"`
			DecisionID         string   `json:"decision_id"`
			Title              string   `json:"title"`
			Statement          string   `json:"statement"`
			Rationale          string   `json:"rationale"`
			EvidenceReferences []string `json:"evidence_references"`
		}
		if err := json.Unmarshal(argsJSON, &decReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.decide args: %w", err)
		}
		spaceID := decReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		if decReq.Action == "accept" {
			return s.engine.AcceptDecision(ctx, spaceID, decReq.DecisionID, s.caller)
		}
		return s.engine.CreateDecision(ctx, spaceID, decReq.Title, decReq.Statement, decReq.Rationale, s.caller, decReq.EvidenceReferences)

	case "collaboration.status":
		var stReq struct {
			SpaceID string `json:"space_id"`
		}
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &stReq)
		}
		spaceID := stReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		return s.engine.SpaceStatus(ctx, spaceID)

	case "operations.status":
		health, err := s.engine.AgentHealth(ctx)
		if err != nil {
			return nil, err
		}
		retries, err := s.engine.Store().ListRetries(ctx, 200)
		if err != nil {
			return nil, err
		}
		return map[string]any{"agents": health, "retry_queue": retries, "metrics": s.engine.OperationalMetrics()}, nil

	case "operations.approvals":
		var req struct {
			Status protocol.ApprovalStatus `json:"status"`
		}
		_ = json.Unmarshal(argsJSON, &req)
		return s.engine.Approvals(ctx, req.Status)

	case "operations.approve":
		var req struct {
			ApprovalID string `json:"approval_id"`
			Approved   bool   `json:"approved"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, err
		}
		return s.engine.DecideApproval(ctx, req.ApprovalID, s.caller, req.Approved)

	case "operations.dead_letters":
		var req struct {
			Limit int `json:"limit"`
		}
		_ = json.Unmarshal(argsJSON, &req)
		return s.engine.Store().ListDeadLetters(ctx, req.Limit)

	case "operations.requeue_dead_letter":
		var req struct {
			ID       string `json:"id"`
			Priority int    `json:"priority"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, err
		}
		if req.ID == "" {
			return nil, fmt.Errorf("dead-letter id is required")
		}
		if err := s.engine.Store().RequeueDeadLetter(ctx, req.ID, time.Now().UTC(), req.Priority); err != nil {
			return nil, err
		}
		return map[string]any{"status": "requeued", "id": req.ID, "priority": req.Priority}, nil

	case "knowledge.search_advanced":
		var req struct {
			Query, Since, Until, Agent, Source string
			Limit                              int
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, err
		}
		var since, until time.Time
		var err error
		if req.Since != "" {
			since, err = time.Parse(time.RFC3339, req.Since)
			if err != nil {
				return nil, fmt.Errorf("invalid since: %w", err)
			}
		}
		if req.Until != "" {
			until, err = time.Parse(time.RFC3339, req.Until)
			if err != nil {
				return nil, fmt.Errorf("invalid until: %w", err)
			}
		}
		records, err := s.engine.KnowledgeSearch(ctx, req.Query, knowledge.SearchOptions{Since: since, Until: until, Agent: req.Agent, Source: req.Source, Limit: req.Limit})
		if err != nil {
			return nil, err
		}
		return map[string]any{"records": records, "count": len(records)}, nil

	case "knowledge.search", "knowledge.context", "knowledge.summary", "knowledge.quality":
		var req struct {
			Query         string  `json:"query"`
			SessionID     string  `json:"session_id"`
			SpaceID       string  `json:"space_id"`
			ProjectID     string  `json:"project_id"`
			Kind          string  `json:"kind"`
			VerifiedOnly  bool    `json:"verified_only"`
			MinConfidence float64 `json:"min_confidence"`
			Limit         int     `json:"limit"`
			Offset        int     `json:"offset"`
			MaxChars      int     `json:"max_chars"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, fmt.Errorf("parse knowledge args: %w", err)
		}
		if err := s.authorizeProject(req.ProjectID); err != nil {
			return nil, err
		}
		records, err := s.engine.KnowledgeSearch(ctx, req.Query, knowledge.SearchOptions{
			SessionID: req.SessionID, SpaceID: req.SpaceID, ProjectID: req.ProjectID, Kind: req.Kind, Limit: req.Limit, Offset: req.Offset, VerifiedOnly: req.VerifiedOnly, MinConfidence: req.MinConfidence,
		})
		if err != nil {
			return nil, err
		}
		if toolName == "knowledge.context" || toolName == "knowledge.summary" {
			if req.MaxChars <= 0 {
				req.MaxChars = 50000
			}
			contextText := knowledge.BuildContext(records, req.MaxChars)
			if toolName == "knowledge.context" {
				return map[string]any{"records": records, "context": contextText}, nil
			}
			counts := map[string]int{}
			for _, record := range records {
				counts[record.Kind]++
			}
			return map[string]any{"records": records, "summary": contextText, "kind_counts": counts}, nil
		}
		if toolName == "knowledge.quality" {
			return map[string]any{"records": records, "quality": knowledge.AssessQuality(records)}, nil
		}
		return map[string]any{"records": records, "count": len(records)}, nil

	case "knowledge.stats":
		return s.engine.KnowledgeStats(ctx)

	case "knowledge.remember", "knowledge.import":
		var req struct {
			Text      string   `json:"text"`
			Kind      string   `json:"kind"`
			Source    string   `json:"source"`
			ProjectID string   `json:"project_id"`
			Tags      []string `json:"tags"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, fmt.Errorf("parse knowledge write args: %w", err)
		}
		if strings.TrimSpace(req.Text) == "" {
			return nil, fmt.Errorf("knowledge text is required")
		}
		if err := s.authorizeProject(req.ProjectID); err != nil {
			return nil, err
		}
		if toolName == "knowledge.import" && req.Kind == "" {
			req.Kind = "transcript"
		}
		if req.Source == "" {
			req.Source = s.caller
		}
		return s.engine.KnowledgeRemember(ctx, req.Text, req.Kind, req.Source, req.ProjectID, req.Tags)

	case "knowledge.compact":
		var req struct {
			Before string `json:"before"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, fmt.Errorf("parse knowledge compact args: %w", err)
		}
		before, err := time.Parse(time.RFC3339, req.Before)
		if err != nil {
			return nil, fmt.Errorf("before must be RFC3339: %w", err)
		}
		kept, err := s.engine.KnowledgeCompact(ctx, before)
		return map[string]any{"kept_records": kept, "before": before}, err

	default:
		return nil, fmt.Errorf("unknown collaboration tool %q", toolName)
	}
}

// ServeStdio starts the MCP JSON-RPC server reading lines from r and writing responses to w.
func (s *Server) ServeStdio(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		resp, err := s.HandleMessage(ctx, []byte(line))
		if err != nil {
			return err
		}
		if resp != nil {
			bytes, err := json.Marshal(resp)
			if err != nil {
				return err
			}
			s.mu.Lock()
			_, _ = w.Write(append(bytes, '\n'))
			s.mu.Unlock()
		}
	}
	return scanner.Err()
}
