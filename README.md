# HarnessMesh

**HarnessMesh is an agent-to-agent collaboration fabric for AI coding harnesses.**

- **NOT** an LLM proxy.
- **NOT** another model router.
- **NOT** a vendor-specific wrapper.

HarnessMesh is the **broker, control plane, and collaboration fabric** that enables diverse AI coding harnesses to communicate directly with each other in real-time through standard tools (MCP) without human copy/paste.

```text
               ┌─────────────┐       ┌─────────────┐
               │ Claude Code │       │ OpenAI      │
               │   Adapter   │       │    Codex    │
               └──────┬──────┘       └──────┬──────┘
                      │                     │
                      ▼                     ▼
               ═════════════════════════════════════
                          HarnessMesh
               ═════════════════════════════════════
                      ▲                     ▲
                      │                     │
               ┌──────┴──────┐       ┌──────┴──────┐
               │   Google    │       │   GitHub    │
               │ Antigravity │       │ Copilot CLI │
               └─────────────┘       └─────────────┘
```

```text
HarnessMesh (Agent Collaboration Plane)
    ├── Collaboration Control Plane & Session Broker
    ├── Model Context Protocol (MCP) Server (34 Tools)
    ├── Peer Message Bus (`harnessmesh.peer/v1`)
    ├── Dynamic Adapter Registry & Lifecycle Manager
    ├── Capability-Based Peer Selection & Routing
    ├── Parallel Multi-Review & Conservative Deduplication
    ├── Context Projection & Secret Filtering
    ├── Evidence Store & Challenge Resolution
    ├── Loop, Depth & Budget Controller
    └── Durable SQLite Session Persistence (WAL)

Switchyard (Model Routing Plane - Optional)
    ├── Model Selection & Provider Routing
    └── Latency / Capability / Cost Routing
```

Switchyard is a model-routing plane. HarnessMesh is an agent-collaboration plane.

## Compressed Knowledge Archive

Every persisted message, collaboration event, finding, evidence record, and decision is also appended to a separate knowledge archive. The default path is `knowledge.hmkz` next to the SQLite database; override it with `HARNESSMESH_KNOWLEDGE_PATH`. Set `HARNESSMESH_KNOWLEDGE_DISABLED=1` only when archiving is intentionally disabled.

The archive is an append-only stream of framed, block-compressed NDJSON using Zstandard. Records are flushed in bounded blocks, so memory usage does not grow with the history and the file can contain hundreds of millions of records without loading the complete transcript. The archive is the durable source for historical knowledge; SQLite remains the transactional operational store.

Other harnesses can use the MCP tools `knowledge.search`, `knowledge.context`, `knowledge.summary`, `knowledge.quality`, `knowledge.remember`, `knowledge.import`, `knowledge.compact`, and `knowledge.stats`. Search combines term ranking with deterministic local vector similarity and the persistent block index; it supports project/session/space/kind filters. The archive format remains provider-neutral so external model embeddings can be layered in later without changing stored records.

The archive redacts common credentials before persistence. For at-rest encryption, set `HARNESSMESH_KNOWLEDGE_KEY`; HarnessMesh derives an AES-256-GCM key from it. Records also retain source, project, repository, branch, commit, agent, model, tags, confidence, and sensitivity metadata when supplied through the API or environment variables (`HARNESSMESH_PROJECT_ID`, `HARNESSMESH_REPOSITORY`, `HARNESSMESH_BRANCH`, `HARNESSMESH_COMMIT`, `HARNESSMESH_AGENT`, `HARNESSMESH_MODEL`).

`knowledge verify` scans all compressed frames without loading the archive into memory. `knowledge export` creates an atomic byte-level backup, and `knowledge watch` imports changed transcript files on a polling interval. These operations are intended for local automation and scheduled backup jobs.

The current search implementation is bounded, relevance-ranked hybrid search with a persistent block index and local vector fallback. It is safe for very large append-only histories; provider-backed embeddings remain an optional deployment optimization.

Provider embeddings can be enabled through the `knowledge.EmbeddingProvider` interface. `NewOpenAIEmbeddingProvider()` uses `OPENAI_API_KEY`, `OPENAI_BASE_URL`, and `HARNESSMESH_EMBEDDING_MODEL` (default `text-embedding-3-small`). Storage deployments can inject a `store.BackendFactory`; SQLite remains the default and reports its capabilities through `BackendDescriptor`.

`knowledge.VectorIndex` persists provider-generated vectors with project filtering and cosine ranking. `telemetry.OTLPHTTPExporter` sends span envelopes to an OpenTelemetry Collector; set the exporter endpoint in the embedding/observability integration layer.

Agent adapters can use `executil.SandboxPolicy`/`RunWithPolicy` to enforce command and working-directory allowlists. `telemetry.Registry` exposes dependency-free Prometheus text and `Engine.ReplaySession` replays persisted events without duplicating event rows.

For release validation use `make race`, `make fuzz`, `make load`, and `make security`. CI additionally runs race tests, archive benchmarks, Docker builds, CodeQL, and `govulncheck`. A future PostgreSQL/object-storage deployment can be injected through `store.BackendFactory`; the default local SQLite backend remains fully supported.

Tagged releases use `.github/workflows/release.yml` to build, checksum, generate an SBOM, keylessly sign the checksum with Cosign, and publish release artifacts.

For deployments that need tenant isolation, restrict the MCP process with comma-separated allowlists:

```bash
export HARNESSMESH_MCP_PROJECTS="project-a,project-b"
export HARNESSMESH_MCP_CALLERS="claude,codex,antigravity"
export HARNESSMESH_MCP_RATE_LIMIT="120"
```

Failed participant invocations are retained in the SQLite `delivery_retry_outbox` with payload, attempt count, error and retry timestamp. The engine retries transient failures automatically and moves exhausted/non-retryable entries to `delivery_dead_letters`.

Operational controls are also durable: retry items support priorities, agent health and circuit-breaker state are stored in SQLite, and expensive operations can require human approval. Configure the controls in `collaboration`:

```json
{
  "collaboration": {
    "global_max_tokens": 200000,
    "global_max_cost_usd": 10,
    "approval_cost_usd": 1,
    "circuit_breaker_failures": 3,
    "circuit_breaker_cooldown": "2m"
  }
}
```

The MCP tools `operations.status`, `operations.approvals`, `operations.approve`, and `knowledge.search_advanced` expose health, quota waits, retry backlog, approval gates, metrics, and source/time-filtered knowledge search.

Provider credit and session limits are handled separately: messages such as `usage limit`, `credits exhausted`, `quota exceeded`, `reset at <timestamp>`, or `try again in <duration>` are classified as quota waits. HarnessMesh keeps the delivery pending, schedules it for the provider reset time, and resumes it automatically. A quota wait does not consume a retry attempt. If the provider gives no reset time, the default wait is 15 minutes; configure it per profile with `collaboration.quota_fallback_wait`, for example:

```json
{
  "collaboration": {
    "quota_fallback_wait": "15m"
  }
}
```

This mechanism works with Codex, Claude, Antigravity, Copilot, Switchyard, and custom adapters because it analyzes the normalized invocation error text. It does not bypass provider limits or require a second token; it only pauses durable work and probes again when the limit should have reset.

---

## Supported First-Class Harness Adapters (v0.3.0)

| Harness Adapter | CLI Tool | Default Roles | Headless Invocation |
| :--- | :--- | :--- | :--- |
| **Claude Code** | `claude` | Executor, Reviewer, Peer | `claude -p` |
| **OpenAI Codex** | `codex` | Reviewer, Executor, Peer | `codex exec` with structured outputs |
| **Google Antigravity** | `agy` | Executor, Reviewer, Peer | `agy -p --dangerously-skip-permissions` |
| **GitHub Copilot CLI** | `copilot` | Executor, Reviewer, Peer | `copilot -p --allow-all-tools` |

The architecture dynamically registers adapters via `agent.RegisterAdapter(...)`. Additional harnesses (OpenCode, Gemini CLI, Aider, remote agents) can be added without modifying collaboration-domain logic.

---

## Core Capabilities & Invariants

### 1. Persistent Multi-Agent Collaboration Spaces (`harnessmesh.collaboration/v1`)
Transforms HarnessMesh into a persistent, multi-channel, event-driven collaboration environment:
- **Predefined Structured Channels**: `#general`, `#architecture`, `#security`, `#testing`, `#findings`, `#decisions`.
- **Causal Threaded Discussions**: Tracks `root_id` and `parent_id` hierarchies with participant read cursors.
- **Participant Activity Modes**: `active` (immediate dispatch), `passive` (inbox buffering), `on_demand` (explicit mentions/capabilities), and `paused`.
- **Durable Inboxes**: Allows offline or passive agents to catch up on unread channel messages, events, and action items.
- **Verifiable Decision Records**: Formal architectural decisions linked to reproducible evidence.
- **Human Supervision Controls**: Real-time space pause, resume, and emergency stop.

### 2. Standards-Compliant MCP Server (34 Tools)
Exposes a Model Context Protocol (MCP) server over standard I/O (`stdio`):

#### Collaboration Space Tools (9 Tools)
- `collaboration.publish`: Post messages to channels and threads with tagging and evidence.
- `collaboration.reply`: Threaded reply linked causally to a parent message.
- `collaboration.inbox`: Retrieve pending messages, unread counts, and action items.
- `collaboration.channels`: List and discover available channels and topics.
- `collaboration.thread`: Retrieve full thread conversation history.
- `collaboration.subscribe`: Subscribe to channels and event topics.
- `collaboration.unsubscribe`: Unsubscribe from channels or topics.
- `collaboration.decide`: Propose and accept formal decisions with verifiable evidence.
- `collaboration.status`: Inspect space health, active participants, channels, and lifecycle state.

#### Point-to-Point Peer Tools (11 Tools)
- `peer.converse`: Multi-turn interactive peer conversation with persistent thread continuity across turns.
- `peer.list`: Enumerate active participants, adapters, and roles.
- `peer.capabilities`: Discover peers by declared capabilities.
- `peer.ask`: An active agent requests targeted assistance from a peer while working.
- `peer.reply`: Sends causal, structured replies linked to parent messages.
- `peer.request_review`: Requests single or parallel multi-peer code reviews.
- `peer.submit_finding`: Publishes structured findings into the shared session.
- `peer.submit_evidence`: Attaches verifiable evidence (test logs, diffs, race detection).
- `peer.challenge`: Formally challenges a finding, marking it as `disputed`.
- `peer.resolve`: Resolves findings based on evidence.
- `peer.status`: Inspects session progress, open/disputed counts, and budget.

### 3. Autonomous Antigravity & OpenAI Codex Peer Collaboration
Work **exclusively** within the VS Code Antigravity chat. Antigravity autonomously queries OpenAI Codex for second opinions, architecture checks, debugging help, or validation via `peer.converse`. Responses are delivered directly into the active agent context — **zero human copy-pasting**.
- Setup in one command: `harnessmesh integrate antigravity`
- Persistent thread continuity across multiple conversational turns.
- Bounded by turn limits, depth ceilings, and single-writer safety.

### 4. Event Bus & Sliding-Window Coalescing
High-throughput pub/sub event bus supporting topics like `repository.*` and `decision.*`. Rapid bursts of file changes are buffered through a sliding window (default 500ms) and coalesced into a single semantic `repository.changed` event.

### 5. Causal Loop & Reentrancy Protection
Graph-based causal loop detection traverses thread reply chains and blocks recursive agent reverberation loops (`CollaborationCycleDetectedError`).

### 6. Evidence-Driven Resolution (No Majority Voting)
Disagreements between models are never settled by "majority vote" or assuming one model is superior. Findings require verifiable repository evidence:
```text
Claim ➔ Repository Evidence ➔ Reproducer (Test / Race Detector / Compiler) ➔ Resolution
```
Any unresolved finding keeps the review status as `changes_required` (or `block` for criticals).

### 7. Single-Writer Invariant
To prevent simultaneous edits, git index corruption, and unstable test runs, HarnessMesh enforces a **single-writer invariant**:
- Exactly **one writable harness** (e.g. Claude Code or Antigravity executor).
- All **peers and reviewers** operate strictly in **read-only** sandboxes.
- Multi-writer workspaces are rejected at config validation and runtime.

### 8. Bounded Context Projection & Sensitive File Filtering
HarnessMesh never forwards raw conversational transcripts between agents. Context projections are bounded, repository-centric, and strictly filtered:
- Secret filtering excludes `.env`, `*.pem`, `*.key`, `*.p12`, `id_rsa`, `id_ed25519`, `secrets/`, `credentials/`, `terraform.tfstate`, and `.git/`.
- Path traversal (`../`) and symlink escapes outside the repository root are automatically rejected.

### 9. Crash-Safe Persistence (SQLite Schema v5)
Collaboration spaces, channels, threads, messages, subscriptions, event deliveries, decisions, participant states, and findings are durably stored in an embedded SQLite database (`~/.harnessmesh/harnessmesh.db` or `.harnessmesh/harnessmesh.db`) with WAL journaling, busy timeouts, foreign keys, and versioned schema migrations.

---

## Quick Start & Installation

### Prerequisites
- Go 1.23+
- Git

### Build from Source

```bash
git clone https://github.com/domehahn/harnessmesh.git
cd harnessmesh
go build -trimpath -o bin/harnessmesh ./cmd/harnessmesh
```

Run environment diagnostics:

```bash
bin/harnessmesh doctor
```

---

## MCP Server Integration

Install MCP configuration into your coding harness of choice:

```bash
# For Claude Code (.mcp.json)
bin/harnessmesh mcp install claude

# For OpenAI Codex (codex-mcp.json)
bin/harnessmesh mcp install codex

# For Google Antigravity (.antigravity/mcp.json)
bin/harnessmesh mcp install antigravity

# For GitHub Copilot CLI (.copilot/mcp.json)
bin/harnessmesh mcp install copilot
```

### Reuse HarnessMesh in Any Session

Install the MCP server with user scope once so it is available in new projects and future VS Code conversations:

```bash
bin/harnessmesh mcp install claude --scope user
bin/harnessmesh mcp install codex --scope user
bin/harnessmesh mcp install antigravity --scope user
bin/harnessmesh mcp install copilot --scope user
```

Install only the harnesses you actually use. Restart the harness or open a new VS Code conversation after installation. In every connected conversation, the harness can use the same persistent archive through:

```text
knowledge.search   Search previous discussions, findings, evidence, decisions, and events.
knowledge.context  Return bounded search results formatted as RAG context.
knowledge.summary  Return a bounded deterministic summary grouped by record kind.
knowledge.remember Store an explicit lesson, problem, solution, or decision.
knowledge.import   Import a copied Claude, ChatGPT, Codex, or Markdown transcript.
knowledge.compact  Remove records older than an RFC3339 retention cutoff.
knowledge.stats    Show archive path, compressed size, encryption, and record statistics.
knowledge.quality  Report verification, confidence, expiry, duplicate, and conflict signals.
```

Example instructions to give an agent at the beginning of a new conversation:

```text
Before proposing a solution, search HarnessMesh knowledge for related prior decisions,
findings, failed approaches, and evidence. Use knowledge.context with the current task
and relevant repository paths, then cite the retrieved record IDs in your reasoning.
Publish important conclusions, problems, evidence, and decisions back to HarnessMesh.
```

The default persistent locations are:

```text
~/.harnessmesh/harnessmesh.db
~/.harnessmesh/knowledge.hmkz
```

If the user home directory is not writable, HarnessMesh falls back to `.harnessmesh/` in the current working directory. Override the archive location with `HARNESSMESH_KNOWLEDGE_PATH`:

```bash
export HARNESSMESH_KNOWLEDGE_PATH="$HOME/.harnessmesh/knowledge.hmkz"
```

The same functions are available without MCP for scripts and CI:

```bash
harnessmesh knowledge import --file conversation.md --source claude-code --project-id my-project
harnessmesh knowledge search --query "sqlite migration rollback" --project-id my-project --json
harnessmesh knowledge compact --before 2025-01-01T00:00:00Z
harnessmesh knowledge verify
harnessmesh knowledge index
harnessmesh knowledge export --file /backup/knowledge.hmkz
harnessmesh knowledge restore --file /backup/knowledge.hmkz
harnessmesh knowledge rotate-key --key "$NEW_KNOWLEDGE_KEY"
# Optional: import changed transcript files continuously
harnessmesh knowledge watch --file conversation.md --project-id my-project
```

For a remote VS Code extension or another machine, start the authenticated HTTP MCP endpoint:

```bash
export HARNESSMESH_MCP_TOKEN="replace-with-a-long-random-token"
harnessmesh mcp serve --listen 127.0.0.1:8787 --token "$HARNESSMESH_MCP_TOKEN" --caller remote-agent
```

Use `Authorization: Bearer <token>` for JSON-RPC `POST /` requests. `GET /healthz` is unauthenticated for liveness checks, `GET /metrics` exposes basic Prometheus counters, and authenticated `GET /admin/knowledge` exposes archive administration statistics. Native TLS is available with `--tls-cert` and `--tls-key`; otherwise bind to localhost or use a TLS reverse proxy.

Instead of a static token, OAuth2 token introspection can be configured with `HARNESSMESH_MCP_OAUTH_INTROSPECTION_URL` and optionally `HARNESSMESH_MCP_OAUTH_CLIENT_SECRET`.

The archive is shared by all local HarnessMesh MCP sessions for that user. A plain Claude or ChatGPT conversation that is not connected to the HarnessMesh MCP server is not captured automatically. ChatGPT in a separate web conversation also cannot read the local archive unless it is connected through a compatible local or remote MCP integration. In that case, use a connected harness or `knowledge.context` as the bridge instead of copying transcripts manually.

---

## Configuration Profiles

Pre-configured collaboration profiles are provided in `configs/`:

- `configs/claude-codex.json`: Claude Code executor, OpenAI Codex reviewer.
- `configs/antigravity-codex.json`: Google Antigravity executor, OpenAI Codex reviewer.
- `configs/copilot-codex.json`: GitHub Copilot CLI executor, OpenAI Codex reviewer.
- `configs/antigravity-multi-review.json`: Antigravity executor, Codex & Claude parallel reviewers.
- `configs/codex-multi-review.json`: Codex executor, Antigravity & Claude parallel reviewers.

Validate and migrate configurations:

```bash
# Validate any configuration profile
bin/harnessmesh config validate configs/antigravity-multi-review.json

# Migrate a v0.1 configuration to v0.2
bin/harnessmesh config migrate harnessmesh.json harnessmesh.v2.json
```

---

## CLI Reference

```text
Usage:
  harnessmesh collaborate --task "..." [options]
  harnessmesh mcp serve [--repo <path>] [--config <path>] [--session <id>] [--caller <name>]
  harnessmesh mcp install <claude|codex|antigravity|copilot> [--scope <project|user>]
  harnessmesh integrate antigravity [--repo <path>]
  harnessmesh space <list|show|create|pause|resume|stop> [options]
  harnessmesh channel <list|create> [options]
  harnessmesh thread <list|show|reply> [options]
  harnessmesh inbox <list> [options]
  harnessmesh subscriptions <list|remove> [options]
  harnessmesh decide <list|propose|accept> [options]
  harnessmesh peer ask --peer <name> --question "..." [options]
  harnessmesh peer review --peer <name> [options]
  harnessmesh agents <list|show <name>>
  harnessmesh session <list|show|resume|stop> [options]
  harnessmesh findings <session-id> [--json]
  harnessmesh evidence <session-id> [--json]
  harnessmesh config <validate|migrate|print> [options]
  harnessmesh doctor [--config <path>]
  harnessmesh print-config [--config <path>]
  harnessmesh version
```

---

## Documentation

### Collaboration Fabric (v0.3.0)
- [Collaboration Spaces Guide](docs/collaboration-spaces.md)
- [Channels and Threads](docs/channels-and-threads.md)
- [Event Bus & Coalescing Engine](docs/events-and-coalescing.md)
- [Participant Inboxes & Activation Modes](docs/inbox-and-activation.md)
- [Decisions & Evidence](docs/decisions-and-evidence.md)
- [Human Supervision & Emergency Controls](docs/human-controls.md)

### Core Architecture & Integration
- [Architecture & Design](docs/ARCHITECTURE.md)
- [Antigravity & OpenAI Codex Peer Integration](docs/antigravity-integration.md)
- [Interactive Peer Conversation Protocol](docs/peer-conversation.md)
- [Model Context Protocol (MCP)](docs/mcp.md)
- [Peer Protocol Specification](docs/peer-protocol.md)
- [Security & Context Filtering](docs/security.md)
- [Context Projection Engine](docs/context-projection.md)
- [Sessions & Persistence](docs/sessions.md)
- [Collaboration Patterns](docs/collaboration-patterns.md)
- [Troubleshooting Guide](docs/troubleshooting.md)
- Adapter Guides:
  - [Claude Code Adapter](docs/adapters/claude-code.md)
  - [OpenAI Codex Adapter](docs/adapters/codex.md)
  - [Google Antigravity Adapter](docs/adapters/antigravity.md)
  - [GitHub Copilot CLI Adapter](docs/adapters/copilot-cli.md)

### Community

- [Contributing Guide](CONTRIBUTING.md)
- [Code of Conduct](CODE_OF_CONDUCT.md)
- [Support](SUPPORT.md)
- [Governance](GOVERNANCE.md)
- [Security Policy](SECURITY.md)
- [Release Process](RELEASE.md)

---

## License

Apache 2.0. See [LICENSE](LICENSE) for details.
