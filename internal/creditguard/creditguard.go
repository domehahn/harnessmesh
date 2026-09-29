// Package creditguard enforces the hard architectural boundary between the
// HarnessMesh ChatGPT collaboration bridge and any metered LLM backend.
//
// HarnessMesh transports collaboration state (messages, tasks, reviews,
// findings, evidence, artifacts) between peers. It must never perform LLM
// reasoning on ChatGPT's behalf, and in particular must never cause a call
// to the OpenAI API or the Codex CLI/SDK while acting on behalf of the
// external ChatGPT participant. This package is the single place that
// decides, and records, whether such a call would be allowed.
package creditguard

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

// Mode is the credit-isolation enforcement level.
type Mode string

const (
	// ModeStrict rejects any attempt to resolve an external/ChatGPT-role
	// participant through a metered backend. This is the default.
	ModeStrict Mode = "strict"
	// ModeOff disables the guard. Only intended for operators who have a
	// deliberate, non-ChatGPT-bridge reason to run a metered adapter under
	// an execution_mode=external agent (e.g. a genuinely metered external
	// reviewer that is not the ChatGPT bridge). Never the default.
	ModeOff Mode = "off"
)

// EnvVar is the environment variable that overrides the configured mode.
const EnvVar = "HARNESSMESH_CHATGPT_CREDIT_ISOLATION"

// Metered backend identifiers. These match the adapter "kind"/"adapter"
// values registered in internal/agent, plus the model-routing/switchyard
// backend names that resolve to the same providers.
const (
	BackendOpenAIAPI = "openai-api"
	BackendCodex     = "codex"
)

var meteredBackends = map[string]string{
	"openai":     BackendOpenAIAPI,
	"openai-api": BackendOpenAIAPI,
	"codex":      BackendCodex,
}

// NormalizeBackend maps an adapter/kind/model-routing-backend string to its
// canonical metered-backend identifier, or "" if it is not metered.
func NormalizeBackend(adapterOrKind string) string {
	return meteredBackends[strings.ToLower(strings.TrimSpace(adapterOrKind))]
}

// Violation is returned when an operation would invoke a metered backend on
// behalf of a participant the credit-isolation guard protects.
type Violation struct {
	Participant string
	Backend     string
	Reason      string
}

func (v *Violation) Error() string {
	return fmt.Sprintf("ChatGPTCreditIsolationViolation: operation would invoke a metered %s backend on behalf of participant %q: %s", v.Backend, v.Participant, v.Reason)
}

// ResolveMode returns the effective credit-isolation mode. The environment
// variable HARNESSMESH_CHATGPT_CREDIT_ISOLATION takes precedence over the
// typed configuration value passed in; an empty/unset configured value
// defaults to strict, matching the fail-closed requirement.
func ResolveMode(configured string) Mode {
	if env := strings.ToLower(strings.TrimSpace(os.Getenv(EnvVar))); env != "" {
		return Mode(env)
	}
	m := strings.ToLower(strings.TrimSpace(configured))
	if m == "" {
		return ModeStrict
	}
	return Mode(m)
}

// CheckParticipant enforces credit isolation for a single participant
// definition. isExternal/isChatGPTRole identify the participant as one the
// guard protects: any participant with execution_mode=external (the
// ChatGPT-browser bridge role) must never resolve to a metered backend
// while isolation is strict, regardless of how it is otherwise configured.
func CheckParticipant(mode Mode, participant string, isExternal bool, adapterOrKind string) error {
	if mode == ModeOff {
		return nil
	}
	if !isExternal {
		return nil
	}
	backend := NormalizeBackend(adapterOrKind)
	if backend == "" {
		return nil
	}
	return &Violation{
		Participant: participant,
		Backend:     backend,
		Reason:      fmt.Sprintf("adapter %q is a metered backend; external participants may only use passive/read-oriented adapters (e.g. mcp-remote)", adapterOrKind),
	}
}

// call counters, incremented unconditionally by the metered adapters
// (internal/agent OpenAIAdapter, CodexAdapter) at the moment they would
// perform a real network call or subprocess exec — independent of why the
// call happened. Tests assert these stay at zero across a full ChatGPT
// bridge end-to-end workflow, proving isolation empirically rather than
// only structurally.
var (
	openAICalls atomic.Uint64
	codexCalls  atomic.Uint64
)

// RecordCall increments the metered-call counter for backend. Safe for
// concurrent use.
func RecordCall(backend string) {
	switch backend {
	case BackendOpenAIAPI:
		openAICalls.Add(1)
	case BackendCodex:
		codexCalls.Add(1)
	}
}

// Calls returns the current call count for backend.
func Calls(backend string) uint64 {
	switch backend {
	case BackendOpenAIAPI:
		return openAICalls.Load()
	case BackendCodex:
		return codexCalls.Load()
	}
	return 0
}

// ResetForTest zeroes the counters. Test-only; never called from production
// code paths.
func ResetForTest() {
	openAICalls.Store(0)
	codexCalls.Store(0)
}
