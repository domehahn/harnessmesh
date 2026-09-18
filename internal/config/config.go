package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	SchemaVersion int                    `json:"schema_version"`
	Workflow      WorkflowConfig         `json:"workflow"`
	Context       ContextConfig          `json:"context"`
	Switchyard    SwitchyardConfig       `json:"switchyard"`
	Agents        map[string]AgentConfig `json:"agents"`
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
	MaxDiffChars       int  `json:"max_diff_chars"`
	MaxTestOutputChars int  `json:"max_test_output_chars"`
	DiffContextLines   int  `json:"diff_context_lines"`
	IncludeUntracked   bool `json:"include_untracked"`
}

type SwitchyardConfig struct {
	Enabled               bool              `json:"enabled"`
	BaseURL               string            `json:"base_url"`
	RouteID               string            `json:"route_id"`
	InjectPlaceholderAuth bool              `json:"inject_placeholder_auth"`
	ExtraEnv              map[string]string `json:"extra_env"`
}

type AgentConfig struct {
	Kind              string            `json:"kind"`
	Command           string            `json:"command"`
	Model             string            `json:"model"`
	Mode              string            `json:"mode"`
	MaxTurns          int               `json:"max_turns"`
	MaxBudgetUSD      float64           `json:"max_budget_usd"`
	TimeoutMinutes    int               `json:"timeout_minutes"`
	ExtraArgs         []string          `json:"extra_args"`
	Env               map[string]string `json:"env"`
	WorkingDir        string            `json:"working_dir"`
	UseSwitchyard     bool              `json:"use_switchyard"`
	SwitchyardRouteID string            `json:"switchyard_route_id"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = 1
	}
	if cfg.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported schema_version %d", cfg.SchemaVersion)
	}
	if cfg.Workflow.Executor == "" || cfg.Workflow.Reviewer == "" {
		return nil, fmt.Errorf("workflow.executor and workflow.reviewer are required")
	}
	if cfg.Workflow.MaxRounds <= 0 {
		cfg.Workflow.MaxRounds = 3
	}
	if cfg.Context.MaxDiffChars <= 0 {
		cfg.Context.MaxDiffChars = 120000
	}
	if cfg.Context.MaxTestOutputChars <= 0 {
		cfg.Context.MaxTestOutputChars = 30000
	}
	if cfg.Context.DiffContextLines <= 0 {
		cfg.Context.DiffContextLines = 30
	}
	if cfg.Switchyard.RouteID == "" {
		cfg.Switchyard.RouteID = "switchyard"
	}
	if cfg.Switchyard.ExtraEnv == nil {
		cfg.Switchyard.ExtraEnv = map[string]string{}
	}

	if len(cfg.Agents) == 0 {
		return nil, fmt.Errorf("at least one agent must be configured")
	}
	for name, a := range cfg.Agents {
		a.Kind = strings.ToLower(strings.TrimSpace(a.Kind))
		if a.Kind == "" {
			return nil, fmt.Errorf("agent %q: kind is required", name)
		}
		switch a.Kind {
		case "claude", "codex":
		default:
			return nil, fmt.Errorf("agent %q: unsupported kind %q", name, a.Kind)
		}
		if a.TimeoutMinutes <= 0 {
			a.TimeoutMinutes = 45
		}
		if a.MaxTurns <= 0 {
			a.MaxTurns = 50
		}
		if a.Mode == "" {
			if a.Kind == "codex" {
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

	if _, ok := cfg.Agents[cfg.Workflow.Executor]; !ok {
		return nil, fmt.Errorf("workflow executor %q is not defined", cfg.Workflow.Executor)
	}
	if _, ok := cfg.Agents[cfg.Workflow.Reviewer]; !ok {
		return nil, fmt.Errorf("workflow reviewer %q is not defined", cfg.Workflow.Reviewer)
	}
	return &cfg, nil
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
