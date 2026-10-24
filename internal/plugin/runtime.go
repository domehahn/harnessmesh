package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strconv"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

type wireRequest struct {
	ID      string         `json:"id"`
	Method  string         `json:"method"`
	Payload map[string]any `json:"payload,omitempty"`
}

type wireResponse struct {
	ID      string         `json:"id"`
	OK      bool           `json:"ok"`
	Error   string         `json:"error,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

type runtime struct {
	manifest Manifest
	dir      string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	mu       sync.Mutex
	seq      uint64
}

func newRuntime(m Manifest, dir, binary string) (*runtime, error) {
	policy := m.sandbox(dir)
	if err := policy.Validate(dir, binary); err != nil {
		return nil, err
	}
	command, args := binary, []string{}
	if policy.NetworkMode == "none" {
		wrapper, err := exec.LookPath("bwrap")
		if err != nil {
			return nil, fmt.Errorf("plugin network isolation requires bubblewrap: %w", err)
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		wrapped := []string{"--die-with-parent", "--new-session", "--unshare-net", "--tmpfs", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
		for _, path := range []string{"/usr", "/bin", "/sbin", "/lib", "/etc"} {
			if _, statErr := os.Stat(path); statErr == nil {
				wrapped = append(wrapped, "--ro-bind", path, path)
			}
		}
		wrapped = append(wrapped, "--dir", "/workspace", "--bind", absDir, "/workspace", "--chdir", "/workspace", "--", filepath.Join("/workspace", m.Binary))
		command, args = wrapper, append(wrapped, args...)
	}
	if stdruntime.GOOS == "linux" && (m.Sandbox.MaxProcesses > 0 || m.Sandbox.MemoryMB > 0) {
		// Apply per-plugin limits before exec. Container/Kubernetes cgroups still
		// remain the authoritative production boundary for the whole service.
		script := `if [ "$1" -gt 0 ]; then ulimit -u "$1" || exit 1; fi; if [ "$2" -gt 0 ]; then ulimit -v "$2" || exit 1; fi; shift 2; exec "$@"`
		command, args = "/bin/sh", append([]string{"-c", script, "harnessmesh-plugin", strconv.Itoa(m.Sandbox.MaxProcesses), strconv.Itoa(m.Sandbox.MemoryMB * 1024), command}, args...)
	}
	cmd := exec.Command(command, args...)
	cmd.Dir = dir
	cmd.Env = append([]string{}, os.Environ()...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		return nil, fmt.Errorf("start plugin %q: %w", m.Name, err)
	}
	return &runtime{manifest: m, dir: dir, cmd: cmd, stdin: in, stdout: bufio.NewReader(out)}, nil
}

func (r *runtime) close() error {
	if r == nil || r.cmd == nil {
		return nil
	}
	_ = r.stdin.Close()
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	return r.cmd.Wait()
}

func (r *runtime) call(ctx context.Context, method string, payload map[string]any) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	req := wireRequest{ID: fmt.Sprintf("%d", r.seq), Method: method, Payload: payload}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if len(data) > maxMessageBytes(r.manifest) {
		return nil, errors.New("plugin request exceeds max_message_bytes")
	}
	if _, err := fmt.Fprintf(r.stdin, "%s\n", data); err != nil {
		return nil, fmt.Errorf("write plugin request: %w", err)
	}
	timeout := time.Duration(r.manifest.Sandbox.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	read := make(chan struct {
		response wireResponse
		err      error
	}, 1)
	go func() {
		line, readErr := r.stdout.ReadBytes('\n')
		if readErr != nil {
			read <- struct {
				response wireResponse
				err      error
			}{err: readErr}
			return
		}
		if len(line) > maxMessageBytes(r.manifest) {
			read <- struct {
				response wireResponse
				err      error
			}{err: errors.New("plugin response exceeds max_message_bytes")}
			return
		}
		var response wireResponse
		if decodeErr := json.Unmarshal(line, &response); decodeErr != nil {
			read <- struct {
				response wireResponse
				err      error
			}{err: fmt.Errorf("decode plugin response: %w", decodeErr)}
			return
		}
		read <- struct {
			response wireResponse
			err      error
		}{response: response}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("plugin %q timed out after %s", r.manifest.Name, timeout)
	case result := <-read:
		if result.err != nil {
			return nil, result.err
		}
		if result.response.ID != req.ID {
			return nil, fmt.Errorf("plugin response id %q does not match request %q", result.response.ID, req.ID)
		}
		if !result.response.OK {
			return nil, errors.New(result.response.Error)
		}
		return result.response.Payload, nil
	}
}

func maxMessageBytes(m Manifest) int {
	if m.Sandbox.MaxMessageBytes > 0 {
		return m.Sandbox.MaxMessageBytes
	}
	return 1 << 20
}

type Harness struct {
	id       string
	manifest Manifest
	dir      string
	binary   string
	mu       sync.Mutex
	rt       *runtime
}

func NewHarness(id string, m Manifest, dir, binary string) (agent.Harness, error) {
	return &Harness{id: id, manifest: m, dir: dir, binary: binary}, nil
}
func (h *Harness) ID() string          { return h.id }
func (h *Harness) Name() string        { return h.id }
func (h *Harness) AdapterType() string { return h.manifest.Name }
func (h *Harness) Capabilities() config.AgentCapabilities {
	return agentConfig(h.manifest, h.dir).Capabilities
}

func (h *Harness) call(ctx context.Context, method string, payload map[string]any) (map[string]any, error) {
	h.mu.Lock()
	if h.rt == nil {
		rt, err := newRuntime(h.manifest, h.dir, h.binary)
		if err != nil {
			h.mu.Unlock()
			return nil, err
		}
		h.rt = rt
	}
	rt := h.rt
	h.mu.Unlock()
	return rt.call(ctx, method, payload)
}

func (h *Harness) Health(ctx context.Context) error { _, err := h.call(ctx, "health", nil); return err }
func (h *Harness) StartSession(ctx context.Context, repo string) (string, error) {
	return h.start(ctx, "start_session", repo, "")
}
func (h *Harness) Start(ctx context.Context, repo string) (string, error) {
	return h.StartSession(ctx, repo)
}
func (h *Harness) ResumeSession(ctx context.Context, sessionID, repo string) error {
	_, err := h.call(ctx, "resume_session", map[string]any{"session_id": sessionID, "repo": repo})
	return err
}
func (h *Harness) Resume(ctx context.Context, sessionID, repo string) error {
	return h.ResumeSession(ctx, sessionID, repo)
}
func (h *Harness) CloseSession(ctx context.Context, sessionID string) error {
	_, err := h.call(ctx, "close_session", map[string]any{"session_id": sessionID})
	return err
}
func (h *Harness) start(ctx context.Context, method, repo, sessionID string) (string, error) {
	out, err := h.call(ctx, method, map[string]any{"repo": repo, "session_id": sessionID})
	if err != nil {
		return "", err
	}
	value, _ := out["session_id"].(string)
	return value, nil
}

func (h *Harness) Invoke(ctx context.Context, req agent.InvokeRequest) (agent.InvokeResult, error) {
	out, err := h.call(ctx, "invoke", map[string]any{"name": req.Name, "repo": req.Repo, "prompt": req.Prompt, "session_id": req.SessionID, "review_mode": req.ReviewMode, "review_schema": req.ReviewSchema, "mcp_config_path": req.MCPConfigPath, "max_tokens": req.MaxTokens, "max_cost_usd": req.MaxCostUSD, "approval_id": req.ApprovalID})
	result := agent.InvokeResult{AgentName: h.id, SessionID: req.SessionID}
	if err != nil {
		return result, err
	}
	if v, ok := out["agent_name"].(string); ok {
		result.AgentName = v
	}
	if v, ok := out["session_id"].(string); ok {
		result.SessionID = v
	}
	if v, ok := out["text"].(string); ok {
		result.Text = v
	}
	if v, ok := out["usage"].(map[string]any); ok {
		result.Usage = v
	}
	if v, ok := out["duration_ms"].(float64); ok {
		result.DurationMS = int64(v)
	}
	return result, nil
}

func (h *Harness) Run(ctx context.Context, req agent.Request) (protocol.AgentResult, error) {
	result, err := h.Invoke(ctx, agent.InvokeRequest{Name: req.Name, Repo: req.Repo, Prompt: req.Prompt, SessionID: req.SessionID, ReviewMode: req.ReviewMode, ReviewSchema: req.ReviewSchema})
	return protocol.AgentResult{AgentName: result.AgentName, SessionID: result.SessionID, Text: result.Text, Usage: result.Usage, DurationMS: result.DurationMS, RawOutput: result.RawOutput}, err
}

func (h *Harness) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.rt == nil {
		return nil
	}
	err := h.rt.close()
	h.rt = nil
	return err
}

var _ agent.Harness = (*Harness)(nil)
