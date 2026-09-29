package bridge

import (
	"container/ring"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

// BridgeProtocolVersion identifies the versioned WebSocket event envelope
// documented in docs/chatgpt-integration.md. Consumers should ignore
// envelopes with an unrecognized protocol string rather than failing.
const BridgeProtocolVersion = "harnessmesh.bridge/v1"

// Event is the documented WebSocket event envelope. It carries exactly one
// collaboration-plane event; HarnessMesh never puts model-generated
// reasoning or LLM output inside it, only transport state.
type Event struct {
	Protocol      string         `json:"protocol"`
	EventID       string         `json:"event_id"`
	Timestamp     time.Time      `json:"timestamp"`
	WorkspaceID   string         `json:"workspace_id,omitempty"`
	SessionID     string         `json:"session_id,omitempty"`
	Type          string         `json:"type"`
	Actor         string         `json:"actor,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
}

const (
	wsSendQueueSize  = 64
	wsWriteWait      = 10 * time.Second
	wsPongWait       = 60 * time.Second
	wsPingInterval   = 30 * time.Second
	wsMaxMessageSize = 8 << 10 // clients only ever send pings/subscribe control frames
	replayBufferSize = 256
)

// hub fans out collaboration events to connected WebSocket clients. Each
// client has a small bounded send queue; a client that cannot keep up is
// disconnected rather than allowed to grow the queue without bound, so a
// slow or dead VS Code client can never exhaust server memory.
type hub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}

	replayMu  sync.Mutex
	replay    *ring.Ring
	replaySeq map[string]struct{} // recent event IDs, for dedup on rapid reconnects

	droppedSlowClients atomic.Uint64
	eventsBroadcast    atomic.Uint64
}

func newHub() *hub {
	return &hub{
		clients:   make(map[*wsClient]struct{}),
		replay:    ring.New(replayBufferSize),
		replaySeq: make(map[string]struct{}, replayBufferSize),
	}
}

type wsClient struct {
	conn      *websocket.Conn
	send      chan []byte
	spaceID   string // "" = all spaces the caller may see
	closeOnce sync.Once
	hub       *hub
}

func (h *hub) register(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) unregister(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	c.closeOnce.Do(func() { close(c.send) })
}

// broadcast fans an already-encoded event out to every client subscribed to
// its space (or to all spaces). A client whose send queue is full is
// disconnected immediately instead of blocking the broadcaster or growing
// unboundedly — this is the slow-consumer bound.
func (h *hub) broadcast(spaceID string, encoded []byte) {
	h.eventsBroadcast.Add(1)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.spaceID != "" && spaceID != "" && c.spaceID != spaceID {
			continue
		}
		select {
		case c.send <- encoded:
		default:
			h.droppedSlowClients.Add(1)
			go h.disconnectSlow(c)
		}
	}
}

func (h *hub) disconnectSlow(c *wsClient) {
	h.unregister(c)
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (h *hub) recordForReplay(eventID string, encoded []byte) {
	h.replayMu.Lock()
	defer h.replayMu.Unlock()
	h.replay.Value = encoded
	h.replay = h.replay.Next()
	h.replaySeq[eventID] = struct{}{}
	if len(h.replaySeq) > replayBufferSize*2 {
		// Cheap bound: rebuild from the ring rather than tracking eviction order.
		h.replaySeq = make(map[string]struct{}, replayBufferSize)
	}
}

// recentEvents returns up to replayBufferSize most-recently broadcast raw
// envelopes, oldest first, for GET /api/v1/events catch-up polling and for
// a freshly (re)connected WebSocket client that wants a short backfill.
func (h *hub) recentEvents() [][]byte {
	h.replayMu.Lock()
	defer h.replayMu.Unlock()
	out := make([][]byte, 0, replayBufferSize)
	h.replay.Do(func(v any) {
		if v == nil {
			return
		}
		out = append(out, v.([]byte))
	})
	return out
}

// serveWebSocket upgrades the connection and pumps events for its lifetime.
// Origin is checked explicitly against this server's allowlist before the
// upgrade, never delegated to gorilla's default (which would allow any
// origin) and never permissive by default.
func (s *Server) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	spaceID := r.URL.Query().Get("space_id")

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     func(*http.Request) bool { return true }, // already checked above
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(wsMaxMessageSize)

	c := &wsClient{
		conn:    conn,
		send:    make(chan []byte, wsSendQueueSize),
		spaceID: spaceID,
		hub:     s.hub,
	}
	s.hub.register(c)
	s.wsConnects.Add(1)

	// Best-effort short backfill so a client that just reconnected does not
	// have to re-derive state it may have missed while disconnected.
	for _, ev := range s.hub.recentEvents() {
		select {
		case c.send <- ev:
		default:
		}
	}

	go c.writePump()
	c.readPump(s)
}

func (c *wsClient) readPump(s *Server) {
	defer func() {
		c.hub.unregister(c)
		_ = c.conn.Close()
		s.wsDisconnects.Add(1)
	}()
	_ = c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (c *wsClient) writePump() {
	ticker := time.NewTicker(wsPingInterval)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// bridgeEventListener converts collaboration.EventBus events into the
// documented bridge envelope and fans them to WebSocket clients. Registered
// once against the engine's existing EventBus — this file introduces no
// second, competing event system.
func (s *Server) bridgeEventListener(_ context.Context, evt *protocol.CollaborationEvent) {
	env := Event{
		Protocol:    BridgeProtocolVersion,
		EventID:     evt.ID,
		Timestamp:   evt.Timestamp,
		WorkspaceID: evt.SpaceID,
		Type:        evt.Type,
		Actor:       evt.Source,
		Payload:     evt.Payload,
	}
	if cid, ok := evt.Payload["correlation_id"].(string); ok {
		env.CorrelationID = cid
	}
	encoded, err := json.Marshal(env)
	if err != nil {
		return
	}
	s.hub.recordForReplay(evt.ID, encoded)
	s.hub.broadcast(evt.SpaceID, encoded)
}

// checkOrigin enforces a strict, explicit allowlist. An empty allowlist
// means same-origin/non-browser clients only (no Origin header, e.g. the
// VS Code extension host's own WebSocket/HTTP client) - it never falls back
// to allowing everything.
func (s *Server) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	for _, allowed := range s.allowedOrigins {
		if strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

func fmtEventCounts(h *hub) string {
	return fmt.Sprintf("broadcast=%d dropped_slow_clients=%d", h.eventsBroadcast.Load(), h.droppedSlowClients.Load())
}
