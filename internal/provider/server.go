package provider

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/creditguard"
)

const (
	defaultRequestMaxBytes = 10 << 20 // 10 MiB
	defaultRequestTimeout  = 10 * time.Minute
	defaultRateLimitPerMin = 300
)

// AuditRecord is one entry in the provider gateway's audit trail (mission
// section 39). Prompts are never included by default.
type AuditRecord struct {
	Timestamp     time.Time `json:"timestamp"`
	RequestID     string    `json:"request_id"`
	CorrelationID string    `json:"correlation_id,omitempty"`
	Client        string    `json:"client,omitempty"`
	Model         string    `json:"model,omitempty"`
	Backend       string    `json:"backend,omitempty"`
	Result        string    `json:"result"` // "ok", "error"
	PolicyDenied  bool      `json:"policy_denied,omitempty"`
	LatencyMS     int64     `json:"latency_ms"`
	StreamStatus  string    `json:"stream_status,omitempty"`
}

// AuditSink receives AuditRecords. Tests use an in-memory implementation;
// production wiring may forward to a log sink.
type AuditSink interface {
	Record(AuditRecord)
}

type noopAuditSink struct{}

func (noopAuditSink) Record(AuditRecord) {}

// Server is the Codex-compatible provider gateway HTTP server. It is
// independent of collaboration.Engine and internal/mcp - the provider
// plane and the collaboration plane share no server-side state.
type Server struct {
	registry *Registry
	cfg      config.ProviderGatewayConfig
	audit    AuditSink

	requests   atomic.Uint64
	errors     atomic.Uint64
	denials    atomic.Uint64
	activeReqs atomic.Int64

	rateMu     sync.Mutex
	rateWindow time.Time
	rateCount  int

	backendCalls    sync.Map // backend name -> *atomic.Uint64
	backendFailures sync.Map

	nextRequestID atomic.Uint64
}

func NewServer(cfg config.ProviderGatewayConfig, registry *Registry) *Server {
	return &Server{registry: registry, cfg: cfg, audit: noopAuditSink{}}
}

func (s *Server) SetAuditSink(a AuditSink) {
	if a == nil {
		a = noopAuditSink{}
	}
	s.audit = a
}

func (s *Server) requestID() string {
	return fmt.Sprintf("req_%d_%d", time.Now().UnixNano(), s.nextRequestID.Add(1))
}

// Handler builds the provider gateway's http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/v1/status", s.withAuth(s.handleStatus))
	mux.HandleFunc("/v1/models", s.withAuth(s.handleModels))
	mux.HandleFunc("/v1/responses", s.withAuth(s.handleResponses))
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleReadyz reports ready only if at least one non-forbidden backend is
// configured and reachable - never reporting ready on the basis of a
// forbidden fallback alone (mission section 32).
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil || len(s.registry.Backends()) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "reason": "no backends configured"})
		return
	}
	policy := s.registry.Policy()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	ready := false
	var lastErr error
	for name, b := range s.registry.Backends() {
		if err := policy.CheckBackendType(name, b.Type()); err != nil {
			continue // forbidden backend never counts toward readiness
		}
		if err := b.Health(ctx); err != nil {
			lastErr = err
			continue
		}
		ready = true
		break
	}
	if !ready {
		reason := "no allowed backend is healthy"
		if lastErr != nil {
			reason = lastErr.Error()
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "reason": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "harnessmesh_provider_requests_total %d\n", s.requests.Load())
	fmt.Fprintf(w, "harnessmesh_provider_errors_total %d\n", s.errors.Load())
	fmt.Fprintf(w, "harnessmesh_provider_streams_active %d\n", s.activeReqs.Load())
	fmt.Fprintf(w, "harnessmesh_zero_credit_policy_denials_total %d\n", s.denials.Load())
	fmt.Fprintf(w, "harnessmesh_metered_backend_calls_total{backend=\"openai-api\"} %d\n", creditguard.Calls(creditguard.BackendOpenAIAPI))
	fmt.Fprintf(w, "harnessmesh_metered_backend_calls_total{backend=\"codex\"} %d\n", creditguard.Calls(creditguard.BackendCodex))
	s.backendCalls.Range(func(k, v any) bool {
		fmt.Fprintf(w, "harnessmesh_provider_backend_calls_total{backend=%q} %d\n", k, v.(*atomic.Uint64).Load())
		return true
	})
	s.backendFailures.Range(func(k, v any) bool {
		fmt.Fprintf(w, "harnessmesh_provider_backend_failures_total{backend=%q} %d\n", k, v.(*atomic.Uint64).Load())
		return true
	})
}

// handleStatus exposes safe operational status (mission section 33). It
// never includes secrets (tokens, API keys).
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	policy := s.registry.Policy()
	backends := map[string]any{}
	for name, b := range s.registry.Backends() {
		backends[name] = map[string]any{
			"type":         b.Type(),
			"capabilities": b.Capabilities(),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"zero_credit_mode":     policy.ZeroCreditMode,
		"default_backend":      policy.DefaultBackend,
		"fallback_enabled":     policy.FallbackEnabled,
		"backends":             backends,
		"requests_total":       s.requests.Load(),
		"errors_total":         s.errors.Load(),
		"policy_denials_total": s.denials.Load(),
		"active_streams":       s.activeReqs.Load(),
		"openai_api_calls":     creditguard.Calls(creditguard.BackendOpenAIAPI),
		"codex_calls":          creditguard.Calls(creditguard.BackendCodex),
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	var models []map[string]any
	for name, b := range s.registry.Backends() {
		models = append(models, map[string]any{"id": name, "object": "model", "backend_type": b.Type()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
}

// withAuth enforces bearer-token auth (fail closed if unconfigured),
// differentiated from the MCP/bridge tokens (mission section 24: provider
// auth never implies admin/collaboration rights - this server has no
// collaboration operations reachable at all).
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")

		if s.cfg.Token == "" {
			s.errors.Add(1)
			writeProviderError(w, http.StatusUnauthorized, &UnauthorizedError{})
			return
		}
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if token == auth || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Token)) != 1 {
			s.errors.Add(1)
			writeProviderError(w, http.StatusUnauthorized, &UnauthorizedError{})
			return
		}
		if !s.allowRequest(time.Now()) {
			s.errors.Add(1)
			w.Header().Set("Retry-After", "60")
			writeProviderError(w, http.StatusTooManyRequests, &RateLimitedError{})
			return
		}

		maxBytes := s.cfg.RequestMaxBytes
		if maxBytes <= 0 {
			maxBytes = defaultRequestMaxBytes
		}
		r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))

		next(w, r)
	}
}

func (s *Server) allowRequest(now time.Time) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.rateWindow.IsZero() || now.Sub(s.rateWindow) >= time.Minute {
		s.rateWindow, s.rateCount = now, 0
	}
	limit := defaultRateLimitPerMin
	if s.rateCount >= limit {
		return false
	}
	s.rateCount++
	return true
}

// handleResponses implements POST /v1/responses.
func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeProviderError(w, http.StatusMethodNotAllowed, &ForbiddenError{Reason: "only POST is supported"})
		return
	}

	start := time.Now()
	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = s.requestID()
	}
	correlationID := r.Header.Get("X-Correlation-Id")
	w.Header().Set("X-Request-Id", requestID)

	dec := json.NewDecoder(r.Body)
	var req Request
	if err := dec.Decode(&req); err != nil {
		if isPayloadTooLarge(err) {
			s.errors.Add(1)
			writeProviderError(w, http.StatusRequestEntityTooLarge, &PayloadTooLargeError{MaxBytes: s.effectiveMaxBytes()})
			return
		}
		s.errors.Add(1)
		writeProviderError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if req.Model == "" {
		s.errors.Add(1)
		writeProviderError(w, http.StatusBadRequest, fmt.Errorf("model is required"))
		return
	}

	backendName := backendNameFromRequest(r, req)
	backend, err := s.registry.Resolve(backendName)
	if err != nil {
		s.recordDenialIfPolicy(err)
		s.audit.Record(AuditRecord{Timestamp: start, RequestID: requestID, CorrelationID: correlationID, Model: req.Model, Backend: backendName, Result: "error", PolicyDenied: isPolicyError(err), LatencyMS: time.Since(start).Milliseconds()})
		writeProviderError(w, statusForProviderError(err), err)
		return
	}

	s.activeReqs.Add(1)
	defer s.activeReqs.Add(-1)

	ctx, cancel := context.WithTimeout(r.Context(), s.effectiveTimeout())
	defer cancel()

	// Build the fallback attempt order: the resolved backend first, then
	// any policy-allowed fallback.order entries not already tried. This is
	// the only place fallback is consulted - explicit backend selection
	// (X-HarnessMesh-Backend) that Resolve itself denies is never silently
	// retried against a different backend; fallback only engages for a
	// backend that resolved successfully but then failed to *execute*.
	attempts := []InferenceBackend{backend}
	if s.registry.Policy().FallbackEnabled {
		tried := map[string]bool{backend.Name(): true}
		for _, fbName := range s.registry.FallbackChain() {
			if tried[fbName] {
				continue
			}
			tried[fbName] = true
			if fb, err := s.registry.Resolve(fbName); err == nil {
				attempts = append(attempts, fb)
			}
		}
	}

	if req.Stream {
		s.streamHTTP(ctx, w, r, attempts, req, requestID, correlationID, start)
		return
	}
	s.bufferedHTTP(ctx, w, attempts, req, requestID, correlationID, start)
}

// streamHTTP serves a streaming request. With no fallback configured (the
// default, and every case this package's non-fallback tests cover), the
// sole attempt's events are forwarded to the client in real time as they
// arrive - streaming is never buffered. When fallback.enabled is true and
// more than one backend is available, each attempt is buffered internally
// first (so a client never sees a partial stream from a backend that then
// fails) and only a fully successful attempt's events are replayed to the
// client; this is a deliberate streaming-latency tradeoff documented in
// docs/codex-provider.md, scoped to the advanced fallback configuration.
func (s *Server) streamHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request, attempts []InferenceBackend, req Request, requestID, correlationID string, start time.Time) {
	sink, err := newSSESink(w, r)
	if err != nil {
		s.errors.Add(1)
		writeProviderError(w, http.StatusInternalServerError, err)
		return
	}

	var usedBackend InferenceBackend
	if len(attempts) == 1 {
		usedBackend = attempts[0]
		err = s.runBackend(ctx, attempts[0], req, sink)
	} else {
		usedBackend, err = s.runWithFallback(ctx, attempts, req, sink)
	}

	status := "ok"
	if err != nil {
		status = "error"
		s.errors.Add(1)
		_ = sink.Send(StreamEvent{Type: "error", Error: errorToResponseError(err)})
	}
	backendName := ""
	if usedBackend != nil {
		backendName = usedBackend.Name()
	}
	s.audit.Record(AuditRecord{Timestamp: start, RequestID: requestID, CorrelationID: correlationID, Model: req.Model, Backend: backendName, Result: status, LatencyMS: time.Since(start).Milliseconds(), StreamStatus: status})
}

func (s *Server) bufferedHTTP(ctx context.Context, w http.ResponseWriter, attempts []InferenceBackend, req Request, requestID, correlationID string, start time.Time) {
	sink := newBufferingSink()
	var final *Response
	var usedBackend InferenceBackend
	errCh := make(chan error, 1)
	go func() {
		defer sink.Close()
		// Unlike streamHTTP, there is no direct-vs-buffered distinction to
		// preserve here: sink is already a bufferingSink regardless, so
		// routing the single-attempt case through runWithFallback (which
		// degrades to one iteration) produces identical output to calling
		// runBackend directly, without a separate code path to keep in sync.
		var err error
		usedBackend, err = s.runWithFallback(ctx, attempts, req, sink)
		errCh <- err
	}()

	for ev := range sink.events {
		if ev.Type == "response.completed" || ev.Type == "response.failed" {
			final = ev.Response
		}
	}
	err := <-errCh
	backend := usedBackend
	if backend == nil && len(attempts) > 0 {
		backend = attempts[0]
	}

	status := "ok"
	if err != nil {
		status = "error"
		s.errors.Add(1)
		s.audit.Record(AuditRecord{Timestamp: start, RequestID: requestID, CorrelationID: correlationID, Model: req.Model, Backend: backend.Name(), Result: status, LatencyMS: time.Since(start).Milliseconds()})
		writeProviderError(w, statusForProviderError(err), err)
		return
	}
	if final == nil {
		s.errors.Add(1)
		writeProviderError(w, http.StatusInternalServerError, fmt.Errorf("backend produced no final response"))
		return
	}
	s.audit.Record(AuditRecord{Timestamp: start, RequestID: requestID, CorrelationID: correlationID, Model: req.Model, Backend: backend.Name(), Result: status, LatencyMS: time.Since(start).Milliseconds()})
	w.Header().Set("X-Request-Id", requestID)
	writeJSON(w, http.StatusOK, final)
}

// runWithFallback tries each backend in attempts, in order. Each attempt is
// buffered internally first; only a fully successful attempt's events are
// replayed to the real sink, so a client is never shown a partial stream
// from a backend that ultimately fails. Returns the backend that succeeded
// (or the last one tried, if all failed) and the last error, if any.
func (s *Server) runWithFallback(ctx context.Context, attempts []InferenceBackend, req Request, sink Sink) (InferenceBackend, error) {
	var lastErr error
	var lastBackend InferenceBackend
	for _, backend := range attempts {
		lastBackend = backend
		attemptSink := newBufferingSink()
		attemptErrCh := make(chan error, 1)
		go func() {
			defer attemptSink.Close()
			attemptErrCh <- s.runBackend(ctx, backend, req, attemptSink)
		}()

		var events []StreamEvent
		for ev := range attemptSink.events {
			events = append(events, ev)
		}
		err := <-attemptErrCh
		if err != nil {
			lastErr = err
			select {
			case <-ctx.Done():
				return backend, ctx.Err()
			default:
			}
			continue
		}
		for _, ev := range events {
			if sErr := sink.Send(ev); sErr != nil {
				return backend, sErr
			}
		}
		return backend, nil
	}
	return lastBackend, lastErr
}

func (s *Server) runBackend(ctx context.Context, backend InferenceBackend, req Request, sink Sink) error {
	s.incBackendCounter(&s.backendCalls, backend.Name())
	err := backend.StreamResponse(ctx, req, sink)
	if err != nil {
		s.incBackendCounter(&s.backendFailures, backend.Name())
	}
	return err
}

func (s *Server) incBackendCounter(m *sync.Map, name string) {
	v, _ := m.LoadOrStore(name, &atomic.Uint64{})
	v.(*atomic.Uint64).Add(1)
}

func (s *Server) recordDenialIfPolicy(err error) {
	if isPolicyError(err) {
		s.denials.Add(1)
	} else {
		s.errors.Add(1)
	}
}

func (s *Server) effectiveMaxBytes() int {
	if s.cfg.RequestMaxBytes > 0 {
		return s.cfg.RequestMaxBytes
	}
	return defaultRequestMaxBytes
}

func (s *Server) effectiveTimeout() time.Duration {
	if s.cfg.RequestTimeoutMS > 0 {
		return time.Duration(s.cfg.RequestTimeoutMS) * time.Millisecond
	}
	return defaultRequestTimeout
}

// backendNameFromRequest lets a caller pin an explicit backend via header
// (X-HarnessMesh-Backend) for testing/operational purposes; Codex itself
// only ever sends "model", so the default_backend is what actually governs
// normal use - there is no implicit selection from model name alone.
func backendNameFromRequest(r *http.Request, req Request) string {
	return r.Header.Get("X-HarnessMesh-Backend")
}

func isPolicyError(err error) bool {
	switch err.(type) {
	case *MeteredBackendDeniedError, *ProviderPolicyDeniedError:
		return true
	}
	return false
}

func isPayloadTooLarge(err error) bool {
	return err != nil && strings.Contains(err.Error(), "http: request body too large")
}

func statusForProviderError(err error) int {
	switch err.(type) {
	case *MeteredBackendDeniedError, *ProviderPolicyDeniedError, *ForbiddenError:
		return http.StatusForbidden
	case *UnauthorizedError:
		return http.StatusUnauthorized
	case *RateLimitedError:
		return http.StatusTooManyRequests
	case *PayloadTooLargeError:
		return http.StatusRequestEntityTooLarge
	case *BackendTimeoutError:
		return http.StatusGatewayTimeout
	case *BackendUnavailableError, *BackendUnsupportedCapabilityError, *StreamInterruptedError:
		return http.StatusBadGateway
	case *ErrUnsupportedOfficialBackend:
		return http.StatusNotImplemented
	}
	return http.StatusBadRequest
}

func errorToResponseError(err error) *ResponseError {
	code := "internal_error"
	etype := "server_error"
	switch err.(type) {
	case *MeteredBackendDeniedError:
		code, etype = "metered_backend_denied", "policy_error"
	case *ProviderPolicyDeniedError:
		code, etype = "provider_policy_denied", "policy_error"
	case *UnauthorizedError:
		code, etype = "unauthorized", "auth_error"
	case *ForbiddenError:
		code, etype = "forbidden", "auth_error"
	case *RateLimitedError:
		code, etype = "rate_limited", "rate_limit_error"
	case *PayloadTooLargeError:
		code, etype = "payload_too_large", "invalid_request_error"
	case *BackendTimeoutError:
		code, etype = "backend_timeout", "backend_error"
	case *BackendUnavailableError:
		code, etype = "backend_unavailable", "backend_error"
	case *BackendUnsupportedCapabilityError:
		code, etype = "backend_unsupported_capability", "backend_error"
	case *StreamInterruptedError:
		code, etype = "stream_interrupted", "backend_error"
	case *ErrUnsupportedOfficialBackend:
		code, etype = "unsupported_official_backend", "not_implemented_error"
	}
	return &ResponseError{Code: code, Message: err.Error(), Type: etype}
}

func writeProviderError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": errorToResponseError(err)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Serve starts the gateway and blocks until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, listen string) error {
	if s.cfg.Token == "" {
		return fmt.Errorf("provider gateway requires a bearer token - refusing to serve unauthenticated")
	}
	srv := &http.Server{
		Addr:              listen,
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
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
