// Package a2a provides the small, transport-neutral core of HarnessMesh's
// Agent2Agent integration. It deliberately keeps orchestration and policy in
// HarnessMesh while exposing the standard discovery/task shape to external
// agents.
package a2a

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/contextpack"
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
	trimmed := strings.TrimPrefix(r.URL.Path, "/a2a/v1/tasks/")
	stream := strings.HasSuffix(trimmed, "/stream") || strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	id := strings.TrimSuffix(trimmed, "/stream")
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
	if stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		raw, _ := json.Marshal(task)
		_, _ = fmt.Fprintf(w, "event: task.status\ndata: %s\n\n", string(raw))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// Client is the outbound A2A federation client used by HarnessMesh to discover
// and delegate tasks to remote A2A-compliant agent clusters (e.g. CrewAI,
// LangGraph or Agno services) while preserving tenant isolation and redacting
// secrets before network egress.
type Client struct {
	Endpoint   string
	Token      string
	HTTPClient *http.Client
}

func (c Client) Discover(ctx context.Context) (AgentCard, error) {
	if strings.TrimSpace(c.Endpoint) == "" {
		return AgentCard{}, errors.New("a2a endpoint is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Endpoint, "/")+"/.well-known/agent-card.json", nil)
	if err != nil {
		return AgentCard{}, err
	}
	c.headers(req, "")
	resp, err := c.client().Do(req)
	if err != nil {
		return AgentCard{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return AgentCard{}, fmt.Errorf("a2a discovery returned %s", resp.Status)
	}
	var card AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		return AgentCard{}, err
	}
	return card, nil
}

func (c Client) SubmitTask(ctx context.Context, req TaskRequest) (Task, error) {
	if strings.TrimSpace(c.Endpoint) == "" {
		return Task{}, errors.New("a2a endpoint is required")
	}
	if strings.TrimSpace(req.Tenant) == "" || strings.TrimSpace(req.Input) == "" {
		return Task{}, errors.New("a2a tenant and input are required")
	}
	req.Input = contextpack.RedactSecrets(req.Input)
	raw, err := json.Marshal(req)
	if err != nil {
		return Task{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Endpoint, "/")+"/a2a/v1/tasks", bytes.NewReader(raw))
	if err != nil {
		return Task{}, err
	}
	c.headers(httpReq, req.Tenant)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(httpReq)
	if err != nil {
		return Task{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Task{}, fmt.Errorf("a2a task submission returned %s", resp.Status)
	}
	var task Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return Task{}, err
	}
	task.Output = contextpack.RedactSecrets(task.Output)
	return task, nil
}

func (c Client) GetTask(ctx context.Context, tenant, taskID string) (Task, error) {
	if strings.TrimSpace(c.Endpoint) == "" || strings.TrimSpace(tenant) == "" || strings.TrimSpace(taskID) == "" {
		return Task{}, errors.New("a2a endpoint, tenant and taskID are required")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Endpoint, "/")+"/a2a/v1/tasks/"+strings.TrimSpace(taskID), nil)
	if err != nil {
		return Task{}, err
	}
	c.headers(httpReq, tenant)
	resp, err := c.client().Do(httpReq)
	if err != nil {
		return Task{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Task{}, fmt.Errorf("a2a task lookup returned %s", resp.Status)
	}
	var task Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		return Task{}, err
	}
	task.Output = contextpack.RedactSecrets(task.Output)
	return task, nil
}

func (c Client) headers(req *http.Request, tenant string) {
	if tenant != "" {
		req.Header.Set("X-HarnessMesh-Tenant", tenant)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

func (c Client) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 20 * time.Second}
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
