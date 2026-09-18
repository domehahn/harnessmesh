package agent

import (
	"strings"

	"github.com/domehahn/harnessmesh/internal/config"
)

func agentEnv(cfg config.AgentConfig, sy config.SwitchyardConfig) map[string]string {
	env := map[string]string{}
	for k, v := range cfg.Env {
		env[k] = v
	}
	if !cfg.UseSwitchyard || !sy.Enabled {
		return env
	}
	for k, v := range sy.ExtraEnv {
		env[k] = v
	}
	base := strings.TrimRight(sy.BaseURL, "/")
	route := cfg.SwitchyardRouteID
	if route == "" {
		route = sy.RouteID
	}
	switch cfg.Kind {
	case "claude":
		env["ANTHROPIC_BASE_URL"] = base
		if route != "" {
			env["ANTHROPIC_MODEL"] = route
		}
		if sy.InjectPlaceholderAuth {
			if _, ok := env["ANTHROPIC_API_KEY"]; !ok {
				env["ANTHROPIC_API_KEY"] = "placeholder"
			}
		}
	case "codex":
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		env["OPENAI_BASE_URL"] = base
	}
	return env
}
