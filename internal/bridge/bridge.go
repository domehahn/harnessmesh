// Package bridge implements the local REST + WebSocket API the VS Code
// extension speaks, so the extension never has to speak raw MCP JSON-RPC
// itself. It is a thin, versioned transport over the same
// collaboration.Engine (and the same EventBus) that the MCP server uses -
// there is no second collaboration/task/event model here, only a different
// wire protocol for the same state.
//
// Like the MCP server, the bridge performs no LLM reasoning and never
// contacts a metered backend; it transports collaboration state between
// the ChatGPT MCP peer, Claude Code (via this bridge), and whatever other
// participants are configured.
package bridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

const (
	maxBodyBytes      = 5 << 20 // 5 MiB per bridge request
	defaultRateLimit  = 240     // requests/minute/server (single local client in practice)
	defaultListenAddr = "127.0.0.1:8788"
)

// Config configures a bridge Server. It intentionally has no field that
// could route a request to a metered LLM backend.
type Config struct {
	Listen           string
	Token            string
	AllowedOrigins   []string
	WebSocketEnabled bool
	// Caller is the participant identity the bridge acts as when talking to
	// the engine (normally the managed/writable executor's name, since the
	// bridge and Claude Code sit on the same trusted, local side of the
	// architecture - see docs/chatgpt-integration.md).
	Caller    string
	RateLimit int
}

// Server is the bridge's HTTP(+WebSocket) server.
type Server struct {
	engine *collaboration.Engine
	cfg    Config

	hub *hub
	idc *idempotencyCache

	rateMu     sync.Mutex
	rateWindow time.Time
	rateCount  int

	requests      atomic.Uint64
	errors        atomic.Uint64
	denials       atomic.Uint64
	wsConnects    atomic.Uint64
	wsDisconnects atomic.Uint64

	allowedOrigins []string

	closeOnce sync.Once
	unlisten  func()
}

// NewServer constructs a bridge Server bound to engine. It registers a
// single listener on the engine's existing EventBus; call Close to
// unregister it if the server is torn down before the engine.
func NewServer(engine *collaboration.Engine, cfg Config) *Server {
	if cfg.Listen == "" {
		cfg.Listen = defaultListenAddr
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = defaultRateLimit
	}
	if cfg.Caller == "" {
		cfg.Caller = "vscode-bridge"
	}
	s := &Server{
		engine:         engine,
		cfg:            cfg,
		hub:            newHub(),
		idc:            newIdempotencyCache(10*time.Minute, 10000),
		allowedOrigins: cfg.AllowedOrigins,
	}
	if engine != nil {
		if eb := engine.EventBus(); eb != nil {
			eb.AddListener("*", s.bridgeEventListener)
		}
	}
	return s
}

// Handler returns the bridge's http.Handler. Exposed separately from Serve
// so tests can drive it with httptest without binding a real listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "harnessmesh_bridge_requests_total %d\n", s.requests.Load())
		fmt.Fprintf(w, "harnessmesh_bridge_errors_total %d\n", s.errors.Load())
		fmt.Fprintf(w, "harnessmesh_bridge_denials_total %d\n", s.denials.Load())
		fmt.Fprintf(w, "harnessmesh_bridge_ws_connects_total %d\n", s.wsConnects.Load())
		fmt.Fprintf(w, "harnessmesh_bridge_ws_disconnects_total %d\n", s.wsDisconnects.Load())
		fmt.Fprintf(w, "# hub: %s\n", fmtEventCounts(s.hub))
	})

	api := http.NewServeMux()
	api.HandleFunc("/api/v1/workspaces", s.handleWorkspaces)
	api.HandleFunc("/api/v1/workspaces/", s.handleWorkspaceByID)
	api.HandleFunc("/api/v1/inbox", s.handleInbox)
	api.HandleFunc("/api/v1/messages", s.handleMessages)
	api.HandleFunc("/api/v1/tasks", s.handleTasks)
	api.HandleFunc("/api/v1/tasks/", s.handleTaskByID)
	api.HandleFunc("/api/v1/artifacts", s.handleArtifacts)
	api.HandleFunc("/api/v1/findings", s.handleFindings)
	api.HandleFunc("/api/v1/reviews", s.handleFindings) // reviews == findings; see docs/chatgpt-integration.md
	api.HandleFunc("/api/v1/events", s.handleEventsPoll)
	if s.cfg.WebSocketEnabled {
		api.HandleFunc("/api/v1/events/ws", s.serveWebSocket)
	}

	mux.Handle("/api/v1/", s.withMiddleware(api))
	return mux
}

// withMiddleware applies, in order: strict CORS headers (never wildcard for
// an authenticated endpoint), bearer-token auth, a bounded body size, and a
// fixed-window rate limit.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")

		origin := r.Header.Get("Origin")
		if origin != "" {
			if !s.checkOrigin(r) {
				s.denials.Add(1)
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if !s.authorized(r) {
			s.denials.Add(1)
			if r.URL.Path != "/api/v1/events/ws" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="harnessmesh-bridge"`)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if !s.allowRequest(time.Now()) {
			s.denials.Add(1)
			w.Header().Set("Retry-After", "60")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	if s.cfg.Token == "" {
		// Fail closed: an unconfigured bridge accepts no requests, matching
		// the MCP server's "no unauthenticated remote endpoint" invariant.
		return false
	}
	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")
	if token == auth { // no "Bearer " prefix
		token = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Token)) == 1
}

func (s *Server) allowRequest(now time.Time) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.rateWindow.IsZero() || now.Sub(s.rateWindow) >= time.Minute {
		s.rateWindow, s.rateCount = now, 0
	}
	if s.rateCount >= s.cfg.RateLimit {
		return false
	}
	s.rateCount++
	return true
}

// Close unregisters the bridge's EventBus listener. Safe to call multiple
// times.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.hub.mu.Lock()
		for c := range s.hub.clients {
			if c.conn != nil {
				_ = c.conn.Close()
			}
		}
		s.hub.mu.Unlock()
	})
}

// Serve starts the HTTP(+WS) listener and blocks until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	if s.cfg.Token == "" {
		return fmt.Errorf("bridge requires a bearer token (Config.Token) - refusing to serve unauthenticated")
	}
	srv := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		s.Close()
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": err.Error()}})
}

func statusForEngineError(err error) int {
	switch err.(type) {
	case *protocol.PolicyDeniedError, *protocol.WriterConflictError, *protocol.ChannelAccessDeniedError:
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}

func decodeJSONBody(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && err != io.EOF {
		return fmt.Errorf("invalid request body: %w", err)
	}
	return nil
}

// withIdempotency wraps a POST handler so a repeated request with the same
// Idempotency-Key header returns the cached first response instead of
// creating a second logical record.
func (s *Server) withIdempotency(route string, fn func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			// Nothing to cache a replay against - skip the capture buffer
			// entirely so the common (non-idempotent) request path never
			// pays for copying the response body.
			fn(w, r)
			return
		}

		// beginOrWait serializes concurrent requests carrying the same key:
		// only one becomes the owner and actually runs fn; any request that
		// races it (the exact scenario an Idempotency-Key exists to guard
		// against) blocks here and then replays the owner's result instead
		// of also executing fn and creating a second logical record.
		if status, body, done := s.idc.beginOrWait(route, key); done {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotency-Replayed", "true")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}

		rec := &captureWriter{ResponseWriter: w, status: http.StatusOK}
		fn(rec, r)
		if rec.status < 500 {
			s.idc.finish(route, key, rec.status, rec.body)
		} else {
			s.idc.abort(route, key)
		}
	}
}

type captureWriter struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (c *captureWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	c.body = append(c.body, b...)
	return c.ResponseWriter.Write(b)
}

func errMissingParam(name string) error {
	return fmt.Errorf("missing required parameter %q", name)
}

func errSpoof(claimed, actual string) error {
	return fmt.Errorf("cannot spoof identity %q (bridge authenticated as %q)", claimed, actual)
}

// resolveCaller returns the bridge's own caller identity, or an error if
// claimed names a different identity. Every mutating endpoint uses this so
// a request body can never assert an identity other than the one this
// bridge instance is configured/authenticated as.
func (s *Server) resolveCaller(claimed string) (string, error) {
	if claimed != "" && claimed != s.cfg.Caller {
		return "", errSpoof(claimed, s.cfg.Caller)
	}
	return s.cfg.Caller, nil
}

func errUnsupportedAction(action string) error {
	return fmt.Errorf("unsupported action %q", action)
}

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
