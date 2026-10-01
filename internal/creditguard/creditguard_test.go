package creditguard

import (
	"os"
	"testing"
)

func TestResolveMode_DefaultsStrict(t *testing.T) {
	os.Unsetenv(EnvVar)
	if got := ResolveMode(""); got != ModeStrict {
		t.Fatalf("expected default mode strict, got %q", got)
	}
}

func TestResolveMode_EnvOverridesConfig(t *testing.T) {
	os.Setenv(EnvVar, "off")
	defer os.Unsetenv(EnvVar)
	if got := ResolveMode("strict"); got != ModeOff {
		t.Fatalf("expected env override to win, got %q", got)
	}
}

func TestCheckParticipant_BlocksMeteredBackendForExternal(t *testing.T) {
	os.Unsetenv(EnvVar)
	err := CheckParticipant(ModeStrict, "chatgpt-browser", true, "openai-api")
	if err == nil {
		t.Fatalf("expected violation, got nil")
	}
	v, ok := err.(*Violation)
	if !ok {
		t.Fatalf("expected *Violation, got %T", err)
	}
	if v.Backend != BackendOpenAIAPI {
		t.Fatalf("expected backend openai-api, got %q", v.Backend)
	}

	err = CheckParticipant(ModeStrict, "chatgpt-browser", true, "codex")
	if err == nil {
		t.Fatalf("expected violation for codex adapter, got nil")
	}
}

func TestCheckParticipant_AllowsPassiveAdapter(t *testing.T) {
	if err := CheckParticipant(ModeStrict, "chatgpt-browser", true, "mcp-remote"); err != nil {
		t.Fatalf("expected mcp-remote adapter to be allowed, got %v", err)
	}
}

func TestCheckParticipant_AllowsMeteredBackendForManagedAgent(t *testing.T) {
	if err := CheckParticipant(ModeStrict, "claude-executor", false, "openai-api"); err != nil {
		t.Fatalf("expected managed agent to be unaffected by guard, got %v", err)
	}
}

func TestCheckParticipant_OffModeAllowsEverything(t *testing.T) {
	if err := CheckParticipant(ModeOff, "chatgpt-browser", true, "openai-api"); err != nil {
		t.Fatalf("expected mode=off to allow, got %v", err)
	}
}

func TestRecordCall_CountersIndependent(t *testing.T) {
	ResetForTest()
	if Calls(BackendOpenAIAPI) != 0 || Calls(BackendCodex) != 0 {
		t.Fatalf("expected zero counters after reset")
	}
	RecordCall(BackendOpenAIAPI)
	if Calls(BackendOpenAIAPI) != 1 {
		t.Fatalf("expected 1 openai-api call, got %d", Calls(BackendOpenAIAPI))
	}
	if Calls(BackendCodex) != 0 {
		t.Fatalf("expected codex calls to remain 0, got %d", Calls(BackendCodex))
	}
	RecordCall(BackendCodex)
	if Calls(BackendCodex) != 1 {
		t.Fatalf("expected 1 codex call, got %d", Calls(BackendCodex))
	}
	ResetForTest()
}
