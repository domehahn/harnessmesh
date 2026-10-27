package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/auth"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/governance"
	"github.com/domehahn/harnessmesh/internal/identity"
	"github.com/domehahn/harnessmesh/internal/incident"
	"github.com/domehahn/harnessmesh/internal/observability"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/replay"
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
	ConfiguredPlugins() []map[string]any
}

type AssuranceCatalog interface {
	AssuranceGraph() map[string]any
}

type SkillCatalog interface {
	SkillSnapshot() map[string]any
}

// MarketplaceManager is the audited lifecycle boundary for marketplace
// actions. The dashboard never installs files directly and never accepts an
// unauthenticated tenant identifier from the request body.
type MarketplaceManager interface {
	Snapshot(context.Context, string, string) (map[string]any, error)
	Mutate(context.Context, string, string, string, map[string]any) (map[string]any, error)
}

type MarketplaceRegistry interface {
	RegistrySnapshot(context.Context) (map[string]any, error)
}

type MarketplaceArtifactReader interface {
	GetArtifact(context.Context, string) (io.ReadCloser, error)
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

type observabilityLogRecord struct {
	ID        string         `json:"id"`
	Source    string         `json:"source"`
	Level     string         `json:"level"`
	Message   string         `json:"message,omitempty"`
	EventType string         `json:"event_type"`
	Timestamp time.Time      `json:"timestamp"`
	TraceID   string         `json:"trace_id"`
	SpanID    string         `json:"span_id"`
	Payload   map[string]any `json:"payload,omitempty"`
}

type observabilitySpanRecord struct {
	TraceID   string    `json:"trace_id"`
	SpanID    string    `json:"span_id"`
	ParentID  string    `json:"parent_span_id,omitempty"`
	Service   string    `json:"service"`
	Operation string    `json:"operation"`
	Start     time.Time `json:"start"`
	Duration  int64     `json:"duration_ms"`
	Status    string    `json:"status"`
}

func observabilityString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func observabilityNumber(payload map[string]any, key string) int64 {
	value, ok := payload[key].(float64)
	if !ok || value < 0 {
		return 0
	}
	return int64(value)
}

func observabilityProjection(ctx context.Context, st store.Store, tenantID string) ([]observabilityLogRecord, []observabilitySpanRecord, error) {
	logs := make([]observabilityLogRecord, 0)
	spans := make([]observabilitySpanRecord, 0)
	jobs, err := st.ListJobs(ctx, tenantID, "", 200)
	if err != nil {
		return nil, nil, err
	}
	if eventStore, ok := st.(store.JobEventStore); ok {
		for _, job := range jobs {
			events, eventErr := eventStore.ListJobEvents(ctx, job.ID, 500)
			if eventErr != nil {
				return nil, nil, eventErr
			}
			for _, event := range events {
				payload := map[string]any{}
				_ = json.Unmarshal(event.Payload, &payload)
				traceID := observabilityString(payload, "trace_id")
				if traceID == "" {
					traceID = "trace_" + job.ID
				}
				spanID := observabilityString(payload, "span_id")
				if spanID == "" {
					spanID = "span_" + event.ID
				}
				level := "info"
				if strings.Contains(event.Type, "fail") || strings.Contains(event.Type, "error") {
					level = "error"
				}
				logs = append(logs, observabilityLogRecord{ID: event.ID, Source: "worker", Level: level, Message: contextpack.RedactSecrets(event.Message), EventType: event.Type, Timestamp: event.CreatedAt, TraceID: traceID, SpanID: spanID})
				spans = append(spans, observabilitySpanRecord{TraceID: traceID, SpanID: spanID, ParentID: observabilityString(payload, "parent_span_id"), Service: "worker", Operation: event.Type, Start: event.CreatedAt, Duration: observabilityNumber(payload, "duration_ms"), Status: level})
			}
		}
	}
	sessions, err := st.ListSessionsForTenant(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	for _, session := range sessions {
		if session == nil {
			continue
		}
		events, eventErr := st.GetEvents(ctx, session.ID)
		if eventErr != nil {
			return nil, nil, eventErr
		}
		for _, event := range events {
			if !strings.HasPrefix(event.EventType, "security.") {
				continue
			}
			traceID := "trace_" + session.ID
			spanID := fmt.Sprintf("span_session_%d", event.ID)
			reason := observabilityString(event.Payload, "reason")
			level := "info"
			if event.EventType == "security.blocked" {
				level = "warn"
			}
			logs = append(logs, observabilityLogRecord{ID: fmt.Sprintf("session_%d", event.ID), Source: "security-gate", Level: level, Message: contextpack.RedactSecrets(reason), EventType: event.EventType, Timestamp: event.Timestamp, TraceID: traceID, SpanID: spanID, Payload: map[string]any{"risk_score": event.Payload["risk_score"], "allowed": event.Payload["allowed"]}})
			spans = append(spans, observabilitySpanRecord{TraceID: traceID, SpanID: spanID, Service: "security-gate", Operation: event.EventType, Start: event.Timestamp, Status: level})
		}
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].Timestamp.After(logs[j].Timestamp) })
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start.Before(spans[j].Start) })
	if len(logs) > 500 {
		logs = logs[:500]
	}
	if len(spans) > 500 {
		spans = spans[:500]
	}
	return logs, spans, nil
}

func APIHandler(st store.Store) http.Handler { return APIHandlerWithOrchestrator(st, nil) }

func APIHandlerWithOrchestrator(st store.Store, orchestrator Orchestrator, integrations ...any) http.Handler {
	var messenger Messenger
	if len(integrations) > 0 {
		messenger, _ = integrations[0].(Messenger)
	}
	var secretManager secrets.Manager
	var marketplace MarketplaceCatalog
	var marketplaceManager MarketplaceManager
	var assuranceCatalog AssuranceCatalog
	var skillCatalog SkillCatalog
	for _, integration := range integrations {
		if configured, ok := integration.(secrets.Manager); ok {
			secretManager = configured
		}
		if configured, ok := integration.(MarketplaceCatalog); ok {
			marketplace = configured
		}
		if configured, ok := integration.(MarketplaceManager); ok {
			marketplaceManager = configured
		}
		if configured, ok := integration.(AssuranceCatalog); ok {
			assuranceCatalog = configured
		}
		if configured, ok := integration.(SkillCatalog); ok {
			skillCatalog = configured
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
		apiKeyAuth := key != ""
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
		if !apiKeyAuth {
			requestedTenant := strings.TrimSpace(r.Header.Get("X-HarnessMesh-Tenant"))
			if requestedTenant != "" && requestedTenant != who.Tenant {
				membership, membershipErr := identityStore.GetMembership(r.Context(), requestedTenant, who.Subject)
				if membershipErr != nil || membership == nil || membership.Status != "active" {
					http.Error(w, "tenant access denied", http.StatusForbidden)
					return
				}
				who.Tenant = requestedTenant
				who.Roles = map[string]bool{membership.Role: true}
			}
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
		if path == "/tenants" && r.Method == http.MethodGet {
			if apiKeyAuth {
				writeJSON(w, map[string]any{"tenant": who.Tenant, "tenants": []map[string]any{{"id": who.Tenant, "role": "api_key"}}})
				return
			}
			directory, ok := st.(store.DirectoryStore)
			if !ok {
				http.Error(w, "directory backend unavailable", http.StatusServiceUnavailable)
				return
			}
			memberships, e := directory.ListMembershipsForSubject(ctx, who.Subject)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			items := make([]map[string]any, 0, len(memberships))
			for _, membership := range memberships {
				item := map[string]any{"id": membership.TenantID, "role": membership.Role, "status": membership.Status}
				if tenantStore, ok := st.(store.IdentityStore); ok {
					if record, tenantErr := tenantStore.GetTenant(ctx, membership.TenantID); tenantErr == nil && record != nil {
						item["slug"], item["name"] = record.Slug, record.Name
					}
				}
				items = append(items, item)
			}
			writeJSON(w, map[string]any{"subject": who.Subject, "active": who.Tenant, "tenants": items})
			return
		}
		if path == "/marketplace" && r.Method == http.MethodGet {
			if marketplaceManager != nil {
				query := r.URL.Query().Get("q")
				data, e := marketplaceManager.Snapshot(ctx, who.Tenant, query)
				if e != nil {
					http.Error(w, e.Error(), http.StatusServiceUnavailable)
					return
				}
				data["agents"] = orchestratorAgents(orchestrator)
				if marketplace != nil {
					data["mcp_servers"] = marketplace.ConfiguredMCPServers()
				}
				writeJSON(w, data)
				return
			}
			items := []map[string]any{}
			plugins := []map[string]any{}
			if marketplace != nil {
				items = marketplace.ConfiguredMCPServers()
				plugins = marketplace.ConfiguredPlugins()
			}
			writeJSON(w, map[string]any{"tenant": who.Tenant, "agents": orchestratorAgents(orchestrator), "mcp_servers": items, "plugins": plugins})
			return
		}
		if path == "/skills" && r.Method == http.MethodGet {
			if skillCatalog == nil {
				writeJSON(w, map[string]any{"enabled": false, "selected": "", "skills": []any{}})
				return
			}
			writeJSON(w, skillCatalog.SkillSnapshot())
			return
		}
		if path == "/assurance/graph" && r.Method == http.MethodGet {
			if assuranceCatalog == nil {
				http.Error(w, "assurance graph unavailable", http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, assuranceCatalog.AssuranceGraph())
			return
		}
		if path == "/assurance/events" && r.Method == http.MethodGet {
			sessions, e := st.ListSessionsForTenant(ctx, who.Tenant)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			type securityEvent struct {
				SessionID string         `json:"session_id"`
				EventType string         `json:"event_type"`
				Timestamp time.Time      `json:"timestamp"`
				Payload   map[string]any `json:"payload,omitempty"`
			}
			events := make([]securityEvent, 0)
			for _, session := range sessions {
				if session == nil {
					continue
				}
				items, eventErr := st.GetEvents(ctx, session.ID)
				if eventErr != nil {
					continue
				}
				for _, item := range items {
					if !strings.HasPrefix(item.EventType, "security.") {
						continue
					}
					// Explicitly project the safe fields. Never expose the stored
					// event payload wholesale because future event producers may add
					// prompt, tool-argument or credential fields.
					safe := make(map[string]any, 5)
					for _, key := range []string{"source", "allowed", "reason", "risk_score", "findings"} {
						if value, ok := item.Payload[key]; ok {
							safe[key] = value
						}
					}
					events = append(events, securityEvent{SessionID: item.SessionID, EventType: item.EventType, Timestamp: item.Timestamp, Payload: safe})
				}
			}
			sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp.After(events[j].Timestamp) })
			if len(events) > 200 {
				events = events[:200]
			}
			writeJSON(w, map[string]any{"tenant": who.Tenant, "events": events})
			return
		}
		if path == "/marketplace/registry" && r.Method == http.MethodGet {
			if registry, ok := marketplaceManager.(MarketplaceRegistry); ok {
				index, e := registry.RegistrySnapshot(ctx)
				if e != nil {
					http.Error(w, e.Error(), http.StatusServiceUnavailable)
					return
				}
				writeJSON(w, map[string]any{"tenant": who.Tenant, "registry": index})
				return
			}
			http.Error(w, "marketplace registry unavailable", http.StatusServiceUnavailable)
			return
		}
		if strings.HasPrefix(path, "/marketplace/artifacts/") && r.Method == http.MethodGet {
			reader, ok := marketplaceManager.(MarketplaceArtifactReader)
			if !ok {
				http.Error(w, "marketplace artifact storage unavailable", http.StatusServiceUnavailable)
				return
			}
			key, e := url.PathUnescape(strings.TrimPrefix(path, "/marketplace/artifacts/"))
			if e != nil || strings.TrimSpace(key) == "" {
				http.Error(w, "invalid artifact key", http.StatusBadRequest)
				return
			}
			body, e := reader.GetArtifact(ctx, key)
			if e != nil {
				http.Error(w, e.Error(), http.StatusNotFound)
				return
			}
			defer body.Close()
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.Copy(w, body)
			return
		}
		if strings.HasPrefix(path, "/marketplace/") && r.Method == http.MethodPost {
			if marketplaceManager == nil {
				http.Error(w, "marketplace manager unavailable", http.StatusServiceUnavailable)
				return
			}
			action := strings.TrimPrefix(path, "/marketplace/")
			requiredRole := "owner"
			if action == "approve" {
				requiredRole = "admin"
			}
			if e := tenant.Authorize(ctx, who.Tenant, requiredRole, ""); e != nil {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			var payload map[string]any
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&payload) != nil {
				http.Error(w, "invalid marketplace request", http.StatusBadRequest)
				return
			}
			result, e := marketplaceManager.Mutate(ctx, who.Tenant, who.Subject, action, payload)
			if e != nil {
				http.Error(w, e.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, result)
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
				if !tenant.ValidRole(req.Role) {
					http.Error(w, "invalid role", http.StatusBadRequest)
					return
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
		if strings.HasPrefix(path, "/members/") && (r.Method == http.MethodPatch || r.Method == http.MethodDelete) {
			if e := tenant.Authorize(ctx, who.Tenant, "owner", ""); e != nil {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			subject, e := url.PathUnescape(strings.TrimPrefix(path, "/members/"))
			if e != nil || strings.TrimSpace(subject) == "" {
				http.Error(w, "invalid member subject", http.StatusBadRequest)
				return
			}
			membership, e := identityStore.GetMembership(ctx, who.Tenant, subject)
			if e != nil || membership == nil {
				http.Error(w, "member not found", http.StatusNotFound)
				return
			}
			if r.Method == http.MethodDelete {
				membership.Status = "inactive"
			} else {
				var req struct {
					Role   string `json:"role"`
					Status string `json:"status"`
				}
				if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req) != nil {
					http.Error(w, "invalid member update", http.StatusBadRequest)
					return
				}
				if req.Role != "" {
					if !tenant.ValidRole(req.Role) {
						http.Error(w, "invalid role", http.StatusBadRequest)
						return
					}
					membership.Role = req.Role
				}
				if req.Status != "" {
					switch req.Status {
					case "active", "inactive", "invited":
						membership.Status = req.Status
					default:
						http.Error(w, "invalid member status", http.StatusBadRequest)
						return
					}
				}
			}
			if e := identityStore.SetMembership(ctx, membership); e != nil {
				http.Error(w, e.Error(), http.StatusConflict)
				return
			}
			writeJSON(w, membership)
			return
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
		if path == "/observability/export" && r.Method == http.MethodPost {
			exporter := observability.FromEnvironment()
			if exporter == nil {
				http.Error(w, "OTLP exporter is not configured", http.StatusNotImplemented)
				return
			}
			logs, spans, e := observabilityProjection(ctx, st, who.Tenant)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			logRecords := make([]observability.Log, 0, len(logs))
			for _, log := range logs {
				logRecords = append(logRecords, observability.Log{ID: log.ID, Source: log.Source, Level: log.Level, Message: log.Message, EventType: log.EventType, Timestamp: log.Timestamp, TraceID: log.TraceID, SpanID: log.SpanID})
			}
			spanRecords := make([]observability.Span, 0, len(spans))
			for _, span := range spans {
				spanRecords = append(spanRecords, observability.Span{TraceID: span.TraceID, SpanID: span.SpanID, ParentID: span.ParentID, Service: span.Service, Operation: span.Operation, Start: span.Start, Duration: span.Duration, Status: span.Status})
			}
			if e = exporter.Export(ctx, who.Tenant, logRecords, spanRecords); e != nil {
				http.Error(w, e.Error(), http.StatusBadGateway)
				return
			}
			writeJSON(w, map[string]any{"tenant": who.Tenant, "exported_logs": len(logRecords), "exported_spans": len(spanRecords)})
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if path == "/observability/logs" || path == "/observability/traces" || path == "/observability/stream" {
			logs, spans, e := observabilityProjection(ctx, st, who.Tenant)
			if e != nil {
				http.Error(w, e.Error(), http.StatusServiceUnavailable)
				return
			}
			if path == "/observability/logs" {
				writeJSON(w, map[string]any{"tenant": who.Tenant, "logs": logs})
				return
			}
			if path == "/observability/traces" {
				writeJSON(w, map[string]any{"tenant": who.Tenant, "spans": spans})
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
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				logs, spans, projectionErr := observabilityProjection(r.Context(), st, who.Tenant)
				if projectionErr != nil {
					return
				}
				payload, marshalErr := json.Marshal(map[string]any{"logs": logs, "spans": spans})
				if marshalErr == nil {
					_, _ = fmt.Fprintf(w, "event: observability\\ndata: %s\\n\\n", payload)
				}
				flusher.Flush()
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
				}
			}
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
			replayPlan := strings.HasSuffix(sessionPath, "/replay")
			id := strings.TrimSuffix(strings.TrimSuffix(sessionPath, "/bundle"), "/replay")
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
			if replayPlan {
				replayEvents := make([]replay.Event, 0, len(messages)+len(findings)+len(events))
				sequence := 0
				for _, message := range messages {
					raw, _ := json.Marshal(message)
					replayEvents = append(replayEvents, replay.Event{Sequence: sequence, Type: "message", Payload: json.RawMessage(raw)})
					sequence++
				}
				for _, finding := range findings {
					replayEvents = append(replayEvents, replay.Event{Sequence: sequence, Type: "finding", Payload: finding})
					sequence++
				}
				for _, event := range events {
					replayEvents = append(replayEvents, replay.Event{Sequence: sequence, Type: event.EventType, Payload: event.Payload, At: event.Timestamp})
					sequence++
				}
				plan, buildErr := replay.Build(id, who.Tenant, replayEvents)
				if buildErr != nil {
					http.Error(w, buildErr.Error(), http.StatusInternalServerError)
					return
				}
				writeJSON(w, plan)
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
			incidents := incident.FromPosture(who.Tenant, len(failed), len(dead), len(unhealthy), time.Now().UTC())
			writeJSON(w, map[string]any{"tenant": who.Tenant, "failed_jobs": failed, "dead_jobs": dead, "unhealthy_agents": unhealthy, "incidents": incidents, "requires_attention": len(incidents) > 0})
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
				"optimization": map[string]any{"status": "agent-routing-active-model-routing-observational", "recommendation": "Agent task selection already applies the configured economy policy; model/provider route changes remain recommendation-only until explicitly enabled by provider policy", "automatic_changes": false, "automatic_agent_routing": true},
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
