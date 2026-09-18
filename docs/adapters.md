# Harness Adapters in HarnessMesh v0.2.0

HarnessMesh is an agent-to-agent collaboration fabric for AI coding harnesses. It does NOT implement bespoke, hardcoded workflows for specific tools. Instead, all agent interactions are decoupled through the `agent.Harness` interface and the extensible `AdapterRegistry`.

## Supported First-Class Adapters

HarnessMesh v0.2.0 ships with four production-grade adapters and an offline test adapter:

| Adapter Name | Kind / Identifier | Mode / Roles | Primary Interaction Style |
|---|---|---|---|
| **Claude Code** | `claude` / `claude-code` | Executor, Reviewer, Peer | CLI invocation with `--print`, permission handling, context projection |
| **OpenAI Codex** | `codex` / `openai-codex` | Reviewer, Peer, Executor | Structured JSON outputs (`ReviewSchema`), `additionalProperties: false`, stderr capture |
| **Google Antigravity** | `antigravity` | Executor, Reviewer, Peer | Workspace and artifact integration, persistent subagent sessions |
| **GitHub Copilot CLI** | `copilot` / `copilot-cli` | Executor, Reviewer, Peer | Non-interactive command invocation, external opaque routing |
| **Fake / Mock** | `fake` / `mock` | Testing / CI | In-memory synchronous or recorded responses for deterministic CI runs |

For detailed adapter configuration and invocation flags, see:
- [Claude Code Adapter Guide](adapters/claude-code.md)
- [OpenAI Codex Adapter Guide](adapters/codex.md)
- [Google Antigravity Adapter Guide](adapters/antigravity.md)
- [GitHub Copilot CLI Adapter Guide](adapters/copilot-cli.md)

---

## The `agent.Harness` Interface

Every adapter implements the unified `Harness` interface defined in [`internal/agent/agent.go`](../internal/agent/agent.go):

```go
type Harness interface {
    ID() string
    AdapterType() string
    Name() string
    Capabilities() config.AgentCapabilities
    Health(ctx context.Context) error
    StartSession(ctx context.Context, repo string) (string, error)
    ResumeSession(ctx context.Context, sessionID, repo string) error
    CloseSession(ctx context.Context, sessionID string) error
    Invoke(ctx context.Context, req InvokeRequest) (InvokeResult, error)
    Run(ctx context.Context, req Request) (protocol.AgentResult, error)
}
```

### Key Responsibilities

1. **Lifecycle Management**:
   - `Health(ctx)`: Verifies binary existence, configuration, and connectivity before initiating a collaboration run.
   - `StartSession`, `ResumeSession`, `CloseSession`: Preserves conversation state and thread continuation across multi-round interactions.

2. **Invocation & Diagnostics**:
   - `Invoke(ctx, req)`: Executes the harness with the appropriate CLI arguments or API envelopes.
   - Diagnostic preservation: When an underlying CLI exits with non-zero status (e.g. exit code 1), adapters extract and attach stderr outputs to prevent hidden failures.

3. **Schema Adherence**:
   - For structured reviews (e.g. Codex), adapters supply schemas conforming to OpenAI Structured Outputs rules (e.g. requiring all object properties in `required` and setting `additionalProperties: false`).

---

## Dynamic Adapter Registry

Adapters register themselves via `agent.RegisterAdapter`:

```go
agent.RegisterAdapter("my-custom-agent", func(name string, cfg config.AgentConfig, sy config.SwitchyardConfig) (agent.Harness, error) {
    return NewCustomAdapter(name, cfg), nil
})
```

When parsing `harnessmesh.json`, HarnessMesh looks up the factory by `agent.kind` or `agent.adapter`. This ensures new harnesses can be plugged in without modifying collaboration-domain logic.

