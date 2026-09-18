# Agent Configuration in HarnessMesh v0.2.0

In HarnessMesh v0.2.0, agents are defined in the `agents` block of the configuration file. Agents represent specific participants in a collaboration session, each with a declared role, execution permissions, capabilities, economy profile, and model-routing backend.

## Configuration Schema

```json
{
  "version": 2,
  "agents": {
    "claude-executor": {
      "kind": "claude",
      "role": "executor",
      "roles": ["executor"],
      "writable": true,
      "capabilities": {
        "read_repository": true,
        "write_repository": true,
        "run_commands": true,
        "review": false,
        "answer_questions": true,
        "submit_evidence": true
      },
      "economy": {
        "class": "capable",
        "relative_cost": 3.0,
        "latency": "normal"
      },
      "model_routing": {
        "type": "switchyard",
        "backend": "switchyard",
        "route": "coding"
      },
      "mode": "acceptEdits",
      "max_turns": 50,
      "timeout_minutes": 45
    },
    "codex-reviewer": {
      "kind": "codex",
      "role": "reviewer",
      "roles": ["reviewer"],
      "writable": false,
      "capabilities": {
        "read_repository": true,
        "write_repository": false,
        "run_commands": true,
        "review": true,
        "answer_questions": true,
        "submit_evidence": true
      },
      "economy": {
        "class": "efficient",
        "relative_cost": 1.0,
        "latency": "low"
      },
      "model_routing": {
        "type": "switchyard",
        "backend": "switchyard",
        "route": "routine"
      },
      "mode": "read-only",
      "max_turns": 50,
      "timeout_minutes": 45
    }
  }
}
```

---

## Field Reference

### Identity & Roles
- `kind` / `adapter`: Identifier of the harness adapter (`claude`, `codex`, `antigravity`, `copilot-cli`, `fake`).
- `role`: Primary role of the agent: `executor`, `reviewer`, or `peer`.
- `roles`: Optional array of roles if an agent can fulfill multiple roles (e.g., `["reviewer", "peer"]`).

### Workspace Permissions (Single-Writer Invariant)
- `writable` (boolean): Whether the agent is permitted to make modifications to the workspace worktree.
  > **Invariant**: Exactly one agent per session may have `writable: true`. If multiple writable agents are configured or attempt to participate in the same session, HarnessMesh rejects the session with a typed validation error.

### Capabilities Block
Defines what actions the agent can perform during collaboration:
- `read_repository`: Can inspect workspace files.
- `write_repository`: Can apply patches or edit files (must match `writable`).
- `run_commands`: Permitted to execute tests or build tools.
- `review`: Permitted to conduct code reviews and generate structured review verdicts.
- `answer_questions`: Can respond to `peer.ask` questions.
- `submit_evidence`: Can attach execution logs, test results, or diff snippets to findings.

### Economy Profile
Used by the participant selection engine (`cheapest_suitable` policy):
- `class`: `"local"`, `"efficient"`, `"capable"`, `"premium"`, or `"unknown"`.
- `relative_cost`: Relative cost weight (e.g. 1.0 for routine models, 3.0-5.0 for capable frontier models).
- `latency`: Expected response speed (`low`, `normal`, `high`).

### Model Routing
Specifies how the agent reaches its underlying model:
- `type`: Routing plane type (`switchyard`, `fixed`, `external`).
- `backend`: Name of the backend defined in `model_routing_backends`.
- `route`: Route ID passed to the routing plane (e.g. `routine`, `coding`, `security`).

---

## Agent Inspection Commands

Inspect configured agents via the HarnessMesh CLI:

```bash
# List all configured agents and their roles
harnessmesh agents list

# Show detailed specification for an agent
harnessmesh agents show claude-executor
```

