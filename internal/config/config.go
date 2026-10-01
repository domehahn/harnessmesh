package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/creditguard"
)

type WorkspaceConfig struct {
	SingleWriter bool `json:"single_writer"`
}

type Config struct {
	Version              int                                  `json:"version,omitempty"`
	SchemaVersion        int                                  `json:"schema_version,omitempty"`
	Workspace            WorkspaceConfig                      `json:"workspace"`
	Collaboration        CollaborationConfig                  `json:"collaboration"`
	Workflow             WorkflowConfig                       `json:"workflow"`
	Selection            SelectionConfig                      `json:"selection,omitempty"`
	Context              ContextConfig                        `json:"context"`
	Switchyard           SwitchyardConfig                     `json:"switchyard"`
	ModelRoutingBackends map[string]ModelRoutingBackendConfig `json:"model_routing_backends,omitempty"`
	Agents               map[string]AgentConfig               `json:"agents"`
	CapabilityRouting    map[string][]string                  `json:"capability_routing,omitempty"`
	ChangeControl        ChangeControlConfig                  `json:"change_control,omitempty"`
	ChatGPT              ChatGPTBridgeConfig                  `json:"chatgpt,omitempty"`
	Bridge               BridgeConfig                         `json:"bridge,omitempty"`
	Provider             ProviderGatewayConfig                `json:"provider,omitempty"`
}

// ProviderGatewayConfig configures HarnessMesh's Codex-compatible model
// provider gateway (the Responses-API-wire HTTP server Codex's
// model_providers.harnessmesh entry points at). This is a bounded interface
// separate from the collaboration plane: it never performs collaboration
// operations, and the collaboration plane never depends on it.
type ProviderGatewayConfig struct {
	Enabled bool   `json:"enabled,omitempty"`
	Listen  string `json:"listen,omitempty"` // default 127.0.0.1:8789
	Token   string `json:"token,omitempty"`

	// ZeroAPIBillingMode, when true (the default), forbids routing any request
	// through a metered backend (openai-api, codex). zero_credit_mode remains
	// accepted as a legacy alias, but is never emitted in user-facing output.
	ZeroAPIBillingMode *bool `json:"zero_api_billing_mode,omitempty"`
	ZeroCreditMode     *bool `json:"zero_credit_mode,omitempty"`
	AllowPublicListen  bool  `json:"allow_public_listen,omitempty"`

	DefaultBackend string                           `json:"default_backend,omitempty"`
	Backends       map[string]ProviderBackendConfig `json:"backends,omitempty"`

	AllowedBackendTypes []string `json:"allowed_backend_types,omitempty"`
	DeniedBackendTypes  []string `json:"denied_backend_types,omitempty"`

	Fallback ProviderFallbackConfig `json:"fallback,omitempty"`

	RequestMaxBytes  int `json:"request_max_bytes,omitempty"`
	RequestTimeoutMS int `json:"request_timeout_ms,omitempty"`
}

// IsZeroCreditMode returns the effective zero-credit setting: true (the
// fail-closed default) unless explicitly set to false.
func (p ProviderGatewayConfig) IsZeroCreditMode() bool {
	if p.ZeroAPIBillingMode != nil {
		return *p.ZeroAPIBillingMode
	}
	if p.ZeroCreditMode == nil {
		return true
	}
	return *p.ZeroCreditMode
}

// IsZeroAPIBillingMode is the canonical name for the policy. The legacy
// method above remains for source compatibility with existing integrations.
func (p ProviderGatewayConfig) IsZeroAPIBillingMode() bool { return p.IsZeroCreditMode() }

type ProviderBackendConfig struct {
	// Type is one of: "openai-compatible" (a local/self-hosted server
	// speaking the OpenAI Chat Completions wire, e.g. vLLM/Ollama/LM
	// Studio), "openai-api" (the real, metered OpenAI API - denied by
	// default under zero-credit mode), "codex" (the Codex CLI as an
	// inference backend - denied by default under zero-credit mode),
	// "bedrock" (AWS Bedrock, if credentials/config are supplied), or
	// "chatgpt-subscription" (unimplemented placeholder - see
	// internal/provider's SubscriptionInferenceBackend doc comment).
	Type         string            `json:"type"`
	BaseURL      string            `json:"base_url,omitempty"`
	APIKeyEnv    string            `json:"api_key_env,omitempty"`
	Model        string            `json:"model,omitempty"`
	TimeoutSec   int               `json:"timeout_sec,omitempty"`
	ExtraHeaders map[string]string `json:"extra_headers,omitempty"`
}

type ProviderFallbackConfig struct {
	Enabled bool     `json:"enabled,omitempty"`
	Order   []string `json:"order,omitempty"`
}

// ChatGPTBridgeConfig configures the ChatGPT-as-collaboration-peer path.
// It never enables any OpenAI API or Codex usage; CreditIsolation is the
// explicit, fail-closed guard against that ever happening by accident.
type ChatGPTBridgeConfig struct {
	Enabled bool `json:"enabled,omitempty"`
	// Participant is the agent name (in Agents) that represents the
	// ChatGPT-browser peer. It must be execution_mode=external and
	// writable=false.
	Participant string `json:"participant,omitempty"`
	// CreditIsolation is "strict" (default) or "off". See internal/creditguard.
	CreditIsolation string `json:"credit_isolation,omitempty"`
}

// BridgeConfig configures the local REST/WebSocket bridge used by the
// VS Code extension. It is a pure collaboration-state transport and never
// itself contacts a metered LLM backend.
type BridgeConfig struct {
	Enabled          bool     `json:"enabled,omitempty"`
	Listen           string   `json:"listen,omitempty"` // default 127.0.0.1:8788
	WebSocketEnabled bool     `json:"websocket_enabled,omitempty"`
	Token            string   `json:"token,omitempty"`
	AllowedOrigins   []string `json:"allowed_origins,omitempty"`
}

type SelectionConfig struct {
	Policy string `json:"policy,omitempty"` // "cheapest_suitable", "first", "round_robin", "least_busy"
}

type ModelRoutingBackendConfig struct {
	Type        string `json:"type"` // "switchyard", "fixed", "external"
	BaseURL     string `json:"base_url,omitempty"`
	HealthCheck bool   `json:"health_check,omitempty"`
	RouteID     string `json:"route_id,omitempty"`
}

type CollaborationConfig struct {
	MaxPeerRounds        int     `json:"max_peer_rounds"`
	MaxPeerDepth         int     `json:"max_peer_depth"`
	MaxPeerCalls         int     `json:"max_peer_calls"`
	MaxConversationTurns int     `json:"max_conversation_turns,omitempty"`
	MaxParallelPeers     int     `json:"max_parallel_peers"`
	PeerSelectionPolicy  string  `json:"peer_selection_policy"` // "first", "round_robin", "least_busy"
	SessionTimeout       string  `json:"session_timeout"`
	MaxIdleDuration      string  `json:"max_idle_duration"`
	StrictTokenCeiling   bool    `json:"strict_token_ceiling,omitempty"`
	MaxInputTokens       int64   `json:"max_input_tokens,omitempty"`
	MaxOutputTokens      int64   `json:"max_output_tokens,omitempty"`
	MaxTotalTokens       int64   `json:"max_total_tokens,omitempty"`
	MaxCostUSD           float64 `json:"max_cost_usd,omitempty"`
	// QuotaFallbackWait is used when an agent reports a limit without a reset
	// timestamp. The retry worker waits this long before probing the agent again.
	QuotaFallbackWait      string  `json:"quota_fallback_wait,omitempty"`
	GlobalMaxTokens        int64   `json:"global_max_tokens,omitempty"`
	GlobalMaxCostUSD       float64 `json:"global_max_cost_usd,omitempty"`
	ApprovalCostUSD        float64 `json:"approval_cost_usd,omitempty"`
	CircuitBreakerFailures int     `json:"circuit_breaker_failures,omitempty"`
	CircuitBreakerCooldown string  `json:"circuit_breaker_cooldown,omitempty"`
}

func (c CollaborationConfig) QuotaFallbackWaitDuration() time.Duration {
	if c.QuotaFallbackWait == "" {
		return 15 * time.Minute
	}
	d, err := time.ParseDuration(c.QuotaFallbackWait)
	if err != nil || d <= 0 {
		return 15 * time.Minute
	}
	return d
}

func (c CollaborationConfig) CircuitBreakerCooldownDuration() time.Duration {
	if c.CircuitBreakerCooldown == "" {
		return 2 * time.Minute
	}
	d, err := time.ParseDuration(c.CircuitBreakerCooldown)
	if err != nil || d <= 0 {
		return 2 * time.Minute
	}
	return d
}

func (c CollaborationConfig) SessionTimeoutDuration() time.Duration {
	if c.SessionTimeout == "" {
		return 60 * time.Minute
	}
	d, err := time.ParseDuration(c.SessionTimeout)
	if err != nil {
		return 60 * time.Minute
	}
	return d
}

func (c CollaborationConfig) MaxIdleDurationDuration() time.Duration {
	if c.MaxIdleDuration == "" {
		return 15 * time.Minute
	}
	d, err := time.ParseDuration(c.MaxIdleDuration)
	if err != nil {
		return 15 * time.Minute
	}
	return d
}

type WorkflowConfig struct {
	Executor           string   `json:"executor"`
	Reviewer           string   `json:"reviewer"`
	MaxRounds          int      `json:"max_rounds"`
	MaxWallTimeMinutes int      `json:"max_wall_time_minutes"`
	StopOnRepeat       bool     `json:"stop_on_repeat"`
	TestCommand        []string `json:"test_command"`
}

type ContextConfig struct {
	MaxContextChars    int      `json:"max_context_chars"`
	MaxDiffChars       int      `json:"max_diff_chars"`
	MaxTestOutputChars int      `json:"max_test_output_chars"`
	MaxFileChars       int      `json:"max_file_chars"`
	MaxFiles           int      `json:"max_files"`
	DiffContextLines   int      `json:"diff_context_lines"`
	IncludeUntracked   bool     `json:"include_untracked"`
	AllowedPaths       []string `json:"allowed_paths,omitempty"`
	DeniedPaths        []string `json:"denied_paths,omitempty"`
}

type ChangeControlConfig struct {
	Enabled       bool                `json:"enabled"`
	DefaultProofs []string            `json:"default_proofs,omitempty"`
	Rules         []ChangeControlRule `json:"rules,omitempty"`
	AutoCommit    bool                `json:"auto_commit,omitempty"`
}

type ChangeControlRule struct {
	Paths   []string `json:"paths"`
	Require []string `json:"require"`
}

type ProofObligationSpec struct {
	Type                string   `json:"type"`
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	Required            bool     `json:"required"`
	Scope               []string `json:"scope,omitempty"`
	Command             string   `json:"command,omitempty"`
	ExpectedExitCode    int      `json:"expected_exit_code"`
	RequiredCapability  string   `json:"required_capability,omitempty"`
	RequiredParticipant string   `json:"required_participant,omitempty"`
	PolicySource        string   `json:"policy_source"`
}

// NormalizeObligationType normalizes hyphenated names like "unit-tests" to canonical "unit_tests".
func NormalizeObligationType(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, "-", "_")
	switch s {
	case "unit_test", "unit_tests", "unittest", "unittests":
		return "unit_tests"
	case "race", "race_detector", "race_detect":
		return "race_detector"
	case "build", "compile":
		return "build"
	case "lint", "vet":
		return "lint"
	case "independent_review", "peer_review", "review":
		return "independent_review"
	case "security_review", "security_scan", "security", "security_audit":
		return "security_review"
	case "architecture_review", "architecture":
		return "architecture_review"
	default:
		return s
	}
}

// MatchPathPattern tests if a relative targetPath matches a pattern (supports "**" recursive wildcards).
func MatchPathPattern(pattern, targetPath string) bool {
	pattern = filepath.ToSlash(filepath.Clean(pattern))
	targetPath = filepath.ToSlash(filepath.Clean(targetPath))
	if pattern == targetPath || pattern == "*" || pattern == "**" || pattern == "**/*" {
		return true
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return targetPath == prefix || strings.HasPrefix(targetPath, prefix+"/")
	}
	if strings.HasPrefix(pattern, "**/") {
		suffix := strings.TrimPrefix(pattern, "**/")
		return targetPath == suffix || strings.HasSuffix(targetPath, "/"+suffix)
	}
	if matched, err := filepath.Match(pattern, targetPath); err == nil && matched {
		return true
	}
	return false
}

// ResolveProofPolicy deterministically computes the set of required proof obligations for a change.
func (cc ChangeControlConfig) ResolveProofPolicy(affectedPaths []string) []ProofObligationSpec {
	proofTypes := make([]string, 0)
	seen := make(map[string]bool)
	scopes := make(map[string][]string)

	addProof := func(p string, scope []string) {
		norm := NormalizeObligationType(p)
		if norm != "" {
			if !seen[norm] {
				seen[norm] = true
				proofTypes = append(proofTypes, norm)
			}
			if len(scope) > 0 {
				scopes[norm] = append(scopes[norm], scope...)
			}
		}
	}

	// 1. Add default proofs
	var defaults []string
	if cc.DefaultProofs == nil {
		defaults = []string{"unit-tests", "independent-review"}
	} else {
		defaults = cc.DefaultProofs
	}
	for _, p := range defaults {
		addProof(p, nil)
	}

	// 2. Add rule-matched proofs
	for _, rule := range cc.Rules {
		matched := false
		if len(rule.Paths) == 0 {
			matched = true
		} else {
			for _, pat := range rule.Paths {
				for _, p := range affectedPaths {
					if MatchPathPattern(pat, p) {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
		}
		if matched {
			for _, req := range rule.Require {
				addProof(req, rule.Paths)
			}
		}
	}

	// 3. Build specifications
	specs := make([]ProofObligationSpec, 0, len(proofTypes))
	for _, pt := range proofTypes {
		spec := ProofObligationSpec{
			Type:         pt,
			Required:     true,
			PolicySource: "policy",
			Scope:        scopes[pt],
		}
		switch pt {
		case "unit_tests":
			spec.Name = "Unit Tests"
			spec.Description = "Execute repository test suite via go test ./..."
			spec.Command = "go test ./..."
			spec.ExpectedExitCode = 0
		case "race_detector":
			spec.Name = "Race Detector"
			spec.Description = "Execute test suite with Go data race detector via go test -race ./..."
			spec.Command = "go test -race ./..."
			spec.ExpectedExitCode = 0
		case "build":
			spec.Name = "Build"
			spec.Description = "Compile codebase via go build ./..."
			spec.Command = "go build ./..."
			spec.ExpectedExitCode = 0
		case "lint":
			spec.Name = "Code Analysis"
			spec.Description = "Run static analysis via go vet ./..."
			spec.Command = "go vet ./..."
			spec.ExpectedExitCode = 0
		case "independent_review":
			spec.Name = "Independent Peer Review"
			spec.Description = "Multi-finding review performed by an independent peer participant"
			spec.RequiredCapability = "review"
		case "security_review":
			spec.Name = "Security Review"
			spec.Description = "Specialized security inspection performed by a security reviewer"
			spec.RequiredCapability = "security"
		case "architecture_review":
			spec.Name = "Architecture Review"
			spec.Description = "Architectural review performed by an architecture reviewer"
			spec.RequiredCapability = "architecture"
		default:
			spec.Name = strings.Title(strings.ReplaceAll(pt, "_", " "))
			spec.Description = fmt.Sprintf("Proof obligation for %s", pt)
		}
		specs = append(specs, spec)
	}

	return specs
}

type SwitchyardConfig struct {
	Enabled               bool              `json:"enabled"`
	BaseURL               string            `json:"base_url"`
	RouteID               string            `json:"route_id"`
	InjectPlaceholderAuth bool              `json:"inject_placeholder_auth"`
	ExtraEnv              map[string]string `json:"extra_env"`
}

type AgentCapabilities struct {
	ReadRepository         bool `json:"read_repository"`
	WriteRepository        bool `json:"write_repository"`
	RunCommands            bool `json:"run_commands"`
	Review                 bool `json:"review"`
	AnswerQuestions        bool `json:"answer_questions"`
	SubmitEvidence         bool `json:"submit_evidence"`
	HardTokenLimitEnforced bool `json:"hard_token_limit_enforced,omitempty"`
}

func DefaultCapabilities(role string, writable bool) AgentCapabilities {
	caps := AgentCapabilities{
		ReadRepository:  true,
		WriteRepository: writable,
		RunCommands:     true,
		Review:          true,
		AnswerQuestions: true,
		SubmitEvidence:  true,
	}
	if role == "reviewer" {
		caps.WriteRepository = false
	}
	return caps
}

type EconomyProfile struct {
	Class        string  `json:"class,omitempty"` // "local", "efficient", "capable", "premium", "unknown"
	RelativeCost float64 `json:"relative_cost,omitempty"`
	Latency      string  `json:"latency,omitempty"` // "low", "normal", "high"
}

type AgentModelRoutingConfig struct {
	Type    string `json:"type,omitempty"` // "switchyard", "fixed", "external"
	Backend string `json:"backend,omitempty"`
	Route   string `json:"route,omitempty"`
}

type AgentConfig struct {
	Kind              string                   `json:"kind,omitempty"`
	Adapter           string                   `json:"adapter,omitempty"`
	Role              string                   `json:"role,omitempty"` // "executor", "reviewer", "peer"
	Roles             []string                 `json:"roles,omitempty"`
	Writable          bool                     `json:"writable"`
	Capabilities      AgentCapabilities        `json:"capabilities"`
	Economy           EconomyProfile           `json:"economy,omitempty"`
	ModelRouting      *AgentModelRoutingConfig `json:"model_routing,omitempty"`
	IsLocal           bool                     `json:"is_local,omitempty"`
	Binary            string                   `json:"binary,omitempty"`
	Command           string                   `json:"command"`
	Model             string                   `json:"model"`
	Mode              string                   `json:"mode"`
	MaxTurns          int                      `json:"max_turns"`
	MaxBudgetUSD      float64                  `json:"max_budget_usd"`
	TimeoutMinutes    int                      `json:"timeout_minutes"`
	ExtraArgs         []string                 `json:"extra_args"`
	Env               map[string]string        `json:"env"`
	WorkingDir        string                   `json:"working_dir"`
	AllowedPaths      []string                 `json:"allowed_paths,omitempty"`
	DeniedPaths       []string                 `json:"denied_paths,omitempty"`
	UseSwitchyard     bool                     `json:"use_switchyard"`
	SwitchyardRouteID string                   `json:"switchyard_route_id"`
	ExecutionMode     string                   `json:"execution_mode,omitempty"` // "managed", "external"
}

func (a AgentConfig) IsExternal() bool {
	return strings.EqualFold(strings.TrimSpace(a.ExecutionMode), "external")
}

func (a AgentConfig) IsManaged() bool {
	return !a.IsExternal()
}

func (a AgentConfig) HasRole(r string) bool {
	if strings.EqualFold(a.Role, r) {
		return true
	}
	for _, role := range a.Roles {
		if strings.EqualFold(role, r) {
			return true
		}
	}
	return false
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Version == 0 && cfg.SchemaVersion != 0 {
		cfg.Version = cfg.SchemaVersion
	}
	if cfg.Version == 0 {
		cfg.Version = 2
	}
	if cfg.Version != 1 && cfg.Version != 2 {
		return nil, fmt.Errorf("unsupported config version %d (must be 1 or 2)", cfg.Version)
	}

	cfg.Workspace.SingleWriter = true

	// Defaults for collaboration
	if cfg.Collaboration.MaxPeerRounds <= 0 {
		if cfg.Workflow.MaxRounds > 0 {
			cfg.Collaboration.MaxPeerRounds = cfg.Workflow.MaxRounds
		} else {
			cfg.Collaboration.MaxPeerRounds = 3
		}
	}
	if cfg.Collaboration.MaxPeerDepth <= 0 {
		cfg.Collaboration.MaxPeerDepth = 2
	}
	if cfg.Collaboration.MaxPeerCalls <= 0 {
		cfg.Collaboration.MaxPeerCalls = 10
	}
	if cfg.Collaboration.MaxParallelPeers <= 0 {
		cfg.Collaboration.MaxParallelPeers = 3
	}
	if cfg.Collaboration.PeerSelectionPolicy == "" {
		cfg.Collaboration.PeerSelectionPolicy = "first"
	}
	if cfg.Collaboration.SessionTimeout == "" {
		if cfg.Workflow.MaxWallTimeMinutes > 0 {
			cfg.Collaboration.SessionTimeout = fmt.Sprintf("%dm", cfg.Workflow.MaxWallTimeMinutes)
		} else {
			cfg.Collaboration.SessionTimeout = "60m"
		}
	}
	if cfg.Collaboration.MaxIdleDuration == "" {
		cfg.Collaboration.MaxIdleDuration = "15m"
	}

	// Defaults for workflow
	if cfg.Workflow.MaxRounds <= 0 {
		cfg.Workflow.MaxRounds = cfg.Collaboration.MaxPeerRounds
	}

	// Context limits
	if cfg.Context.MaxContextChars <= 0 {
		cfg.Context.MaxContextChars = 100000
	}
	if cfg.Context.MaxDiffChars <= 0 {
		cfg.Context.MaxDiffChars = 50000
	}
	if cfg.Context.MaxTestOutputChars <= 0 {
		cfg.Context.MaxTestOutputChars = 20000
	}
	if cfg.Context.MaxFileChars <= 0 {
		cfg.Context.MaxFileChars = 20000
	}
	if cfg.Context.MaxFiles <= 0 {
		cfg.Context.MaxFiles = 20
	}
	if cfg.Context.DiffContextLines <= 0 {
		cfg.Context.DiffContextLines = 30
	}

	// Switchyard
	if cfg.Switchyard.RouteID == "" {
		cfg.Switchyard.RouteID = "switchyard"
	}
	if cfg.Switchyard.ExtraEnv == nil {
		cfg.Switchyard.ExtraEnv = map[string]string{}
	}

	if len(cfg.Agents) == 0 {
		// Deliberately unconditional, including for provider-gateway-only
		// deployments: internal/collaboration's MeshCommitCoordinator
		// treats a config with zero registered agents as "no agent-config
		// model in play" and skips its single-writer/external-participant
		// enforcement (see requireWritableExecutor) - a legitimate
		// allowance for standalone/test use, but dangerous if a real,
		// config.Parse-validated zero-agent config were ever reused for
		// `mcp serve`/`bridge serve` (an operator mistake, e.g. copying a
		// provider-only harnessmesh.json). Requiring at least one agent
		// unconditionally means that reuse fails immediately and loudly
		// here, instead of silently building a collaboration engine whose
		// change-transaction authorization checks are all no-ops. A
		// provider-only deployment defines one placeholder agent entry -
		// see configs/codex-provider.example.json - which costs nothing at
		// runtime since `provider serve` never constructs a
		// collaboration.Engine or reads cfg.Agents at all.
		return nil, fmt.Errorf("at least one agent must be configured")
	}

	writableCount := 0
	creditIsolationMode := creditguard.ResolveMode(cfg.ChatGPT.CreditIsolation)
	for name, a := range cfg.Agents {
		// Normalize kind / adapter
		if a.Adapter != "" && a.Kind == "" {
			a.Kind = a.Adapter
		}
		a.Kind = strings.ToLower(strings.TrimSpace(a.Kind))
		if a.Kind == "" {
			return nil, fmt.Errorf("agent %q: kind or adapter is required", name)
		}
		if a.ExecutionMode == "" {
			a.ExecutionMode = "managed"
		} else {
			a.ExecutionMode = strings.ToLower(strings.TrimSpace(a.ExecutionMode))
			if a.ExecutionMode != "managed" && a.ExecutionMode != "external" {
				return nil, fmt.Errorf("agent %q: invalid execution_mode %q (must be 'managed' or 'external')", name, a.ExecutionMode)
			}
		}

		switch a.Kind {
		case "claude", "claude-code", "codex", "openai", "openai-api", "antigravity", "agy", "copilot", "copilot-cli", "github-copilot", "fake", "mock", "external", "mcp-remote", "chatgpt", "browser":
		default:
			if !a.IsExternal() {
				return nil, fmt.Errorf("agent %q: unsupported kind/adapter %q", name, a.Kind)
			}
		}

		// Credit isolation: an external participant (the ChatGPT bridge role)
		// must never be configured to resolve through a metered LLM backend.
		// Fail closed at load time so this can never reach runtime.
		if a.IsExternal() {
			if err := creditguard.CheckParticipant(creditIsolationMode, name, true, a.Kind); err != nil {
				return nil, err
			}
			if a.ModelRouting != nil {
				if err := creditguard.CheckParticipant(creditIsolationMode, name, true, a.ModelRouting.Backend); err != nil {
					return nil, err
				}
			}
		}

		if a.Role == "" {
			if len(a.Roles) > 0 {
				a.Role = a.Roles[0]
			} else if name == cfg.Workflow.Executor {
				a.Role = "executor"
			} else if name == cfg.Workflow.Reviewer {
				a.Role = "reviewer"
			} else {
				a.Role = "peer"
			}
		}
		if len(a.Roles) == 0 && a.Role != "" {
			a.Roles = []string{a.Role}
		}

		// Set default writable state based on role
		if a.Role == "executor" && !a.Writable {
			// If not explicitly disabled, default executor to writable if no other is writable
			if writableCount == 0 {
				a.Writable = true
			}
		}

		if a.Writable {
			writableCount++
		}

		// Capabilities
		if !a.Capabilities.ReadRepository && !a.Capabilities.RunCommands && !a.Capabilities.Review {
			a.Capabilities = DefaultCapabilities(a.Role, a.Writable)
		}
		// If agent is marked not writable, ensure capability reflects it
		if !a.Writable {
			a.Capabilities.WriteRepository = false
		}

		if a.TimeoutMinutes <= 0 {
			a.TimeoutMinutes = 45
		}
		if a.MaxTurns <= 0 {
			a.MaxTurns = 50
		}
		if a.Mode == "" {
			if !a.Writable {
				a.Mode = "read-only"
			} else {
				a.Mode = "default"
			}
		}
		if a.Env == nil {
			a.Env = map[string]string{}
		}
		cfg.Agents[name] = a
	}

	// Single writer validation: at most one agent may be writable
	if writableCount > 1 {
		return nil, fmt.Errorf("single-writer invariant violated: %d agents configured as writable (maximum allowed is 1)", writableCount)
	}

	// Validate workflow agent references if specified
	if cfg.Workflow.Executor != "" {
		if _, ok := cfg.Agents[cfg.Workflow.Executor]; !ok {
			return nil, fmt.Errorf("workflow executor %q is not defined", cfg.Workflow.Executor)
		}
	}
	if cfg.Workflow.Reviewer != "" {
		if _, ok := cfg.Agents[cfg.Workflow.Reviewer]; !ok {
			return nil, fmt.Errorf("workflow reviewer %q is not defined", cfg.Workflow.Reviewer)
		}
	}

	if err := validateProviderGateway(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// meteredProviderBackendTypes are the provider-backend types that resolve to
// a metered LLM backend (the real OpenAI API, or the Codex CLI/SDK used as
// an inference engine). Zero-credit mode's whole purpose is to make it
// structurally impossible for these to be reached implicitly.
var meteredProviderBackendTypes = map[string]bool{
	"openai-api": true,
	"codex":      true,
}

// IsMeteredProviderBackendType reports whether a provider backend type
// resolves to a metered LLM backend (the real OpenAI API, or Codex used as
// an inference engine). Exported so internal/provider can reuse this
// single definition instead of keeping an independent copy.
func IsMeteredProviderBackendType(t string) bool {
	return meteredProviderBackendTypes[strings.ToLower(strings.TrimSpace(t))]
}

// validateProviderGateway rejects configuration combinations that would let
// zero-credit mode be silently defeated: a default/fallback backend that
// resolves to a metered type, an allowlist that includes a metered type
// while zero-credit mode is on, a backend reference that doesn't exist, or
// an openai-compatible backend with no base_url.
func toStringSet(ss []string) map[string]bool {
	out := make(map[string]bool, len(ss))
	for _, s := range ss {
		out[strings.ToLower(strings.TrimSpace(s))] = true
	}
	return out
}

func validateProviderGateway(cfg *Config) error {
	p := cfg.Provider
	if !p.Enabled {
		return nil
	}

	if strings.TrimSpace(p.DefaultBackend) == "" {
		return fmt.Errorf("provider.default_backend is required when provider.enabled is true")
	}
	if len(p.Backends) == 0 {
		return fmt.Errorf("provider.backends must define at least one backend when provider.enabled is true")
	}
	listen := p.Listen
	if listen == "" {
		listen = "127.0.0.1:8789"
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return fmt.Errorf("provider.listen %q is invalid; expected host:port", listen)
	}
	if !p.AllowPublicListen && !isLoopbackHost(host) {
		return fmt.Errorf("provider.listen %q is not loopback-safe; set provider.allow_public_listen=true only with an authenticated, protected deployment", listen)
	}

	denied := toStringSet(p.DeniedBackendTypes)
	allowed := toStringSet(p.AllowedBackendTypes)

	for name, b := range p.Backends {
		if strings.TrimSpace(b.Type) == "" {
			return fmt.Errorf("provider.backends[%q]: type is required", name)
		}
		lt := strings.ToLower(strings.TrimSpace(b.Type))
		switch lt {
		case "openai-compatible", "openai-api", "codex", "bedrock", "chatgpt-subscription":
		default:
			return fmt.Errorf("provider.backends[%q]: unsupported backend type %q", name, b.Type)
		}
		if lt == "openai-compatible" && strings.TrimSpace(b.BaseURL) == "" {
			return fmt.Errorf("provider.backends[%q]: base_url is required for type \"openai-compatible\"", name)
		}
		if b.BaseURL != "" {
			u, parseErr := url.Parse(b.BaseURL)
			if parseErr != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("provider.backends[%q]: base_url %q must be an absolute http(s) URL", name, b.BaseURL)
			}
		}
	}

	zeroCredit := p.IsZeroCreditMode()

	// checkBackendRef mirrors internal/provider's Policy.CheckBackendType so
	// a self-contradictory config (e.g. default_backend resolving to a type
	// also listed in denied_backend_types, or excluded by
	// allowed_backend_types) is caught here rather than only discovered at
	// first request, when the gateway would otherwise report itself
	// "listening" while every request silently 403s.
	checkBackendRef := func(field, name string) error {
		if name == "" {
			return nil
		}
		b, exists := p.Backends[name]
		if !exists {
			return fmt.Errorf("provider.%s %q is not defined in provider.backends", field, name)
		}
		lt := strings.ToLower(strings.TrimSpace(b.Type))
		if zeroCredit && meteredProviderBackendTypes[lt] {
			return fmt.Errorf("provider.%s %q resolves to metered backend type %q, which zero_credit_mode forbids", field, name, b.Type)
		}
		if denied[lt] {
			return fmt.Errorf("provider.%s %q resolves to backend type %q, which is listed in provider.denied_backend_types", field, name, b.Type)
		}
		if len(allowed) > 0 && !allowed[lt] {
			return fmt.Errorf("provider.%s %q resolves to backend type %q, which is not in provider.allowed_backend_types", field, name, b.Type)
		}
		return nil
	}

	if err := checkBackendRef("default_backend", p.DefaultBackend); err != nil {
		return err
	}
	for _, name := range p.Fallback.Order {
		if err := checkBackendRef("fallback.order entry", name); err != nil {
			return err
		}
	}
	if zeroCredit {
		for _, t := range p.AllowedBackendTypes {
			if meteredProviderBackendTypes[strings.ToLower(t)] {
				return fmt.Errorf("provider.allowed_backend_types includes metered type %q, which zero_credit_mode forbids", t)
			}
		}
	}

	return nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// MigrateV1ToV2 migrates a v1 configuration JSON payload to v2.
func MigrateV1ToV2(raw []byte) ([]byte, error) {
	cfg, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse config for migration: %w", err)
	}

	cfg.Version = 2
	cfg.SchemaVersion = 2
	cfg.Workspace.SingleWriter = true

	if cfg.CapabilityRouting == nil {
		cfg.CapabilityRouting = make(map[string][]string)
		for name, a := range cfg.Agents {
			for _, role := range a.Roles {
				if role != "executor" && role != "peer" {
					cfg.CapabilityRouting[role] = append(cfg.CapabilityRouting[role], name)
				}
			}
		}
	}

	return json.MarshalIndent(cfg, "", "  ")
}

func (c *Config) Redacted() any {
	clone := *c
	clone.Agents = make(map[string]AgentConfig, len(c.Agents))
	for name, a := range c.Agents {
		if a.Env != nil {
			env := map[string]string{}
			for k, v := range a.Env {
				if looksSecret(k) {
					env[k] = "<redacted>"
				} else {
					env[k] = v
				}
			}
			a.Env = env
		}
		clone.Agents[name] = a
	}
	if clone.Switchyard.ExtraEnv != nil {
		env := map[string]string{}
		for k, v := range clone.Switchyard.ExtraEnv {
			if looksSecret(k) {
				env[k] = "<redacted>"
			} else {
				env[k] = v
			}
		}
		clone.Switchyard.ExtraEnv = env
	}
	return clone
}

func looksSecret(k string) bool {
	u := strings.ToUpper(k)
	return strings.Contains(u, "KEY") ||
		strings.Contains(u, "TOKEN") ||
		strings.Contains(u, "SECRET") ||
		strings.Contains(u, "PASSWORD")
}
