# HarnessMesh Architecture

HarnessMesh is a persistent, multi-agent **collaboration control plane** for AI coding harnesses (session broker, MCP server, message/event fabric, evidence store, policy/safety boundary), plus a separate, optional **Codex-compatible model-provider gateway** (`internal/provider`) for routing the Codex VS Code extension's inference to a backend of your choosing. These are two bounded planes that share no server-side state - see [Two planes](#two-planes) below.

## Core components

| Component | Package | Responsibility |
| :--- | :--- | :--- |
| Collaboration engine | `internal/collaboration` | Sessions, spaces/channels/threads, findings/evidence/challenges, MeshCommit change transactions, the event bus |
| Protocol types | `internal/protocol` | Every wire/domain type and typed error shared across transports |
| Persistence | `internal/store` | SQLite-backed durable storage; versioned, transactional migrations |
| MCP server | `internal/mcp` | stdio, legacy HTTP, and Streamable HTTP transports over the same tool surface |
| Local bridge | `internal/bridge` | REST + WebSocket transport for the VS Code extension, over the same engine/event bus |
| Credit isolation | `internal/creditguard` | Fail-closed guard + telemetry preventing any external participant from reaching a metered backend |
| Harness adapters | `internal/agent` | Per-harness process/API adapters (Claude Code, Codex, Copilot CLI, Antigravity, OpenAI API, external/passive) |
| Context projection | `internal/contextpack` | Bounded, secret-filtered repository context extraction |
| Config | `internal/config` | Typed configuration, validation, and the single-writer / credit-isolation invariants |
| Provider gateway | `internal/provider` | Codex-compatible Responses-API model-provider HTTP server, pluggable inference backends, zero-credit policy |

## Two planes

HarnessMesh exposes two deliberately separate planes:

- **Collaboration plane** (everything above this section): MCP, the VS Code bridge, ChatGPT as a peer, findings/evidence/reviews/tasks/decisions. Provider-neutral - it never performs model inference itself.
- **Model provider plane** (`internal/provider`, [docs/codex-provider.md](codex-provider.md)): a bounded HTTP interface speaking the OpenAI Responses API wire protocol that the official Codex VS Code extension expects of a custom `model_provider`. It performs no collaboration operations and holds no collaboration state.

Both can run in the same `harnessmesh` process/config, or independently. Neither depends on the other.

```text
Codex VS Code Extension --(Responses API wire)--> HarnessMesh Provider Gateway --> local/Bedrock/(optionally OpenAI) backend
ChatGPT                 --(MCP)-----------------> HarnessMesh Collaboration Plane --> reviews/findings/tasks/evidence/decisions
```

## Participants

Every participant is either:

- **`execution_mode: managed`** - HarnessMesh may invoke it directly (spawn a subprocess, call an API) via an `internal/agent` adapter. Claude Code is the canonical example.
- **`execution_mode: external`** - HarnessMesh never invokes it. It only reads/writes collaboration state through a transport (MCP, the bridge). ChatGPT is the canonical example: its reasoning happens in the user's own ChatGPT session, and HarnessMesh only carries state to and from it.

At most one participant may be `writable: true` (the single-writer invariant), enforced at config-load time and re-checked at runtime by MeshCommit before any change transaction is created or committed.

## The ChatGPT ↔ HarnessMesh ↔ VS Code ↔ Claude Code path

```text
ChatGPT  --(Streamable HTTP /mcp)-->  HarnessMesh  --(REST+WS /api/v1)-->  VS Code extension  -->  Claude Code  -->  Repository
   ^                                       |
   |                                       |  (same EventBus, same store)
   +-------------- inbox pull <------------+
```

- ChatGPT and the VS Code extension/Claude Code talk to **the same engine and the same event bus** through two different transports (MCP vs. REST/WebSocket) - there is exactly one collaboration/task/event model, not two competing ones.
- ChatGPT is read/propose-oriented: it can publish messages, findings, evidence, and reviews, and pull its inbox, but it can never open or commit a MeshCommit change transaction and is never autonomously invoked by the engine (see [docs/chatgpt-integration.md](chatgpt-integration.md)).
- Claude Code, reached via the bridge or its own MCP connection, is the writable executor: it is the only participant that can open/commit change transactions and touch the repository.

See [docs/chatgpt-integration.md](chatgpt-integration.md) for the full ChatGPT-specific setup, permission matrix, and the credit-isolation guarantee (zero OpenAI API calls, zero Codex invocations from this path).

## MeshCommit

Repository changes are gated by **MeshCommit**: a deterministic, policy-locked evidence gate (`internal/collaboration/meshcommit.go`) that evaluates whether a change transaction's proof obligations (tests, independent review, etc.) are satisfied before it can be committed. The gate itself makes zero LLM calls - it is pure, deterministic Go. See [docs/meshcommit.md](meshcommit.md).

## Further reading

- [docs/mcp.md](mcp.md) - MCP transports, tool surface, authentication
- [docs/chatgpt-integration.md](chatgpt-integration.md) - ChatGPT-specific setup and guarantees
- [docs/codex-provider.md](codex-provider.md) - Codex-compatible model-provider gateway, zero-credit mode
- [docs/security.md](security.md) - trust model, threat model, invariants
- [docs/meshcommit.md](meshcommit.md) - evidence-gated change transactions
- [docs/collaboration-spaces.md](collaboration-spaces.md), [docs/channels-and-threads.md](channels-and-threads.md), [docs/inbox-and-activation.md](inbox-and-activation.md) - collaboration-plane domain model
