package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/auth"
	"github.com/domehahn/harnessmesh/internal/governance"
	"github.com/domehahn/harnessmesh/internal/identity"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/runbundle"
	"github.com/domehahn/harnessmesh/internal/secrets"
	"github.com/domehahn/harnessmesh/internal/simulation"
	"github.com/domehahn/harnessmesh/internal/store"
	"github.com/domehahn/harnessmesh/internal/tenant"
)

// APIHandler exposes read-only, tenant-scoped control-plane projections for
// the dashboard. Mutations remain on the audited MCP/CLI paths.
type Orchestrator interface {
	ListSpaces(context.Context) ([]*protocol.CollaborationSpace, error)
	GetSpace(context.Context, string) (*protocol.CollaborationSpace, error)
	CreateSpace(context.Context, string, string, string, string, string, map[string]protocol.SpaceParticipant) (*protocol.CollaborationSpace, error)
	PauseSpace(context.Context, string) error
	ResumeSpace(context.Context, string) error
	StopSpace(context.Context, string) error
	AddParticipant(context.Context, string, *protocol.SpaceParticipant) error
	UpdateParticipantMode(context.Context, string, string, protocol.ParticipantActivityMode) error
	GetMessages(context.Context, string, string, string, int) ([]*protocol.PeerEnvelope, error)
	ConfiguredAgents() []map[string]any
}

type Messenger interface {
	Publish(context.Context, *protocol.PublishRequest) (*protocol.PublishResponse, error)
}

type MarketplaceCatalog interface {
	ConfiguredMCPServers() []map[string]any
}

func orchestratorAgents(orchestrator Orchestrator) []map[string]any {
	if orchestrator == nil {
		return []map[string]any{}
	}
	return orchestrator.ConfiguredAgents()
}

func dashboardPage(r *http.Request) (int, int) {
	page, pageSize := 1, 50
	if value, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && value > 0 {
		page = value
	}
	if value, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && value > 0 {
		pageSize = value
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

func APIHandler(st store.Store) http.Handler { return APIHandlerWithOrchestrator(st, nil) }

func APIHandlerWithOrchestrator(st store.Store, orchestrator Orchestrator, integrations ...any) http.Handler {
	var messenger Messenger
	if len(integrations) > 0 {
		messenger, _ = integrations[0].(Messenger)
	}
	var secretManager secrets.Manager
	var marketplace MarketplaceCatalog
	for _, integration := range integrations {
		if configured, ok := integration.(secrets.Manager); ok {
			secretManager = configured
		}
		if configured, ok := integration.(MarketplaceCatalog); ok {
			marketplace = configured
		}
	}
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
		var who tenant.Identity
		var err error
		if key != "" {
			who, err = (identity.Service{Store: identityStore}).AuthenticateAPIKey(r.Context(), key)
		} else {
			bearer := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			info, ok := auth.IntrospectToken(r.Context(), os.Getenv("HARNESSMESH_DASHBOARD_OAUTH_INTROSPECTION_URL"), bearer, os.Getenv("HARNESSMESH_DASHBOARD_OAUTH_CLIENT_SECRET"))
			if !ok || strings.TrimSpace(info.Tenant) == "" || strings.TrimSpace(info.Subject) == "" {
				err = tenant.ErrUnauthenticated
			} else {
				who = tenant.Identity{Subject: info.Subject, Tenant: info.Tenant, Roles: info.Roles}
			}
		}
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := tenant.WithIdentity(r.Context(), who)
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/dashboard")
		if strings.HasPrefix(path, "/spaces") {
			if orchestrator == nil {
				http.Error(w, "orchestrator unavailable", http.StatusServiceUnavailable)
				return
			}
			spacePath := strings.Trim(strings.TrimPrefix(path, "/spaces"), "/")
			parts := strings.Split(spacePath, "/")
			belongs := func(space *protocol.CollaborationSpace) bool {
				if space == nil || space.Metadata == nil {
					return false
				}
				value, _ := space.Metadata["tenant"].(string)
				return value == who.Tenant
			}
			if spacePath == "" {
				if r.Method == http.MethodGet {
					spaces, e := orchestrator.ListSpaces(ctx)
					if e != nil {
						http.Error(w, e.Error(), http.StatusServiceUnavailable)
						return
					}
					filtered := make([]*protocol.CollaborationSpace, 0, len(spaces))
					for _, space := range spaces {
						if belongs(space) {
							filtered = append(filtered, space)
						}
					}
					writeJSON(w, map[string]any{"tenant": who.Tenant, "spaces": filtered})
					return
				}
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if e := tenant.Authorize(ctx, who.Tenant, "owner", ""); e != nil {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				var req struct {
					ID           string                               `json:"id"`
					WorkspaceID  string                               `json:"workspace_id"`
					Title        string                               `json:"title"`
					Purpose      string                               `json:"purpose"`
					Writer       string                               `json:"writer_participant"`
					Participants map[string]protocol.SpaceParticipant `json:"participants"`
				}
				if json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&req) != nil || strings.TrimSpace(req.Title) == "" {
					http.Error(w, "invalid space request", http.StatusBadRequest)
					return
				}
				space, e := orchestrator.CreateSpace(ctx, req.ID, req.WorkspaceID, req.Title, req.Purpose, req.Writer, req.Participants)
				if e != nil {
					http.Error(w, e.Error(), http.StatusBadRequest)
					return
				}
				writeJSON(w, space)
				return
			}
			id := parts[0]
			space, e := orchestrator.GetSpace(ctx, id)
			if e != nil || !belongs(space) {
				http.Error(w, "space not found", http.StatusNotFound)
				return
			}
			if len(parts) == 1 && r.Method == http.MethodGet {
				writeJSON(w, space)
				return
			}
			if len(parts) >= 3 && parts[1] == "messages" && parts[2] == "stream" && r.Method == http.MethodGet {
				flusher, ok := w.(http.Flusher)
				if !ok {
					http.Error(w, "streaming unsupported", http.StatusInternalServerError)
					return
				}
				channelID := r.URL.Query().Get("channel")
				if channelID == "" {
					channelID = "general"
				}
				threadID := r.URL.Query().Get("thread")
				lastCount := 0
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				for {
					messages, messageErr := orchestrator.GetMessages(ctx, id, channelID, threadID, 500)
					if messageErr != nil {
						return
					}
					if len(messages) < lastCount {
						lastCount = 0
					}
					for _, message := range messages[lastCount:] {
						payload, marshalErr := json.Marshal(message)
						if marshalErr == nil {
							_, _ = fmt.Fprintf(w, "event: collaboration\\ndata: %s\\n\\n", payload)
						}
					}
					if len(messages) > lastCount {
						lastCount = len(messages)
					}
					_, _ = fmt.Fprint(w, ": keepalive\\n\\n")
					flusher.Flush()
					select {
					case <-r.Context().Done():
						return
					case <-ticker.C:
					}
				}
			}
			if len(parts) >= 2 && parts[1] == "messages" && r.Method == http.MethodGet {
				channelID := r.URL.Query().Get("channel")
				if channelID == "" {
					channelID = "general"
				}
				threadID := r.URL.Query().Get("thread")
				limit := 100
				if raw := r.URL.Query().Get("limit"); raw != "" {
					if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 && parsed <= 500 {
						limit = parsed
					}
				}
				messages, e := orchestrator.GetMessages(ctx, id, channelID, threadID, limit)
				if e != nil {
					http.Error(w, e.Error(), http.StatusServiceUnavailable)
					return
				}
				if messages == nil {
					messages = []*protocol.PeerEnvelope{}
				}
				writeJSON(w, map[string]any{"space_id": id, "channel": channelID, "messages": messages})
				return
			}
			if len(parts) >= 2 && parts[1] == "messages" && r.Method == http.MethodPost {
				if messenger == nil {
					http.Error(w, "messenger unavailable", http.StatusServiceUnavailable)
					return
				}
				var req struct {
					Channel  string   `json:"channel"`
					From     string   `json:"from"`
					To       string   `json:"to"`
					Message  string   `json:"message"`
					Mentions []string `json:"mentions"`
				}
				if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req) != nil || strings.TrimSpace(req.Message) == "" {
					http.Error(w, "invalid message request", http.StatusBadRequest)
					return
				}
				if req.Channel == "" {
					req.Channel = "general"
				}
				if req.From == "" {
					req.From = who.Subject
				}
				response, e := messenger.Publish(ctx, &protocol.PublishRequest{SpaceID: id, Channel: req.Channel, From: req.From, To: req.To, Message: req.Message, Mentions: req.Mentions})
				if e != nil {
					http.Error(w, e.Error(), http.StatusBadRequest)
					return
				}
				writeJSON(w, response)
				return
			}
			if len(parts) >= 2 && parts[1] == "lifecycle" {
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if e := tenant.Authorize(ctx, who.Tenant, "owner", ""); e != nil {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				var req struct {
					Action string `json:"action"`
				}
				if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil {
					http.Error(w, "invalid lifecycle request", http.StatusBadRequest)
					return
				}
				var actionErr error
				switch req.Action {
				case "pause":
					actionErr = orchestrator.PauseSpace(ctx, id)
				case "resume":
					actionErr = orchestrator.ResumeSpace(ctx, id)
				case "stop":
					actionErr = orchestrator.StopSpace(ctx, id)
				default:
					http.Error(w, "unknown lifecycle action", http.StatusBadRequest)
					return
				}
				if actionErr != nil {
					http.Error(w, actionErr.Error(), http.StatusBadRequest)
					return
				}
				writeJSON(w, map[string]any{"ok": true, "action": req.Action})
				return
			}
			if len(parts) >= 2 && parts[1] == "participants" {
				if len(parts) >= 4 && parts[3] == "mode" && r.Method == http.MethodPost {
					if e := tenant.Authorize(ctx, who.Tenant, "owner", ""); e != nil {
						http.Error(w, "forbidden", http.StatusForbidden)
						return
					}
					var req struct {
						Mode protocol.ParticipantActivityMode `json:"mode"`
					}
					if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil {
						http.Error(w, "invalid participant mode", http.StatusBadRequest)
						return
					}
					switch req.Mode {
					case protocol.ParticipantModeActive, protocol.ParticipantModePassive, protocol.ParticipantModeOnDemand, protocol.ParticipantModePaused:
					default:
						http.Error(w, "invalid participant mode", http.StatusBadRequest)
						return
					}
					if e := orchestrator.UpdateParticipantMode(ctx, id, parts[2], req.Mode); e != nil {
						http.Error(w, e.Error(), http.StatusBadRequest)
						return
					}
					writeJSON(w, map[string]any{"ok": true, "participant": parts[2], "mode": req.Mode})
					return
				}
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if e := tenant.Authorize(ctx, who.Tenant, "owner", ""); e != nil {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				var req struct {
					ID       string                           `json:"id"`
					Adapter  string                           `json:"adapter"`
					Mode     protocol.ParticipantActivityMode `json:"mode"`
					Writable bool                             `json:"writable"`
				}
				if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil || strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.Adapter) == "" {
					http.Error(w, "invalid participant request", http.StatusBadRequest)
					return
				}
				if req.Mode == "" {
					req.Mode = protocol.ParticipantModeOnDemand
				}
				if e := orchestrator.AddParticipant(ctx, id, &protocol.SpaceParticipant{ID: req.ID, Adapter: req.Adapter, Mode: req.Mode, ExecutionMode: protocol.ExecutionModeExternal, Writable: req.Writable, Roles: []string{}, Capabilities: []string{}, JoinedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); e != nil {
					http.Error(w, e.Error(), http.StatusBadRequest)
					return
				}
				writeJSON(w, map[string]any{"ok": true, "participant": req.ID})
				return
			}
			http.NotFound(w, r)
			return
		}
		if path == "/agents" && r.Method == http.MethodGet {
			if orchestrator == nil {
				http.Error(w, "orchestrator unavailable", http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, map[string]any{"tenant": who.Tenant, "agents": orchestrator.ConfiguredAgents()})
			return
		}
		if path == "/marketplace" && r.Method == http.MethodGet {
			items := []map[string]any{}
			if marketplace != nil {
				items = marketplace.ConfiguredMCPServers()
			}
			writeJSON(w, map[string]any{"tenant": who.Tenant, "agents": orchestratorAgents(orchestrator), "mcp_servers": items})
			return
		}
		if strings.HasPrefix(path, "/secrets") {
			if secretManager == nil {
				http.Error(w, "secret manager unavailable", http.StatusServiceUnavailable)
				return
			}
			if e := tenant.Authorize(ctx, who.Tenant, "owner", ""); e != nil {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if path == "/secrets" && r.Method == http.MethodGet {
				items, e := secretManager.List(ctx, who.Tenant)
				if e != nil {
					http.Error(w, e.Error(), http.StatusServiceUnavailable)
					return
				}
				writeJSON(w, map[string]any{"tenant": who.Tenant, "secrets": items})
				return
			}
			if path == "/secrets" && r.Method == http.MethodPost {
				var req struct {
					Name     string            `json:"name"`
					Value    string            `json:"value"`
					Metadata map[string]string `json:"metadata"`
				}
				if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req) != nil || strings.TrimSpace(req.Name) == "" || req.Value == "" {
					http.Error(w, "name and value are required", http.StatusBadRequest)
					return
				}
				if len(req.Name) > 128 || strings.ContainsAny(req.Name, "/\\") {
					http.Error(w, "invalid secret name", http.StatusBadRequest)
					return
				}
				item, e := secretManager.Put(ctx, who.Tenant, strings.TrimSpace(req.Name), req.Value, req.Metadata)
				if e != nil {
					http.Error(w, e.Error(), http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				writeJSON(w, map[string]any{"secret": item, "stored": true})
				return
			}
			if strings.HasPrefix(path, "/secrets/") && r.Method == http.MethodDelete {
				name := strings.TrimPrefix(path, "/secrets/")
				if name == "" || strings.ContainsAny(name, "/\\") {
					http.Error(w, "invalid secret name", http.StatusBadRequest)
					return
				}
				if e := secretManager.Delete(ctx, who.Tenant, name); e != nil {
					http.Error(w, e.Error(), http.StatusNotFound)
					return
				}
				writeJSON(w, map[string]any{"name": name, "deleted": true})
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if path == "/members" {
			directory, ok := st.(store.DirectoryStore)
			if !ok {
				http.Error(w, "directory backend unavailable", http.StatusServiceUnavailable)
				return
			}
			if r.Method == http.MethodGet {
				if e := tenant.Authorize(ctx, who.Tenant, "owner", ""); e != nil {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				members, e := directory.ListMemberships(ctx, who.Tenant)
				if e != nil {
					http.Error(w, e.Error(), http.StatusServiceUnavailable)
					return
				}
				writeJSON(w, map[string]any{"tenant": who.Tenant, "members": members})
				return
			}
			if r.Method == http.MethodPost {
				if e := tenant.Authorize(ctx, who.Tenant, "owner", ""); e != nil {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				var req struct {
					Subject string `json:"subject"`
					Role    string `json:"role"`
				}
				if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil || strings.TrimSpace(req.Subject) == "" {
					http.Error(w, "invalid member request", http.StatusBadRequest)
					return
				}
				if req.Role == "" {
					req.Role = "member"
				}
				if e := identityStore.UpsertPrincipal(ctx, &store.PrincipalRecord{Subject: req.Subject}); e != nil {
					http.Error(w, e.Error(), http.StatusConflict)
					return
				}
				member := &store.MembershipRecord{TenantID: who.Tenant, Subject: req.Subject, Role: req.Role}
				if e := identityStore.SetMembership(ctx, member); e != nil {
					http.Error(w, e.Error(), http.StatusConflict)
					return
				}
				writeJSON(w, member)
				return
			}
		}
		if path == "/keys" {
			directory, ok := st.(store.DirectoryStore)
			if !ok {
				http.Error(w, "directory backend unavailable", http.StatusServiceUnavailable)
				return
			}
			if e := tenant.Authorize(ctx, who.Tenant, "owner", ""); e != nil {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if r.Method == http.MethodGet {
				keys, e := directory.ListAPIKeys(ctx, who.Tenant)
				if e != nil {
					http.Error(w, e.Error(), http.StatusServiceUnavailable)
					return
				}
				writeJSON(w, map[string]any{"tenant": who.Tenant, "keys": keys})
				return
			}
			if r.Method == http.MethodPost {
				var req struct {
					Subject string `json:"subject"`
				}
				if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil || strings.TrimSpace(req.Subject) == "" {
					http.Error(w, "invalid key request", http.StatusBadRequest)
					return
				}
				secret, record, e := (identity.Service{Store: identityStore}).IssueAPIKey(ctx, who.Tenant, req.Subject)
				if e != nil {
					http.Error(w, e.Error(), http.StatusBadRequest)
					return
				}
				writeJSON(w, map[string]any{"api_key": secret, "record": record})
				return
			}
		}
		if strings.HasPrefix(path, "/keys/") && strings.HasSuffix(path, "/revoke") && r.Method == http.MethodPost {
			if e := tenant.Authorize(ctx, who.Tenant, "owner", ""); e != nil {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/keys/"), "/revoke")
			if e := identityStore.RevokeAPIKey(ctx, id); e != nil {
				http.Error(w, e.Error(), http.StatusConflict)
				return
			}
			writeJSON(w, map[string]any{"id": id, "status": "revoked"})
			return
		}
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
			if events, ok := st.(store.JobEventStore); ok {
				_ = events.AppendJobEvent(ctx, &store.JobEvent{JobID: job.ID, Tenant: job.Tenant, Type: "submitted", Message: "job accepted through dashboard"})
			}
			writeJSON(w, job)
			return
		}
		if strings.HasPrefix(path, "/approvals/") && r.Method == http.MethodGet {
			id := strings.TrimPrefix(path, "/approvals/")
			a, e := st.GetApproval(ctx, id)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if a == nil {
				http.NotFound(w, r)
				return
			}
			audit := []store.ApprovalAudit{}
			if auditStore, ok := st.(store.ApprovalAuditStore); ok {
				audit, e = auditStore.ListApprovalAudit(ctx, id, 200)
				if e != nil {
					http.Error(w, e.Error(), http.StatusServiceUnavailable)
					return
				}
			}
			writeJSON(w, map[string]any{"approval": a, "audit": audit})
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
			if events, ok := st.(store.JobEventStore); ok {
				_ = events.AppendJobEvent(ctx, &store.JobEvent{JobID: job.ID, Tenant: job.Tenant, Type: req.Action, Message: "job action requested through dashboard"})
			}
			writeJSON(w, job)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.HasPrefix(path, "/jobs/") && strings.HasSuffix(path, "/events/stream") {
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/jobs/"), "/events/stream")
			job, e := st.GetJob(ctx, id)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if job == nil || job.Tenant != who.Tenant {
				http.NotFound(w, r)
				return
			}
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "streaming unsupported", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			lastCount := 0
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				events := []store.JobEvent{}
				if eventStore, ok := st.(store.JobEventStore); ok {
					events, e = eventStore.ListJobEvents(ctx, id, 500)
					if e != nil {
						return
					}
				}
				if len(events) < lastCount {
					lastCount = 0
				}
				for _, event := range events[lastCount:] {
					payload, marshalErr := json.Marshal(event)
					if marshalErr == nil {
						_, _ = fmt.Fprintf(w, "event: job\\ndata: %s\\n\\n", payload)
					}
				}
				if len(events) > lastCount {
					lastCount = len(events)
				}
				_, _ = fmt.Fprint(w, ": keepalive\\n\\n")
				flusher.Flush()
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
				}
			}
		}
		if strings.HasPrefix(path, "/jobs/") && strings.HasSuffix(path, "/events") {
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/jobs/"), "/events")
			job, e := st.GetJob(ctx, id)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if job == nil || job.Tenant != who.Tenant {
				http.NotFound(w, r)
				return
			}
			events := []store.JobEvent{}
			if eventStore, ok := st.(store.JobEventStore); ok {
				events, e = eventStore.ListJobEvents(ctx, id, 200)
				if e != nil {
					http.Error(w, e.Error(), http.StatusServiceUnavailable)
					return
				}
			}
			writeJSON(w, map[string]any{"job_id": id, "events": events})
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
			response := map[string]any{}
			encoded, _ := json.Marshal(job)
			_ = json.Unmarshal(encoded, &response)
			response["events"] = []store.JobEvent{}
			if eventStore, ok := st.(store.JobEventStore); ok {
				events, eventErr := eventStore.ListJobEvents(ctx, id, 200)
				if eventErr != nil {
					http.Error(w, eventErr.Error(), http.StatusServiceUnavailable)
					return
				}
				response["events"] = events
			}
			writeJSON(w, response)
			return
		}
		if strings.HasPrefix(path, "/sessions/") {
			sessionPath := strings.TrimPrefix(path, "/sessions/")
			stream := strings.HasSuffix(sessionPath, "/events/stream")
			if stream {
				sessionPath = strings.TrimSuffix(sessionPath, "/events/stream")
			}
			export := strings.HasSuffix(sessionPath, "/bundle")
			id := strings.TrimSuffix(sessionPath, "/bundle")
			session, e := st.GetSessionForTenant(ctx, id, who.Tenant)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if session == nil {
				http.NotFound(w, r)
				return
			}
			if stream {
				flusher, ok := w.(http.Flusher)
				if !ok {
					http.Error(w, "streaming unsupported", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				lastCount := 0
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					events, eventErr := st.GetEvents(r.Context(), id)
					if eventErr != nil {
						return
					}
					if len(events) < lastCount {
						lastCount = 0
					}
					for _, event := range events[lastCount:] {
						payload, marshalErr := json.Marshal(event)
						if marshalErr != nil {
							continue
						}
						_, _ = fmt.Fprintf(w, "event: collaboration\\ndata: %s\\n\\n", payload)
					}
					if len(events) > lastCount {
						lastCount = len(events)
						flusher.Flush()
					} else {
						_, _ = fmt.Fprint(w, ": keepalive\\n\\n")
						flusher.Flush()
					}
					select {
					case <-r.Context().Done():
						return
					case <-ticker.C:
					}
				}
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
			events, e := st.GetEvents(ctx, id)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if export && r.Method == http.MethodGet {
				files := map[string][]byte{}
				for name, value := range map[string]any{"session.json": session, "messages.json": messages, "findings.json": findings, "timeline.json": events} {
					encoded, marshalErr := json.Marshal(value)
					if marshalErr != nil {
						http.Error(w, marshalErr.Error(), http.StatusInternalServerError)
						return
					}
					files[name] = runbundle.Redact(encoded)
				}
				bundle, _, buildErr := runbundle.Build("session-"+id, who.Tenant, id, files, time.Now().UTC())
				if buildErr != nil {
					http.Error(w, buildErr.Error(), http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/zip")
				w.Header().Set("Content-Disposition", `attachment; filename="`+id+`.harnessmesh-run.zip"`)
				_, _ = w.Write(bundle)
				return
			}
			writeJSON(w, map[string]any{"session": session, "messages": messages, "findings": findings, "timeline": events})
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
			// Keep collection fields iterable for empty tenants; clients should not
			// need a special case for JSON null.
			if sessions == nil {
				sessions = []*store.Session{}
			}
			if jobs == nil {
				jobs = []store.DurableJob{}
			}
			if approvals == nil {
				approvals = []protocol.ApprovalRequest{}
			}
			if health == nil {
				health = []protocol.AgentHealth{}
			}
			writeJSON(w, map[string]any{"tenant": who.Tenant, "subject": who.Subject, "sessions": sessions, "jobs": jobs, "pending_approvals": approvals, "agent_health": health})
		case "/sessions":
			v, e := st.ListSessionsForTenant(ctx, who.Tenant)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if r.URL.Query().Get("page") == "" && r.URL.Query().Get("page_size") == "" {
				writeJSON(w, v)
				return
			}
			page, pageSize := dashboardPage(r)
			start := (page - 1) * pageSize
			if start > len(v) {
				start = len(v)
			}
			end := start + pageSize
			if end > len(v) {
				end = len(v)
			}
			writeJSON(w, map[string]any{"items": v[start:end], "page": page, "page_size": pageSize, "total": len(v), "has_more": end < len(v)})
		case "/jobs":
			v, e := st.ListJobs(ctx, who.Tenant, r.URL.Query().Get("status"), 200)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if r.URL.Query().Get("page") == "" && r.URL.Query().Get("page_size") == "" {
				writeJSON(w, v)
				return
			}
			page, pageSize := dashboardPage(r)
			start := (page - 1) * pageSize
			if start > len(v) {
				start = len(v)
			}
			end := start + pageSize
			if end > len(v) {
				end = len(v)
			}
			writeJSON(w, map[string]any{"items": v[start:end], "page": page, "page_size": pageSize, "total": len(v), "has_more": end < len(v)})
		case "/approvals":
			status := protocol.ApprovalStatus(r.URL.Query().Get("status"))
			v, e := st.ListApprovals(ctx, status)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if r.URL.Query().Get("page") == "" && r.URL.Query().Get("page_size") == "" {
				writeJSON(w, v)
				return
			}
			page, pageSize := dashboardPage(r)
			start := (page - 1) * pageSize
			if start > len(v) {
				start = len(v)
			}
			end := start + pageSize
			if end > len(v) {
				end = len(v)
			}
			writeJSON(w, map[string]any{"items": v[start:end], "page": page, "page_size": pageSize, "total": len(v), "has_more": end < len(v)})
		case "/health":
			v, e := st.ListAgentHealth(ctx)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, v)
		case "/incident":
			failed, e := st.ListJobs(ctx, who.Tenant, "failed", 200)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			dead, e := st.ListJobs(ctx, who.Tenant, "dead", 200)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			health, e := st.ListAgentHealth(ctx)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			unhealthy := make([]protocol.AgentHealth, 0)
			for _, agent := range health {
				if agent.Status != protocol.AgentHealthHealthy {
					unhealthy = append(unhealthy, agent)
				}
			}
			writeJSON(w, map[string]any{"tenant": who.Tenant, "failed_jobs": failed, "dead_jobs": dead, "unhealthy_agents": unhealthy, "requires_attention": len(failed)+len(dead)+len(unhealthy) > 0})
		case "/simulation":
			if r.Method == http.MethodGet {
				writeJSON(w, map[string]any{"tenant": who.Tenant, "scenarios": simulation.Catalog()})
				return
			}
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			var request struct {
				ScenarioID  string                 `json:"scenario_id"`
				Observation simulation.Observation `json:"observation"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&request) != nil {
				http.Error(w, "invalid simulation request", http.StatusBadRequest)
				return
			}
			var selected *simulation.Scenario
			for _, scenario := range simulation.Catalog() {
				if scenario.ID == request.ScenarioID {
					copy := scenario
					selected = &copy
					break
				}
			}
			if selected == nil {
				http.Error(w, "unknown simulation scenario", http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]any{"tenant": who.Tenant, "scenario": selected, "result": simulation.Evaluate(*selected, request.Observation)})
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
		case "/optimization":
			sessions, e := st.ListSessionsForTenant(ctx, who.Tenant)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			type usageBucket struct {
				Key          string  `json:"key"`
				InputTokens  int64   `json:"input_tokens"`
				OutputTokens int64   `json:"output_tokens"`
				CostUSD      float64 `json:"cost_usd"`
				DurationMS   int64   `json:"duration_ms"`
				Requests     int     `json:"requests"`
			}
			type qualityBucket struct {
				Agent       string  `json:"agent"`
				Successful  int64   `json:"successful"`
				Failed      int64   `json:"failed"`
				SuccessRate float64 `json:"success_rate"`
				SampleCount int64   `json:"sample_count"`
				TrustScore  float64 `json:"trust_score"`
			}
			byAgent := map[string]*usageBucket{}
			byModel := map[string]*usageBucket{}
			bySession := map[string]*usageBucket{}
			add := func(target map[string]*usageBucket, key string, item store.UsageRecord) {
				if key == "" {
					key = "unknown"
				}
				bucket := target[key]
				if bucket == nil {
					bucket = &usageBucket{Key: key}
					target[key] = bucket
				}
				bucket.InputTokens += item.InputTokens
				bucket.OutputTokens += item.OutputTokens
				bucket.CostUSD += item.MonetaryCostUSD
				bucket.DurationMS += item.DurationMS
				bucket.Requests++
			}
			for _, session := range sessions {
				usage, usageErr := st.GetUsage(ctx, session.ID)
				if usageErr != nil {
					http.Error(w, usageErr.Error(), http.StatusServiceUnavailable)
					return
				}
				for _, item := range usage {
					add(byAgent, item.Participant, item)
					add(byModel, item.Model, item)
					add(bySession, item.SessionID, item)
				}
			}
			toList := func(values map[string]*usageBucket) []usageBucket {
				out := make([]usageBucket, 0, len(values))
				for _, value := range values {
					out = append(out, *value)
				}
				return out
			}
			health, e := st.ListAgentHealth(ctx)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			quality := make([]qualityBucket, 0, len(health))
			for _, item := range health {
				samples := item.Successful + item.Failed
				rate := 0.0
				if samples > 0 {
					rate = float64(item.Successful) / float64(samples)
				}
				quality = append(quality, qualityBucket{Agent: item.Agent, Successful: item.Successful, Failed: item.Failed, SuccessRate: rate, SampleCount: samples, TrustScore: governance.TrustScore(governance.TrustSignals{SuccessRate: rate, SampleCount: int(samples)})})
			}
			connections := make([]map[string]any, 0, len(health))
			routingCandidates := make([]governance.RoutingCandidate, 0, len(health))
			mcp := probeTransport(ctx, "HARNESSMESH_MCP_HEALTH_URL", "HARNESSMESH_MCP_TOKEN")
			bridge := probeTransport(ctx, "HARNESSMESH_BRIDGE_HEALTH_URL", "HARNESSMESH_BRIDGE_TOKEN")
			for _, item := range health {
				costPerSuccess := 0.0
				if bucket := byAgent[item.Agent]; bucket != nil && item.Successful > 0 {
					costPerSuccess = bucket.CostUSD / float64(item.Successful)
				}
				routingCandidates = append(routingCandidates, governance.RoutingCandidate{Agent: item.Agent, TrustScore: governance.TrustScore(governance.TrustSignals{SuccessRate: func() float64 {
					samples := item.Successful + item.Failed
					if samples == 0 {
						return 0
					}
					return float64(item.Successful) / float64(samples)
				}(), SampleCount: int(item.Successful + item.Failed)}), CostPerSuccess: costPerSuccess, LatencyMS: func() int64 {
					if bucket := byAgent[item.Agent]; bucket != nil {
						if bucket.Requests > 0 {
							return bucket.DurationMS / int64(bucket.Requests)
						}
					}
					return 0
				}(), Available: item.Status == protocol.AgentHealthHealthy})
				connections = append(connections, map[string]any{
					"agent": item.Agent, "adapter": item.Adapter, "status": item.Status,
					"invocations": item.TotalInvocations, "successful": item.Successful,
					"failed": item.Failed, "quota_reset_at": item.QuotaResetAt,
					"mcp": mcp["status"], "bridge": bridge["status"],
				})
			}
			credits := providerQuota(ctx)
			writeJSON(w, map[string]any{
				"tenant": who.Tenant, "connections": connections,
				"by_agent": toList(byAgent), "by_model": toList(byModel), "by_session": toList(bySession),
				"quality":      quality,
				"routing":      governance.Recommend(routingCandidates),
				"transports":   map[string]any{"mcp": mcp, "bridge": bridge},
				"credits":      credits,
				"optimization": map[string]any{"status": "observational", "recommendation": "Collect more completed usage and quality outcomes before changing routing", "automatic_changes": false},
			})
		default:
			http.NotFound(w, r)
		}
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
