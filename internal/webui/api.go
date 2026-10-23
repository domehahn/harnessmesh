package webui

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/identity"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
	"github.com/domehahn/harnessmesh/internal/tenant"
)

// APIHandler exposes read-only, tenant-scoped control-plane projections for
// the dashboard. Mutations remain on the audited MCP/CLI paths.
func APIHandler(st store.Store) http.Handler {
	const maxRateLimitClients = 10000
	var rateMu sync.Mutex
	windows := map[string]struct {
		started time.Time
		count   int
	}{}
	lastCleanup := time.Time{}
	limit := 120
	if configured, err := strconv.Atoi(os.Getenv("HARNESSMESH_DASHBOARD_RATE_LIMIT")); err == nil && configured > 0 {
		limit = configured
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		client := r.RemoteAddr
		if host, _, splitErr := net.SplitHostPort(client); splitErr == nil {
			client = host
		}
		now := time.Now()
		rateMu.Lock()
		if lastCleanup.IsZero() || now.Sub(lastCleanup) >= time.Minute {
			for address, entry := range windows {
				if now.Sub(entry.started) >= time.Minute {
					delete(windows, address)
				}
			}
			lastCleanup = now
		}
		if _, exists := windows[client]; !exists && len(windows) >= maxRateLimitClients {
			// Evict an arbitrary stale/old bucket before admitting a new client.
			// The window is intentionally approximate; the bound protects the
			// process from unbounded memory growth under spoofed source addresses.
			for address := range windows {
				delete(windows, address)
				break
			}
		}
		window := windows[client]
		if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
			window = struct {
				started time.Time
				count   int
			}{started: now}
		}
		window.count++
		windows[client] = window
		rateMu.Unlock()
		if window.count > limit {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "dashboard rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		identityStore, ok := st.(store.IdentityStore)
		if !ok {
			http.Error(w, "identity backend unavailable", http.StatusServiceUnavailable)
			return
		}
		key := strings.TrimSpace(r.Header.Get("X-HarnessMesh-API-Key"))
		who, err := (identity.Service{Store: identityStore}).AuthenticateAPIKey(r.Context(), key)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := tenant.WithIdentity(r.Context(), who)
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/dashboard")
		if r.Method == http.MethodPost && path == "/jobs" {
			var req struct {
				ID          string          `json:"id"`
				Kind        string          `json:"kind"`
				Payload     json.RawMessage `json:"payload"`
				MaxAttempts int             `json:"max_attempts"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) != nil || strings.TrimSpace(req.Kind) == "" {
				http.Error(w, "invalid job request", http.StatusBadRequest)
				return
			}
			if len(req.Payload) == 0 {
				req.Payload = json.RawMessage(`{}`)
			}
			if req.ID == "" {
				req.ID = "ui-" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
			}
			job := &store.DurableJob{ID: req.ID, Tenant: who.Tenant, Kind: req.Kind, Payload: req.Payload, MaxAttempts: req.MaxAttempts, CreatedAt: time.Now().UTC(), NextRunAt: time.Now().UTC()}
			if err := st.CreateJob(ctx, job); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			writeJSON(w, job)
			return
		}
		if r.Method == http.MethodPost && strings.HasPrefix(path, "/approvals/") {
			id := strings.TrimPrefix(path, "/approvals/")
			var req struct {
				Status protocol.ApprovalStatus `json:"status"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil {
				return
			}
			if req.Status != protocol.ApprovalApproved && req.Status != protocol.ApprovalRejected {
				http.Error(w, "status must be approved or rejected", http.StatusBadRequest)
				return
			}
			a, e := st.GetApproval(ctx, id)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if a == nil {
				http.NotFound(w, r)
				return
			}
			now := time.Now().UTC()
			a.Status = req.Status
			a.DecidedBy = who.Subject
			a.DecidedAt = &now
			if e = st.SaveApproval(ctx, a); e != nil {
				http.Error(w, e.Error(), http.StatusConflict)
				return
			}
			writeJSON(w, a)
			return
		}
		if r.Method == http.MethodPost && strings.HasPrefix(path, "/jobs/") && strings.HasSuffix(path, "/action") {
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/jobs/"), "/action")
			var req struct {
				Action string `json:"action"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil || (req.Action != "retry" && req.Action != "cancel") {
				http.Error(w, "action must be retry or cancel", http.StatusBadRequest)
				return
			}
			job, e := st.GetJob(ctx, id)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if job == nil || job.Tenant != who.Tenant {
				http.NotFound(w, r)
				return
			}
			status := "cancelled"
			lastError := job.LastError
			nextRun := job.NextRunAt
			if req.Action == "retry" {
				status, lastError, nextRun = "queued", "", time.Now().UTC()
			}
			if e = st.UpdateJob(ctx, id, status, nextRun, lastError); e != nil {
				http.Error(w, e.Error(), http.StatusConflict)
				return
			}
			job.Status, job.NextRunAt, job.LastError = status, nextRun, lastError
			writeJSON(w, job)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.HasPrefix(path, "/jobs/") {
			id := strings.TrimPrefix(path, "/jobs/")
			job, e := st.GetJob(ctx, id)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if job == nil || job.Tenant != who.Tenant {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, job)
			return
		}
		if strings.HasPrefix(path, "/sessions/") {
			id := strings.TrimPrefix(path, "/sessions/")
			session, e := st.GetSessionForTenant(ctx, id, who.Tenant)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if session == nil {
				http.NotFound(w, r)
				return
			}
			messages, e := st.GetMessages(ctx, id)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			findings, e := st.GetFindings(ctx, id)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, map[string]any{"session": session, "messages": messages, "findings": findings})
			return
		}
		switch path {
		case "", "/", "/summary":
			sessions, e := st.ListSessionsForTenant(ctx, who.Tenant)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			jobs, e := st.ListJobs(ctx, who.Tenant, "", 200)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			approvals, e := st.ListApprovals(ctx, protocol.ApprovalPending)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			health, e := st.ListAgentHealth(ctx)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, map[string]any{"tenant": who.Tenant, "subject": who.Subject, "sessions": sessions, "jobs": jobs, "pending_approvals": approvals, "agent_health": health})
		case "/sessions":
			v, e := st.ListSessionsForTenant(ctx, who.Tenant)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, v)
		case "/jobs":
			v, e := st.ListJobs(ctx, who.Tenant, r.URL.Query().Get("status"), 200)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, v)
		case "/approvals":
			status := protocol.ApprovalStatus(r.URL.Query().Get("status"))
			v, e := st.ListApprovals(ctx, status)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, v)
		case "/health":
			v, e := st.ListAgentHealth(ctx)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, v)
		case "/usage":
			observations, e := st.QueryPerformanceObservations(ctx, who.Tenant, "", "")
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			jobs, e := st.ListJobs(ctx, who.Tenant, "", 1000)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, map[string]any{"tenant": who.Tenant, "observation_count": len(observations), "job_count": len(jobs), "billing_mode": "zero_api_billing", "metered_api_calls": 0})
		default:
			http.NotFound(w, r)
		}
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
