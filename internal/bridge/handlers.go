package bridge

import (
	"net/http"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/protocol"
)

// --- workspaces ---

func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	spaces, err := s.engine.SpaceService().ListSpaces(r.Context())
	if err != nil {
		s.errors.Add(1)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": spaces})
}

func (s *Server) handleWorkspaceByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/workspaces/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasSuffix(id, "/status") {
		id = strings.TrimSuffix(id, "/status")
		status, err := s.engine.SpaceStatus(r.Context(), id)
		if err != nil {
			s.errors.Add(1)
			writeError(w, statusForEngineError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	space, err := s.engine.SpaceService().GetSpace(r.Context(), id)
	if err != nil {
		s.errors.Add(1)
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, space)
}

// --- inbox ---

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	spaceID := r.URL.Query().Get("space_id")
	participant := r.URL.Query().Get("participant")
	if participant == "" {
		participant = s.cfg.Caller
	}
	if spaceID == "" {
		writeError(w, http.StatusBadRequest, errMissingParam("space_id"))
		return
	}
	unreadOnly := r.URL.Query().Get("unread_only") == "true"
	inbox, err := s.engine.GetInbox(r.Context(), spaceID, participant, nil, unreadOnly)
	if err != nil {
		s.errors.Add(1)
		writeError(w, statusForEngineError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, inbox)
}

// --- messages ---

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.withIdempotency("POST /api/v1/messages", s.postMessage).ServeHTTP(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	var req protocol.PublishRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	from, err := s.resolveCaller(req.From)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	req.From = from
	res, err := s.engine.Publish(r.Context(), &req)
	if err != nil {
		s.errors.Add(1)
		writeError(w, statusForEngineError(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// --- tasks (MeshCommit change transactions; see docs/chatgpt-integration.md
// for why "task" maps onto HarnessMesh's existing MeshChange model rather
// than a new, parallel task abstraction) ---

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listTasks(w, r)
	case http.MethodPost:
		s.withIdempotency("POST /api/v1/tasks", s.createTask).ServeHTTP(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	spaceID := r.URL.Query().Get("space_id")
	status := protocol.MeshChangeStatus(r.URL.Query().Get("status"))
	changes, err := s.engine.ListMeshChanges(r.Context(), spaceID, status)
	if err != nil {
		s.errors.Add(1)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": changes})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req protocol.CreateChangeRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	author, err := s.resolveCaller(req.AuthorParticipant)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	req.AuthorParticipant = author
	chg, err := s.engine.CreateChange(r.Context(), &req)
	if err != nil {
		s.errors.Add(1)
		writeError(w, statusForEngineError(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, chg)
}

func (s *Server) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		chg, err := s.engine.GetMeshChange(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		paths, _ := s.engine.GetChangePaths(r.Context(), id)
		obls, _ := s.engine.GetProofObligations(r.Context(), id)
		writeJSON(w, http.StatusOK, map[string]any{"task": chg, "paths": paths, "obligations": obls})
	case http.MethodPatch:
		var body struct {
			Action string `json:"action"` // "prepare" | "abort"
			Reason string `json:"reason,omitempty"`
		}
		if err := decodeJSONBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		var chg *protocol.MeshChange
		var err error
		switch body.Action {
		case "prepare":
			chg, err = s.engine.PrepareChange(r.Context(), id, s.cfg.Caller)
		case "abort":
			chg, err = s.engine.AbortChange(r.Context(), id, s.cfg.Caller, body.Reason)
		default:
			writeError(w, http.StatusBadRequest, errUnsupportedAction(body.Action))
			return
		}
		if err != nil {
			s.errors.Add(1)
			writeError(w, statusForEngineError(err), err)
			return
		}
		writeJSON(w, http.StatusOK, chg)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- artifacts (session-scoped evidence) ---

func (s *Server) handleArtifacts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errMissingParam("session_id"))
			return
		}
		ev, err := s.engine.Store().GetEvidence(r.Context(), sessionID)
		if err != nil {
			s.errors.Add(1)
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"artifacts": ev})
	case http.MethodPost:
		s.withIdempotency("POST /api/v1/artifacts", s.postArtifact).ServeHTTP(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) postArtifact(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		protocol.EvidencePayload
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, errMissingParam("session_id"))
		return
	}
	source, err := s.resolveCaller(req.SourceAgent)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	req.EvidencePayload.SourceAgent = source
	ev, err := s.engine.SubmitEvidence(r.Context(), req.SessionID, s.cfg.Caller, req.EvidencePayload)
	if err != nil {
		s.errors.Add(1)
		writeError(w, statusForEngineError(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, ev)
}

// --- findings / reviews ---

func (s *Server) handleFindings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errMissingParam("session_id"))
			return
		}
		findings, err := s.engine.Store().GetFindings(r.Context(), sessionID)
		if err != nil {
			s.errors.Add(1)
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"findings": findings})
	case http.MethodPost:
		s.withIdempotency("POST /api/v1/findings", s.postFinding).ServeHTTP(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) postFinding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		protocol.FindingPayload
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, errMissingParam("session_id"))
		return
	}
	participant, err := s.resolveCaller(req.SourceParticipant)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	req.FindingPayload.SourceParticipant = participant
	req.FindingPayload.SourceAgent = participant
	if req.Timestamp.IsZero() {
		req.Timestamp = time.Now().UTC()
	}
	finding, err := s.engine.SubmitFinding(r.Context(), req.SessionID, s.cfg.Caller, req.FindingPayload)
	if err != nil {
		s.errors.Add(1)
		writeError(w, statusForEngineError(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, finding)
}

// --- events (polling fallback / reconnect catch-up) ---

func (s *Server) handleEventsPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := queryInt(r, "limit", 100)
	events := s.hub.recentEvents()
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"events":[`))
	for i, raw := range events {
		if i > 0 {
			_, _ = w.Write([]byte(","))
		}
		_, _ = w.Write(raw)
	}
	_, _ = w.Write([]byte(`]}`))
}
