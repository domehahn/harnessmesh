# NVIDIA NeMo Switchyard Integration in HarnessMesh v0.2.0

HarnessMesh v0.2.0 includes first-class support for **NVIDIA NeMo Switchyard** as an optional model-routing plane.

Switchyard manages dynamic model routing, stage routers, fallback chains, and inference multiplexing. HarnessMesh coordinates harnesses; Switchyard coordinates models.

---

## Architectural Boundary

```
+-------------------------------------------------------------+
|                     HarnessMesh Fabric                      |
|                                                             |
|  Participant Selection: Claude (Executor) vs Codex (Review) |
|  Workspace Invariant: Exactly one writer worktree           |
|  Evidence & Verification: Durable Store & MCP Protocols     |
+------------------------------+------------------------------+
                               |
                   HTTP Proxy Configuration
                   (e.g., OPENAI_BASE_URL)
                               |
                               v
+-------------------------------------------------------------+
|                 NVIDIA NeMo Switchyard                      |
|                                                             |
|  Routes: routine, coding, architecture, security            |
|  Model Selection: DeepSeek vs Claude 3.5 Sonnet vs o3-mini  |
|  Failover, Retries, Context-window Routing                  |
+-------------------------------------------------------------+
```

### The Invariant
- HarnessMesh decides **who collaborates** (which agent harness).
- Switchyard decides **which model serves the turn** (for switchyard-backed harnesses).
- HarnessMesh **never** overrides Switchyard's internal model selection.

---

## Configuration

Define Switchyard in `model_routing_backends` and reference it from agent definitions:

```json
{
  "version": 2,
  "model_routing_backends": {
    "switchyard": {
      "type": "switchyard",
      "base_url": "http://127.0.0.1:4000",
      "health_check": true
    }
  },
  "agents": {
    "codex-reviewer": {
      "kind": "codex",
      "role": "reviewer",
      "model_routing": {
        "type": "switchyard",
        "backend": "switchyard",
        "route": "routine"
      }
    }
  }
}
```

---

## Switchyard Route Configurations

A sample Switchyard routes configuration is provided at [`configs/switchyard.routes.example.toml`](../configs/switchyard.routes.example.toml):

```toml
# Routine tasks: lint fixes, format checks, small diffs
[routes.routine]
id = "routine"
type = "stage_router"
capable_target = "capable_target"
efficient_target = "routine_target"
picker = "efficient_first"
context_window = 128000

# Coding tasks: feature implementation, multi-file refactoring
[routes.coding]
id = "coding"
type = "stage_router"
capable_target = "capable_target"
efficient_target = "routine_target"
picker = "efficient_first"
context_window = 200000

# Critical security reviews: static vulnerability audits
[routes.security]
id = "security"
type = "static_target"
target = "reasoning_target"
context_window = 200000
```

---

## CLI Management Commands

HarnessMesh provides subcommands to inspect and validate Switchyard:

```bash
# Validate Switchyard backend connectivity and /v1/models endpoint
harnessmesh switchyard doctor [--config harnessmesh.json]

# Inspect route mappings for all configured agents
harnessmesh switchyard routes [--config harnessmesh.json]

# Validate Switchyard syntax in configuration files
harnessmesh switchyard config validate [--config harnessmesh.json]
```

---

## Error Handling & Degraded Modes

If Switchyard is configured but unreachable at runtime, HarnessMesh surfaces a typed error:

```go
type SwitchyardUnavailableError struct {
    URL    string `json:"url"`
    Reason string `json:"reason"`
}
```

If Switchyard is disabled (`enabled: false` or omitted), HarnessMesh runs in direct mode without requiring Switchyard to be running.

