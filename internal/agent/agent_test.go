package agent

import (
	"context"
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
)

func TestNewHarnessCapabilities(t *testing.T) {
	claudeCfg := config.AgentConfig{
		Kind:     "claude",
		Role:     "executor",
		Writable: true,
		Capabilities: config.AgentCapabilities{
			ReadRepository:  true,
			WriteRepository: true,
			RunCommands:     true,
			Review:          true,
			AnswerQuestions: true,
			SubmitEvidence:  true,
		},
	}

	hClaude, err := NewHarness("claude", claudeCfg, config.SwitchyardConfig{})
	if err != nil {
		t.Fatalf("unexpected error creating claude harness: %v", err)
	}
	if !hClaude.Capabilities().WriteRepository {
		t.Fatal("expected claude to have write capability")
	}

	codexCfg := config.AgentConfig{
		Kind:     "codex",
		Role:     "reviewer",
		Writable: false,
		Capabilities: config.AgentCapabilities{
			ReadRepository:  true,
			WriteRepository: false,
			RunCommands:     true,
			Review:          true,
			AnswerQuestions: true,
			SubmitEvidence:  true,
		},
	}

	hCodex, err := NewHarness("codex", codexCfg, config.SwitchyardConfig{})
	if err != nil {
		t.Fatalf("unexpected error creating codex harness: %v", err)
	}
	if hCodex.Capabilities().WriteRepository {
		t.Fatal("expected codex to NOT have write capability")
	}

	// Antigravity adapter test
	agyCfg := config.AgentConfig{
		Adapter:  "antigravity",
		Role:     "executor",
		Writable: true,
	}
	hAgy, err := NewHarness("agy-main", agyCfg, config.SwitchyardConfig{})
	if err != nil {
		t.Fatalf("unexpected error creating antigravity harness: %v", err)
	}
	if hAgy.ID() != "agy-main" || hAgy.AdapterType() != "antigravity" {
		t.Fatalf("unexpected antigravity ID/type: %s/%s", hAgy.ID(), hAgy.AdapterType())
	}

	// Copilot adapter test
	copilotCfg := config.AgentConfig{
		Adapter:  "copilot-cli",
		Role:     "reviewer",
		Writable: false,
	}
	hCopilot, err := NewHarness("copilot-sec", copilotCfg, config.SwitchyardConfig{})
	if err != nil {
		t.Fatalf("unexpected error creating copilot harness: %v", err)
	}
	if hCopilot.ID() != "copilot-sec" || hCopilot.AdapterType() != "copilot-cli" {
		t.Fatalf("unexpected copilot ID/type: %s/%s", hCopilot.ID(), hCopilot.AdapterType())
	}

	// Fake adapter test
	fakeCfg := config.AgentConfig{
		Adapter: "fake",
		Role:    "reviewer",
	}
	hFake, err := NewHarness("fake-agent", fakeCfg, config.SwitchyardConfig{})
	if err != nil {
		t.Fatalf("unexpected error creating fake harness: %v", err)
	}
	if hFake.ID() != "fake-agent" || hFake.AdapterType() != "fake" {
		t.Fatalf("unexpected fake ID/type: %s/%s", hFake.ID(), hFake.AdapterType())
	}

	// Check registered adapters
	adapters := RegisteredAdapters()
	if len(adapters) < 4 {
		t.Fatalf("expected at least 4 registered adapters, got %v", adapters)
	}
}

func TestFakeAdapter_EnforcesMaxTokens(t *testing.T) {
	fakeAdapter := NewFakeAdapter("fake", config.AgentConfig{})
	ctx := context.Background()

	// 1. Invoke without MaxTokens limit
	res1, err := fakeAdapter.Invoke(ctx, InvokeRequest{
		Name:   "fake",
		Prompt: "A short prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res1.Text) == 0 {
		t.Fatal("expected non-empty response")
	}

	// 2. Invoke with tight MaxTokens limit (e.g. 2 tokens = ~8 characters)
	res2, err := fakeAdapter.Invoke(ctx, InvokeRequest{
		Name:      "fake",
		Prompt:    "A very long prompt that would normally produce a long fake response",
		MaxTokens: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Text) > 8 {
		t.Fatalf("expected output truncated to at most 8 chars (2 tokens), got %d chars: %q", len(res2.Text), res2.Text)
	}
	if outTok, ok := res2.Usage["output_tokens"].(int64); ok && outTok > 2 {
		t.Fatalf("expected output_tokens <= 2 in usage, got %d", outTok)
	}
}
