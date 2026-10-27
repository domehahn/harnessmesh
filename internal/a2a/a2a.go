// Package a2a provides the small, transport-neutral core of HarnessMesh's
// Agent2Agent integration. It deliberately keeps orchestration and policy in
// HarnessMesh while exposing the standard discovery/task shape to external
// agents.
package a2a

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

const ProtocolVersion = "a2a/v1"

type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// AgentCard is intentionally compatible with the public A2A discovery shape.
// URLs and capabilities are configuration, never inferred from user input.
type AgentCard struct {
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	URL          string            `json:"url"`
	Version      string            `json:"version"`
	Protocol     string            `json:"protocol_version"`
	Capabilities map[string]bool   `json:"capabilities,omitempty"`
	Skills       []Skill           `json:"skills,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type TaskRequest struct {
	ID       string         `json:"id,omitempty"`
	Tenant   string         `json:"tenant,omitempty"`
	Input    string         `json:"input"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type Task struct {
	ID        string         `json:"id"`
	Tenant    string         `json:"tenant"`
	Status    string         `json:"status"`
	Input     string         `json:"input"`
	Output    string         `json:"output,omitempty"`
	Error     string         `json:"error,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type Executor func(context.Context, TaskRequest) (string, error)

type Config struct {
	Card        AgentCard
	BearerToken string
	MaxBody     int64
	Executor    Executor
}

type Server struct {
	cfg   Config
	mu    sync.RWMutex
	tasks map[string]Task
}

func NewServer(cfg Config) (*Server, error) {
	if strings.TrimSpace(cfg.Card.Name) == "" || strings.TrimSpace(cfg.Card.URL) == "" {
		return nil, errors.New("a2a card name and url are required")
	}
	if cfg.Card.Protocol == "" {
		cfg.Card.Protocol = ProtocolVersion
	}
	if cfg.MaxBody <= 0 || cfg.MaxBody > 10<<20 {
		cfg.MaxBody = 1 << 20
	}
	return &Server{cfg: cfg, tasks: make(map[string]Task)}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", s.card)
	mux.HandleFunc("/a2a/v1/tasks", s.tasksEndpoint)
	mux.HandleFunc("/a2a/v1/tasks/", s.taskEndpoint)
	return s.security(mux)
}

func (s *Server) card(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.Card)
}

func (s *Server) tasksEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req TaskRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.cfg.MaxBody)).Decode(&req); err != nil || strings.TrimSpace(req.Input) == "" {
		http.Error(w, "invalid task request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Tenant) == "" {
		req.Tenant = r.Header.Get("X-HarnessMesh-Tenant")
	}
	if strings.TrimSpace(req.Tenant) == "" {
		http.Error(w, "tenant is required", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = "a2a_" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	now := time.Now().UTC()
	task := Task{ID: id, Tenant: req.Tenant, Status: "submitted", Input: req.Input, Metadata: req.Metadata, CreatedAt: now, UpdatedAt: now}
	if s.cfg.Executor == nil {
		task.Status = "rejected"
		task.Error = "no A2A executor configured"
	} else if output, err := s.cfg.Executor(r.Context(), req); err != nil {
		task.Status, task.Error = "failed", err.Error()
	} else {
		task.Status, task.Output = "completed", output
	}
	s.mu.Lock()
	s.tasks[id] = task
	s.mu.Unlock()
	status := http.StatusAccepted
	if task.Status == "completed" {
		status = http.StatusOK
	}
	if task.Status == "rejected" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, task)
}

func (s *Server) taskEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/a2a/v1/tasks/")
	s.mu.RLock()
	task, ok := s.tasks[id]
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if tenant := r.Header.Get("X-HarnessMesh-Tenant"); tenant == "" || tenant != task.Tenant {
		http.Error(w, "tenant mismatch", http.StatusForbidden)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if s.cfg.BearerToken != "" {
			want := "Bearer " + s.cfg.BearerToken
			got := r.Header.Get("Authorization")
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
