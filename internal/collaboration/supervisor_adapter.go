package collaboration

import (
	"context"

	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/supervisor"
)

// SupervisorAgentDirectory adapts Engine's existing configured-agent
// registry (e.cfg.Agents) to supervisor.AgentDirectory, so a Supervisor's
// proposals are validated against the real, live set of agents and their
// declared capabilities - without internal/supervisor importing
// internal/collaboration. The dependency runs one way: Core
// (collaboration) depends on the untrusted layer's interface to implement
// an adapter; the untrusted layer (supervisor) knows nothing about Core's
// concrete types.
//
// internal/collaboration currently has no tenant concept, so this adapter
// deliberately does not implement supervisor.TenantDirectory - tenant
// checks in the Authorizer/Executor are a no-op until a real tenant model
// exists here.
type SupervisorAgentDirectory struct {
	engine *Engine
}

// NewSupervisorAgentDirectory constructs the adapter over a live Engine.
func NewSupervisorAgentDirectory(e *Engine) *SupervisorAgentDirectory {
	return &SupervisorAgentDirectory{engine: e}
}

func (d *SupervisorAgentDirectory) AgentExists(id supervisor.AgentID) bool {
	if d.engine == nil || d.engine.cfg == nil {
		return false
	}
	_, ok := d.engine.cfg.Agents[string(id)]
	return ok
}

func (d *SupervisorAgentDirectory) Capabilities(id supervisor.AgentID) ([]string, bool) {
	if d.engine == nil || d.engine.cfg == nil {
		return nil, false
	}
	cfg, ok := d.engine.cfg.Agents[string(id)]
	if !ok {
		return nil, false
	}
	return capabilityNames(cfg.Capabilities), true
}

// ListAgents enumerates every configured agent for capability-based
// routing (Phase 3's Scheduler). Order is not significant.
func (d *SupervisorAgentDirectory) ListAgents(ctx context.Context) ([]supervisor.AgentContextSummary, error) {
	if d.engine == nil || d.engine.cfg == nil {
		return nil, nil
	}
	out := make([]supervisor.AgentContextSummary, 0, len(d.engine.cfg.Agents))
	for name, cfg := range d.engine.cfg.Agents {
		out = append(out, supervisor.AgentContextSummary{
			AgentID:      supervisor.AgentID(name),
			Capabilities: capabilityNames(cfg.Capabilities),
		})
	}
	return out, nil
}

// capabilityNames maps config.AgentCapabilities' declared boolean fields
// onto the string vocabulary supervisor.DefaultKnownCapabilities expects,
// so a real agent's capabilities are expressed in the same vocabulary
// Phase 1/2A tests already validate against.
func capabilityNames(caps config.AgentCapabilities) []string {
	var names []string
	if caps.ReadRepository {
		names = append(names, "read_repository")
	}
	if caps.WriteRepository {
		names = append(names, "write_repository")
	}
	if caps.RunCommands {
		names = append(names, "run_commands")
	}
	if caps.Review {
		names = append(names, "review")
	}
	if caps.AnswerQuestions {
		names = append(names, "answer_questions")
	}
	if caps.SubmitEvidence {
		names = append(names, "submit_evidence")
	}
	return names
}

var _ supervisor.AgentDirectory = (*SupervisorAgentDirectory)(nil)
var _ supervisor.AgentCatalog = (*SupervisorAgentDirectory)(nil)
