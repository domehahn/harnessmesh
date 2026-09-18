package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
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
	MaxInputTokens       int64   `json:"max_input_tokens,omitempty"`
	MaxOutputTokens      int64   `json:"max_output_tokens,omitempty"`
	MaxTotalTokens       int64   `json:"max_total_tokens,omitempty"`
	MaxCostUSD           float64 `json:"max_cost_usd,omitempty"`
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

type SwitchyardConfig struct {
	Enabled               bool              `json:"enabled"`
	BaseURL               string            `json:"base_url"`
	RouteID               string            `json:"route_id"`
	InjectPlaceholderAuth bool              `json:"inject_placeholder_auth"`
	ExtraEnv              map[string]string `json:"extra_env"`
}

type AgentCapabilities struct {
	ReadRepository  bool `json:"read_repository"`
	WriteRepository bool `json:"write_repository"`
	RunCommands     bool `json:"run_commands"`
	Review          bool `json:"review"`
	AnswerQuestions bool `json:"answer_questions"`
	SubmitEvidence  bool `json:"submit_evidence"`
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
		return nil, fmt.Errorf("at least one agent must be configured")
	}

	writableCount := 0
	for name, a := range cfg.Agents {
		// Normalize kind / adapter
		if a.Adapter != "" && a.Kind == "" {
			a.Kind = a.Adapter
		}
		a.Kind = strings.ToLower(strings.TrimSpace(a.Kind))
		if a.Kind == "" {
			return nil, fmt.Errorf("agent %q: kind or adapter is required", name)
		}
		switch a.Kind {
		case "claude", "claude-code", "codex", "antigravity", "agy", "copilot", "copilot-cli", "github-copilot", "fake", "mock":
		default:
			return nil, fmt.Errorf("agent %q: unsupported kind/adapter %q", name, a.Kind)
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

	return &cfg, nil
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
