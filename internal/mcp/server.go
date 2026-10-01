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
	"github.com/domehahn/harnessmesh/internal/creditguard"
	"github.com/domehahn/harnessmesh/internal/knowledge"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/telemetry"
)

// Supported MCP protocol (spec revision) versions. defaultProtocolVersion is
// used when a client sends no Mcp-Protocol-Version header, and is what the
// server offers back to a client that requested an unsupported version.
const defaultProtocolVersion = "2025-06-18"

var supportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

func isSupportedProtocolVersion(v string) bool {
	for _, sv := range supportedProtocolVersions {
		if sv == v {
			return true
		}
	}
	return false
}

type mcpSession struct {
	id        string
	createdAt time.Time
}

type Server struct {
	engine             *collaboration.Engine
	sessionID          string
	caller             string
	mu                 sync.Mutex
	requests           atomic.Uint64
	errors             atomic.Uint64
	allowedProjects    map[string]struct{}
	allowedCallers     map[string]struct{}
	rateMu             sync.Mutex
	rateWindow         time.Time
	rateCount          int
	rateLimit          int
	metrics            *telemetry.Registry
	sessionsMu         sync.Mutex
	sessions           map[string]*mcpSession
	terminatedSessions map[string]time.Time
}

func (s *Server) recordSession(id string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		s.sessions[id] = &mcpSession{id: id, createdAt: time.Now().UTC()}
	}
}

func (s *Server) deleteSession(id string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	delete(s.sessions, id)
	if s.terminatedSessions == nil {
		s.terminatedSessions = make(map[string]time.Time)
	}
	s.terminatedSessions[id] = time.Now().UTC()
}

func (s *Server) isSessionTerminated(id string) bool {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if s.terminatedSessions == nil {
		return false
	}
	_, terminated := s.terminatedSessions[id]
	return terminated
}

func (s *Server) isSessionKnown(id string) bool {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	if id == s.sessionID {
		return true
	}
	_, ok := s.sessions[id]
	return ok
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
		_, _ = fmt.Fprintf(w, "harnessmesh_metered_backend_calls_total{backend=\"openai-api\"} %d\n", creditguard.Calls(creditguard.BackendOpenAIAPI))
		_, _ = fmt.Fprintf(w, "harnessmesh_metered_backend_calls_total{backend=\"codex\"} %d\n", creditguard.Calls(creditguard.BackendCodex))
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
	streamable := s.StreamableHTTPHandler(token)
	mux.Handle("/mcp", streamable)
	if customPath := os.Getenv("HARNESSMESH_MCP_PATH"); customPath != "" && customPath != "/mcp" {
		mux.Handle(customPath, streamable)
	}
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

func isAllowedCORSOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if allowed := os.Getenv("HARNESSMESH_CORS_ALLOWED_ORIGINS"); allowed != "" {
		if allowed == "*" {
			// Disallow wildcard with Access-Control-Allow-Credentials: true
			return false
		}
		for _, o := range strings.Split(allowed, ",") {
			if strings.TrimSpace(o) == origin {
				return true
			}
		}
		return false
	}
	hostname := strings.ToLower(u.Hostname())
	if (hostname == "localhost" || hostname == "127.0.0.1") && (u.Scheme == "http" || u.Scheme == "https") {
		return true
	}
	if u.Scheme == "https" && (hostname == "chatgpt.com" || strings.HasSuffix(hostname, ".chatgpt.com") || hostname == "openai.com" || strings.HasSuffix(hostname, ".openai.com")) {
		return true
	}
	return false
}

// StreamableHTTPHandler provides the standard Model Context Protocol Streamable HTTP transport.
// It supports POST, GET (SSE or info), DELETE (session termination), and OPTIONS (CORS preflight).
func (s *Server) StreamableHTTPHandler(token string) http.Handler {
	introspectionURL := os.Getenv("HARNESSMESH_MCP_OAUTH_INTROSPECTION_URL")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS headers
		origin := r.Header.Get("Origin")
		if origin != "" && isAllowedCORSOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Mcp-Session-Id, Mcp-Protocol-Version")
			w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, Mcp-Protocol-Version, Mcp-Method, Mcp-Name")
		}

		if r.Method == http.MethodOptions {
			if origin != "" && !isAllowedCORSOrigin(origin) {
				http.Error(w, "cors origin forbidden", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Security headers
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")

		// Rate limiting
		if !s.allowRequest(time.Now()) {
			s.errors.Add(1)
			w.Header().Set("Retry-After", "60")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		// Authentication
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		authorized := token != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
		if !authorized && introspectionURL != "" {
			authorized = introspectOAuthToken(r.Context(), introspectionURL, provided, os.Getenv("HARNESSMESH_MCP_OAUTH_CLIENT_SECRET"))
		}
		if !authorized {
			s.errors.Add(1)
			w.Header().Set("WWW-Authenticate", `Bearer realm="harnessmesh"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// MCP Protocol Negotiation & Session ID
		sessionID := r.Header.Get("Mcp-Session-Id")
		if sessionID != "" {
			if s.isSessionTerminated(sessionID) {
				s.errors.Add(1)
				http.Error(w, fmt.Sprintf("session %q has been terminated", sessionID), http.StatusNotFound)
				return
			}
			if !s.isSessionKnown(sessionID) {
				s.errors.Add(1)
				http.Error(w, fmt.Sprintf("unknown session %q", sessionID), http.StatusNotFound)
				return
			}
		} else {
			if s.sessionID != "" {
				sessionID = s.sessionID
			} else {
				sessionID = fmt.Sprintf("mcp_sess_%d", time.Now().UnixNano())
			}
			s.recordSession(sessionID)
		}

		protocolVersion := r.Header.Get("Mcp-Protocol-Version")
		if protocolVersion == "" {
			protocolVersion = defaultProtocolVersion
		} else if !isSupportedProtocolVersion(protocolVersion) {
			s.errors.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(&JSONRPCResponse{
				JSONRPC: "2.0",
				Error: &JSONRPCError{
					Code:    -32600,
					Message: fmt.Sprintf("unsupported Mcp-Protocol-Version %q; supported versions: %s", protocolVersion, strings.Join(supportedProtocolVersions, ", ")),
				},
			})
			return
		}
		w.Header().Set("Mcp-Session-Id", sessionID)
		w.Header().Set("Mcp-Protocol-Version", protocolVersion)

		switch r.Method {
		case http.MethodDelete:
			s.deleteSession(sessionID)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"session_terminated"}`))
			return

		case http.MethodGet:
			accept := r.Header.Get("Accept")
			if strings.Contains(accept, "text/event-stream") {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				flusher, ok := w.(http.Flusher)
				if !ok {
					http.Error(w, "streaming unsupported", http.StatusInternalServerError)
					return
				}
				_, _ = fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", sessionID)
				flusher.Flush()
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":           "ok",
				"transport":        "streamable-http",
				"protocol_version": protocolVersion,
				"session_id":       sessionID,
				"caller":           s.caller,
			})
			return

		case http.MethodPost:
			s.requests.Add(1)
			s.metrics.Counter("harnessmesh_mcp_requests_total").Add(1)

			body := http.MaxBytesReader(w, r.Body, 10<<20)
			defer body.Close()
			raw, err := io.ReadAll(body)
			if err != nil {
				s.errors.Add(1)
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}

			// Parse JSON-RPC header mirroring
			var msg struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      any             `json:"id,omitempty"`
				Method  string          `json:"method"`
				Params  json.RawMessage `json:"params,omitempty"`
			}
			if err := json.Unmarshal(raw, &msg); err == nil && msg.Method != "" {
				w.Header().Set("Mcp-Method", msg.Method)
				if msg.Method == "tools/call" {
					var toolParams struct {
						Name string `json:"name"`
					}
					if err := json.Unmarshal(msg.Params, &toolParams); err == nil && toolParams.Name != "" {
						w.Header().Set("Mcp-Name", toolParams.Name)
					}
				}
			}

			resp, err := s.HandleMessage(r.Context(), raw)
			if err != nil {
				s.errors.Add(1)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			// Notification (no ID) returns 202 Accepted
			if msg.ID == nil && resp == nil {
				w.WriteHeader(http.StatusAccepted)
				return
			}

			accept := r.Header.Get("Accept")
			if strings.Contains(accept, "text/event-stream") {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				respBytes, err := json.Marshal(resp)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				flusher, ok := w.(http.Flusher)
				if ok {
					_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(respBytes))
					flusher.Flush()
					return
				}
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
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
	server.sessions = make(map[string]*mcpSession)
	server.terminatedSessions = make(map[string]time.Time)
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
		ToolDefinition{
			Name: "change.create", Description: "Propose a new evidence-gated change transaction with title, intent, space/session. Returns change summary, TreeHash, and locked proof obligations.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"title":         map[string]any{"type": "string", "description": "Title of the change"},
				"intent":        map[string]any{"type": "string", "description": "High-level goal or reason for the change"},
				"space_id":      map[string]any{"type": "string", "description": "Collaboration space ID"},
				"session_id":    map[string]any{"type": "string", "description": "Session ID"},
				"repository_id": map[string]any{"type": "string", "description": "Repository identifier"},
				"author":        map[string]any{"type": "string", "description": "Author participant ID"},
				"base_commit":   map[string]any{"type": "string", "description": "Base Git commit hash"},
			}, "required": []string{"title"}},
		},
		ToolDefinition{
			Name: "change.prepare", Description: "Transition a change from draft to prepared, re-evaluating working tree changes.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"change_id": map[string]any{"type": "string", "description": "Change transaction ID"},
			}, "required": []string{"change_id"}},
		},
		ToolDefinition{
			Name: "change.status", Description: "Inspect full change state: status, current TreeHash, verified TreeHash, proof obligations, evidence, and gate status.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"change_id": map[string]any{"type": "string", "description": "Change transaction ID"},
			}, "required": []string{"change_id"}},
		},
		ToolDefinition{
			Name: "change.diff", Description: "Get affected paths and content hashes for the change.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"change_id": map[string]any{"type": "string", "description": "Change transaction ID"},
			}, "required": []string{"change_id"}},
		},
		ToolDefinition{
			Name: "change.verify", Description: "Trigger automated execution of a proof obligation.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"change_id":     map[string]any{"type": "string", "description": "Change transaction ID"},
				"obligation_id": map[string]any{"type": "string", "description": "Proof obligation ID to execute"},
			}, "required": []string{"change_id", "obligation_id"}},
		},
		ToolDefinition{
			Name: "change.evidence", Description: "Submit structured verification evidence (e.g. peer review, test log, static analysis artifact).",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"change_id":          map[string]any{"type": "string", "description": "Change transaction ID"},
				"obligation_id":      map[string]any{"type": "string", "description": "Associated proof obligation ID"},
				"tree_hash":          map[string]any{"type": "string", "description": "Exact working tree hash verified"},
				"source_participant": map[string]any{"type": "string", "description": "Participant providing the evidence"},
				"evidence_type":      map[string]any{"type": "string", "description": "Type of evidence (e.g. 'peer_review', 'unit_tests', 'security_audit')"},
				"result":             map[string]any{"type": "string", "enum": []string{"passed", "failed"}, "description": "Verification result"},
				"command":            map[string]any{"type": "string", "description": "Command executed if applicable"},
				"exit_code":          map[string]any{"type": "integer", "description": "Exit code if applicable"},
				"artifact_hash":      map[string]any{"type": "string", "description": "Hash of output artifact if applicable"},
				"metadata":           map[string]any{"type": "object", "description": "Additional verification metadata"},
			}, "required": []string{"change_id", "tree_hash", "evidence_type", "result"}},
		},
		ToolDefinition{
			Name: "change.abort", Description: "Abort an in-flight change transaction.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"change_id": map[string]any{"type": "string", "description": "Change transaction ID"},
				"reason":    map[string]any{"type": "string", "description": "Reason for aborting the change"},
			}, "required": []string{"change_id"}},
		},
		ToolDefinition{
			Name: "change.commit_status", Description: "Quick check if change is committable and what obligations are pending or failed.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"change_id": map[string]any{"type": "string", "description": "Change transaction ID"},
			}, "required": []string{"change_id"}},
		},
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
		negotiated := defaultProtocolVersion
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 && json.Unmarshal(req.Params, &initParams) == nil && initParams.ProtocolVersion != "" {
			if isSupportedProtocolVersion(initParams.ProtocolVersion) {
				negotiated = initParams.ProtocolVersion
			}
			// If the client requested an unsupported version, the server
			// responds with the latest version it supports (defaultProtocolVersion)
			// per the MCP version-negotiation lifecycle; the client decides
			// whether to proceed or disconnect.
		}
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": negotiated,
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "harnessmesh",
					"version": "0.5.0",
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
			From           string                     `json:"from"`
			Caller         string                     `json:"caller"`
		}
		if err := json.Unmarshal(argsJSON, &askReq); err != nil {
			return nil, fmt.Errorf("parse peer.ask args: %w", err)
		}
		if askReq.From != "" && askReq.From != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", askReq.From, s.caller)
		}
		if askReq.Caller != "" && askReq.Caller != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", askReq.Caller, s.caller)
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
			From             string                  `json:"from"`
			Caller           string                  `json:"caller"`
		}
		if err := json.Unmarshal(argsJSON, &revReq); err != nil {
			return nil, fmt.Errorf("parse peer.request_review args: %w", err)
		}
		if revReq.From != "" && revReq.From != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", revReq.From, s.caller)
		}
		if revReq.Caller != "" && revReq.Caller != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", revReq.Caller, s.caller)
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
		var fp struct {
			protocol.FindingPayload
			From   string `json:"from"`
			Caller string `json:"caller"`
		}
		if err := json.Unmarshal(argsJSON, &fp); err != nil {
			return nil, fmt.Errorf("parse peer.submit_finding args: %w", err)
		}
		if fp.From != "" && fp.From != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", fp.From, s.caller)
		}
		if fp.Caller != "" && fp.Caller != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", fp.Caller, s.caller)
		}
		if fp.SourceParticipant != "" && fp.SourceParticipant != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", fp.SourceParticipant, s.caller)
		}
		if fp.SourceAgent != "" && fp.SourceAgent != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", fp.SourceAgent, s.caller)
		}
		fp.FindingPayload.SourceParticipant = s.caller
		fp.FindingPayload.SourceAgent = s.caller
		return s.engine.SubmitFinding(ctx, s.sessionID, s.caller, fp.FindingPayload)

	case "peer.submit_evidence":
		var ev struct {
			protocol.EvidencePayload
			From   string `json:"from"`
			Caller string `json:"caller"`
		}
		if err := json.Unmarshal(argsJSON, &ev); err != nil {
			return nil, fmt.Errorf("parse peer.submit_evidence args: %w", err)
		}
		if ev.From != "" && ev.From != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", ev.From, s.caller)
		}
		if ev.Caller != "" && ev.Caller != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", ev.Caller, s.caller)
		}
		if ev.SourceAgent != "" && ev.SourceAgent != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", ev.SourceAgent, s.caller)
		}
		ev.EvidencePayload.SourceAgent = s.caller
		return s.engine.SubmitEvidence(ctx, s.sessionID, s.caller, ev.EvidencePayload)

	case "peer.challenge":
		var ch struct {
			protocol.ChallengePayload
			From   string `json:"from"`
			Caller string `json:"caller"`
		}
		if err := json.Unmarshal(argsJSON, &ch); err != nil {
			return nil, fmt.Errorf("parse peer.challenge args: %w", err)
		}
		if ch.From != "" && ch.From != s.caller {
			return nil, fmt.Errorf("cannot spoof challenger identity %q (authenticated as %q)", ch.From, s.caller)
		}
		if ch.Caller != "" && ch.Caller != s.caller {
			return nil, fmt.Errorf("cannot spoof challenger identity %q (authenticated as %q)", ch.Caller, s.caller)
		}
		if ch.Challenger != "" && ch.Challenger != s.caller {
			return nil, fmt.Errorf("cannot spoof challenger identity %q (authenticated as %q)", ch.Challenger, s.caller)
		}
		ch.ChallengePayload.Challenger = s.caller
		return s.engine.Challenge(ctx, s.sessionID, s.caller, ch.ChallengePayload)

	case "peer.resolve":
		var res struct {
			protocol.ResolutionPayload
			From   string `json:"from"`
			Caller string `json:"caller"`
		}
		if err := json.Unmarshal(argsJSON, &res); err != nil {
			return nil, fmt.Errorf("parse peer.resolve args: %w", err)
		}
		if res.From != "" && res.From != s.caller {
			return nil, fmt.Errorf("cannot spoof resolving agent identity %q (authenticated as %q)", res.From, s.caller)
		}
		if res.Caller != "" && res.Caller != s.caller {
			return nil, fmt.Errorf("cannot spoof resolving agent identity %q (authenticated as %q)", res.Caller, s.caller)
		}
		if res.ResolvingAgent != "" && res.ResolvingAgent != s.caller {
			return nil, fmt.Errorf("cannot spoof resolving agent identity %q (authenticated as %q)", res.ResolvingAgent, s.caller)
		}
		res.ResolutionPayload.ResolvingAgent = s.caller
		return s.engine.Resolve(ctx, s.sessionID, s.caller, res.ResolutionPayload)

	case "peer.reply":
		var rep struct {
			protocol.ReplyPayload
			From   string `json:"from"`
			Caller string `json:"caller"`
		}
		if err := json.Unmarshal(argsJSON, &rep); err != nil {
			return nil, fmt.Errorf("parse peer.reply args: %w", err)
		}
		if rep.From != "" && rep.From != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", rep.From, s.caller)
		}
		if rep.Caller != "" && rep.Caller != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", rep.Caller, s.caller)
		}
		return s.engine.Reply(ctx, s.sessionID, s.caller, rep.ReplyPayload, 1, "")

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
			From                 string                          `json:"from"`
			Caller               string                          `json:"caller"`
		}
		if err := json.Unmarshal(argsJSON, &convReq); err != nil {
			return nil, fmt.Errorf("parse peer.converse args: %w", err)
		}
		if convReq.From != "" && convReq.From != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", convReq.From, s.caller)
		}
		if convReq.Caller != "" && convReq.Caller != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", convReq.Caller, s.caller)
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
		if pubReq.From != "" && pubReq.From != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", pubReq.From, s.caller)
		}
		pubReq.From = s.caller
		if pubReq.SpaceID == "" {
			pubReq.SpaceID = s.sessionID
		}
		return s.engine.Publish(ctx, &pubReq)

	case "collaboration.reply":
		var repReq struct {
			SpaceID  string   `json:"space_id"`
			Channel  string   `json:"channel"`
			ThreadID string   `json:"thread_id"`
			From     string   `json:"from"`
			Author   string   `json:"author"`
			Message  string   `json:"message"`
			Mentions []string `json:"mentions"`
			Scope    []string `json:"scope"`
		}
		if err := json.Unmarshal(argsJSON, &repReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.reply args: %w", err)
		}
		if repReq.From != "" && repReq.From != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", repReq.From, s.caller)
		}
		if repReq.Author != "" && repReq.Author != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", repReq.Author, s.caller)
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
		if sub.ParticipantID != "" && sub.ParticipantID != s.caller {
			return nil, fmt.Errorf("cannot spoof participant identity %q (authenticated as %q)", sub.ParticipantID, s.caller)
		}
		sub.ParticipantID = s.caller
		if sub.SpaceID == "" {
			sub.SpaceID = s.sessionID
		}
		if err := s.engine.Subscribe(ctx, &sub); err != nil {
			return nil, err
		}
		return map[string]any{"status": "subscribed", "subscription_id": sub.ID}, nil

	case "collaboration.unsubscribe":
		var unsubReq struct {
			SpaceID        string `json:"space_id"`
			SubscriptionID string `json:"subscription_id"`
			ParticipantID  string `json:"participant_id"`
			From           string `json:"from"`
			Caller         string `json:"caller"`
		}
		if err := json.Unmarshal(argsJSON, &unsubReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.unsubscribe args: %w", err)
		}
		if unsubReq.ParticipantID != "" && unsubReq.ParticipantID != s.caller {
			return nil, fmt.Errorf("cannot spoof participant identity %q (authenticated as %q)", unsubReq.ParticipantID, s.caller)
		}
		if unsubReq.From != "" && unsubReq.From != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", unsubReq.From, s.caller)
		}
		if unsubReq.Caller != "" && unsubReq.Caller != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", unsubReq.Caller, s.caller)
		}
		spaceID := unsubReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		// Authorization check: verify subscription ownership
		subs, err := s.engine.Store().GetParticipantSubscriptions(ctx, spaceID, s.caller)
		if err != nil {
			return nil, fmt.Errorf("lookup subscriptions: %w", err)
		}
		owned := false
		for _, sub := range subs {
			if sub.ID == unsubReq.SubscriptionID {
				owned = true
				break
			}
		}
		if !owned {
			return nil, fmt.Errorf("unauthorized: caller %q does not own subscription %q", s.caller, unsubReq.SubscriptionID)
		}
		if err := s.engine.Unsubscribe(ctx, spaceID, unsubReq.SubscriptionID); err != nil {
			return nil, err
		}
		return map[string]any{"status": "unsubscribed", "space_id": spaceID, "subscription_id": unsubReq.SubscriptionID}, nil

	case "collaboration.decide":
		var decReq struct {
			SpaceID            string   `json:"space_id"`
			Action             string   `json:"action"`
			DecisionID         string   `json:"decision_id"`
			Title              string   `json:"title"`
			Statement          string   `json:"statement"`
			Rationale          string   `json:"rationale"`
			ProposedBy         string   `json:"proposed_by"`
			AcceptedBy         string   `json:"accepted_by"`
			From               string   `json:"from"`
			Caller             string   `json:"caller"`
			EvidenceReferences []string `json:"evidence_references"`
		}
		if err := json.Unmarshal(argsJSON, &decReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.decide args: %w", err)
		}
		if decReq.From != "" && decReq.From != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", decReq.From, s.caller)
		}
		if decReq.Caller != "" && decReq.Caller != s.caller {
			return nil, fmt.Errorf("cannot spoof sender identity %q (authenticated as %q)", decReq.Caller, s.caller)
		}
		if decReq.ProposedBy != "" && decReq.ProposedBy != s.caller {
			return nil, fmt.Errorf("cannot spoof proposer identity %q (authenticated as %q)", decReq.ProposedBy, s.caller)
		}
		if decReq.AcceptedBy != "" && decReq.AcceptedBy != s.caller {
			return nil, fmt.Errorf("cannot spoof acceptor identity %q (authenticated as %q)", decReq.AcceptedBy, s.caller)
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

	case "change.create":
		var req struct {
			ChangeID     string `json:"change_id"`
			SpaceID      string `json:"space_id"`
			SessionID    string `json:"session_id"`
			RepositoryID string `json:"repository_id"`
			Author       string `json:"author"`
			Title        string `json:"title"`
			Intent       string `json:"intent"`
			BaseCommit   string `json:"base_commit"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, fmt.Errorf("parse change.create args: %w", err)
		}
		author := req.Author
		if author == "" {
			author = s.caller
		}
		sessID := req.SessionID
		if sessID == "" {
			sessID = s.sessionID
		}
		chg, err := s.engine.CreateChange(ctx, &protocol.CreateChangeRequest{
			ChangeID:          req.ChangeID,
			SpaceID:           req.SpaceID,
			SessionID:         sessID,
			RepositoryID:      req.RepositoryID,
			AuthorParticipant: author,
			Title:             req.Title,
			Intent:            req.Intent,
			BaseCommit:        req.BaseCommit,
		})
		if err != nil {
			return nil, err
		}
		obls, _ := s.engine.GetProofObligations(ctx, chg.ID)
		paths, _ := s.engine.GetChangePaths(ctx, chg.ID)
		return map[string]any{
			"change":         chg,
			"obligations":    obls,
			"affected_paths": paths,
			"tree_hash":      chg.CurrentTreeHash,
		}, nil

	case "change.prepare":
		var req struct {
			ChangeID string `json:"change_id"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, fmt.Errorf("parse change.prepare args: %w", err)
		}
		if req.ChangeID == "" {
			return nil, fmt.Errorf("change_id is required")
		}
		return s.engine.PrepareChange(ctx, req.ChangeID, s.caller)

	case "change.status":
		var req struct {
			ChangeID string `json:"change_id"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, fmt.Errorf("parse change.status args: %w", err)
		}
		if req.ChangeID == "" {
			return nil, fmt.Errorf("change_id is required")
		}
		chg, err := s.engine.GetMeshChange(ctx, req.ChangeID)
		if err != nil || chg == nil {
			return nil, fmt.Errorf("change %q not found", req.ChangeID)
		}
		obls, _ := s.engine.GetProofObligations(ctx, req.ChangeID)
		evs, _ := s.engine.GetChangeEvidence(ctx, req.ChangeID)
		gate, _ := s.engine.GetLatestGateResult(ctx, req.ChangeID)
		return map[string]any{
			"change":      chg,
			"obligations": obls,
			"evidence":    evs,
			"gate_result": gate,
		}, nil

	case "change.diff":
		var req struct {
			ChangeID string `json:"change_id"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, fmt.Errorf("parse change.diff args: %w", err)
		}
		if req.ChangeID == "" {
			return nil, fmt.Errorf("change_id is required")
		}
		paths, err := s.engine.GetChangePaths(ctx, req.ChangeID)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"change_id": req.ChangeID,
			"paths":     paths,
		}, nil

	case "change.verify":
		var req struct {
			ChangeID     string `json:"change_id"`
			ObligationID string `json:"obligation_id"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, fmt.Errorf("parse change.verify args: %w", err)
		}
		if req.ChangeID == "" || req.ObligationID == "" {
			return nil, fmt.Errorf("change_id and obligation_id are required")
		}
		return s.engine.ExecuteProof(ctx, req.ChangeID, req.ObligationID)

	case "change.evidence":
		var req struct {
			ChangeID          string         `json:"change_id"`
			ObligationID      string         `json:"obligation_id"`
			TreeHash          string         `json:"tree_hash"`
			SourceParticipant string         `json:"source_participant"`
			EvidenceType      string         `json:"evidence_type"`
			Result            string         `json:"result"`
			Command           string         `json:"command"`
			ExitCode          *int           `json:"exit_code"`
			ArtifactHash      string         `json:"artifact_hash"`
			Metadata          map[string]any `json:"metadata"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, fmt.Errorf("parse change.evidence args: %w", err)
		}
		if req.ChangeID == "" || req.EvidenceType == "" || req.Result == "" {
			return nil, fmt.Errorf("change_id, evidence_type, and result ('passed'/'failed') are required")
		}
		source := req.SourceParticipant
		if source == "" {
			source = s.caller
		}
		ev := &protocol.ChangeEvidence{
			ChangeID:          req.ChangeID,
			ObligationID:      req.ObligationID,
			TreeHash:          req.TreeHash,
			SourceParticipant: source,
			EvidenceType:      protocol.EvidenceType(req.EvidenceType),
			Result:            req.Result,
			Command:           req.Command,
			ExitCode:          req.ExitCode,
			ArtifactHash:      req.ArtifactHash,
			Metadata:          req.Metadata,
			Valid:             true,
		}
		return s.engine.SubmitChangeEvidence(ctx, ev)

	case "change.abort":
		var req struct {
			ChangeID string `json:"change_id"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, fmt.Errorf("parse change.abort args: %w", err)
		}
		if req.ChangeID == "" {
			return nil, fmt.Errorf("change_id is required")
		}
		return s.engine.AbortChange(ctx, req.ChangeID, s.caller, req.Reason)

	case "change.commit_status":
		var req struct {
			ChangeID string `json:"change_id"`
		}
		if err := json.Unmarshal(argsJSON, &req); err != nil {
			return nil, fmt.Errorf("parse change.commit_status args: %w", err)
		}
		if req.ChangeID == "" {
			return nil, fmt.Errorf("change_id is required")
		}
		gate, err := s.engine.EvaluateGate(ctx, req.ChangeID)
		if err != nil {
			return nil, err
		}
		chg, _ := s.engine.GetMeshChange(ctx, req.ChangeID)
		var chgStatus protocol.MeshChangeStatus
		if chg != nil {
			chgStatus = chg.Status
		}
		return map[string]any{
			"change_id":     req.ChangeID,
			"committable":   gate.Status == protocol.GateStatusCommittable,
			"gate_status":   gate.Status,
			"change_status": chgStatus,
			"passed":        gate.PassedObligations,
			"pending":       gate.PendingObligations,
			"failed":        gate.FailedObligations,
			"stale":         gate.StaleObligations,
			"reasons":       gate.Reasons,
		}, nil

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
