package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/agent"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/creditguard"
)

// CodexInferenceBackend uses the Codex CLI itself as an inference engine
// (i.e. HarnessMesh would shell out to `codex exec` to answer a provider
// request). It reuses the existing, tested internal/agent CodexAdapter
// rather than reimplementing subprocess handling - see
// agent.NewHarness("codex", ...).
//
// Like OpenAIAPIBackend, this exists only so an operator who has explicitly
// disabled zero-credit mode can name it as a backend; Registry.Resolve
// refuses to reach it at all while zero-credit mode is on. codex exec does
// not stream, so this backend runs one full invocation and then emits its
// result as a single burst of StreamEvents through the same wire path
// every other backend uses.
type CodexInferenceBackend struct {
	name string
	repo string
}

func NewCodexInferenceBackend(name string, cfg config.ProviderBackendConfig) *CodexInferenceBackend {
	repo := cfg.BaseURL // reused field: for this backend type, BaseURL names a repo working directory, not an HTTP URL
	if repo == "" {
		if wd, err := os.Getwd(); err == nil {
			repo = wd
		}
	}
	return &CodexInferenceBackend{name: name, repo: repo}
}

func (b *CodexInferenceBackend) Name() string { return b.name }
func (b *CodexInferenceBackend) Type() string { return "codex" }

func (b *CodexInferenceBackend) Capabilities() Capabilities {
	return Capabilities{Streaming: false, Tools: false}
}

func (b *CodexInferenceBackend) Health(ctx context.Context) error {
	creditguard.RecordCall(creditguard.BackendCodex)
	h, err := agent.NewHarness("codex", config.AgentConfig{Kind: "codex"}, config.SwitchyardConfig{})
	if err != nil {
		return &BackendUnavailableError{Backend: b.name, Reason: err.Error()}
	}
	return h.Health(ctx)
}

func (b *CodexInferenceBackend) StreamResponse(ctx context.Context, req Request, sink Sink) error {
	// Recorded unconditionally and first, before spawning any process, so
	// tests can prove this is unreachable in zero-credit mode.
	creditguard.RecordCall(creditguard.BackendCodex)

	h, err := agent.NewHarness("codex", config.AgentConfig{Kind: "codex"}, config.SwitchyardConfig{})
	if err != nil {
		return &BackendUnavailableError{Backend: b.name, Reason: err.Error()}
	}

	prompt := flattenPrompt(req)
	result, err := h.Invoke(ctx, agent.InvokeRequest{Name: b.name, Repo: b.repo, Prompt: prompt})
	if err != nil {
		return &BackendUnavailableError{Backend: b.name, Reason: err.Error()}
	}

	responseID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	now := time.Now().Unix()
	if err := sink.Send(StreamEvent{Type: "response.created", Response: &Response{ID: responseID, Object: "response", CreatedAt: now, Status: StatusInProgress, Model: "codex", Output: []OutputItem{}}}); err != nil {
		return err
	}
	msgItemID := fmt.Sprintf("%s_msg_0", responseID)
	oi, ci := 0, 0
	if err := sink.Send(StreamEvent{Type: "response.output_item.added", OutputIndex: &oi, Item: &OutputItem{ID: msgItemID, Type: "message", Status: StatusInProgress, Role: "assistant"}}); err != nil {
		return err
	}
	if err := sink.Send(StreamEvent{Type: "response.content_part.added", ItemID: msgItemID, OutputIndex: &oi, ContentIndex: &ci}); err != nil {
		return err
	}
	if result.Text != "" {
		if err := sink.Send(StreamEvent{Type: "response.output_text.delta", ItemID: msgItemID, OutputIndex: &oi, ContentIndex: &ci, Delta: result.Text}); err != nil {
			return err
		}
	}
	if err := sink.Send(StreamEvent{Type: "response.output_text.done", ItemID: msgItemID, OutputIndex: &oi, ContentIndex: &ci, Text: result.Text}); err != nil {
		return err
	}
	msgItem := OutputItem{ID: msgItemID, Type: "message", Status: StatusCompleted, Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: result.Text}}}
	if err := sink.Send(StreamEvent{Type: "response.output_item.done", OutputIndex: &oi, Item: &msgItem}); err != nil {
		return err
	}
	final := &Response{ID: responseID, Object: "response", CreatedAt: now, Status: StatusCompleted, Model: "codex", Output: []OutputItem{msgItem}}
	return sink.Send(StreamEvent{Type: "response.completed", Response: final})
}

// flattenPrompt renders a Responses-wire Request's input items into a
// single plain-text prompt, for backends (Codex CLI) that only accept text.
func flattenPrompt(req Request) string {
	var b strings.Builder
	if req.Instructions != "" {
		b.WriteString(req.Instructions)
		b.WriteString("\n\n")
	}
	for _, item := range req.Input {
		switch item.Type {
		case "message":
			b.WriteString(item.Content.PlainText())
			b.WriteString("\n")
		case "function_call_output":
			fmt.Fprintf(&b, "[tool result for %s]: %s\n", item.CallID, item.Output.PlainText())
		}
	}
	return b.String()
}
