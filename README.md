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
- [ChatGPT Integration (remote MCP bridge)](docs/chatgpt-integration.md)
- [Codex Provider Gateway (zero-API-billing local/Bedrock inference for the Codex extension)](docs/codex-provider.md)
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

## Complete CLI Reference

Alle Befehle verwenden standardmäßig den lokalen SQLite-Store und die
Konfigurationsdatei harnessmesh.json, sofern kein anderer Pfad angegeben
wird. Ausgabe mit --json eignet sich für Skripte und CI. IDs in den
Beispielen werden jeweils von einem vorherigen Befehl geliefert.

### Globale Befehle

| Befehl | Zweck | Beispiel |
| :--- | :--- | :--- |
| version | Version ausgeben | bin/harnessmesh version |
| doctor | Agenten, Store, Integrationen und Routing diagnostizieren | bin/harnessmesh doctor --config harnessmesh.json |
| print-config | Redigierte Konfiguration ausgeben | bin/harnessmesh print-config --config harnessmesh.json |
| help / --help | Top-Level-Verwendung anzeigen | bin/harnessmesh --help |

### Workflow und Collaboration

| Befehl | Wichtige Optionen | Beispiel |
| :--- | :--- | :--- |
| collaborate | --task oder --task-file, --repo, --config, --executor, --reviewer, --max-rounds, --dry-run | bin/harnessmesh collaborate --task "Review auth" --repo . --config harnessmesh.json |
| peer converse | --message, --peer, --capability, --outcome, --session, --repo, --config, --caller, --causation-id, --json | bin/harnessmesh peer converse --peer codex --message "Review this diff" --outcome review --json |
| peer ask | --peer, --question, --session, --repo, --config, --caller, --json | bin/harnessmesh peer ask --peer codex --question "Find regressions" --json |
| peer review | --peer, --session, --repo, --config, --caller, --focus, --json | bin/harnessmesh peer review --peer codex --focus security,tests --json |
| peer status | --session, --repo, --config, --json | bin/harnessmesh peer status --session <SESSION_ID> --json |
| agents list | --config | bin/harnessmesh agents list --config harnessmesh.json |
| agents show | <name>, --config | bin/harnessmesh agents show codex --config harnessmesh.json |
| session list | keine | bin/harnessmesh session list |
| session show | <session-id> | bin/harnessmesh session show <SESSION_ID> |
| session messages | <session-id> | bin/harnessmesh session messages <SESSION_ID> |
| session resume | <session-id> | bin/harnessmesh session resume <SESSION_ID> |
| session stop | <session-id> | bin/harnessmesh session stop <SESSION_ID> |
| findings | <session-id>, optional --json | bin/harnessmesh findings <SESSION_ID> --json |
| evidence | <session-id>, optional --json | bin/harnessmesh evidence <SESSION_ID> --json |

### MCP, Bridge und Integrationen

| Befehl | Wichtige Optionen | Beispiel |
| :--- | :--- | :--- |
| mcp serve | --repo, --config, --session, --caller, --listen, --endpoint, --token, --tls-cert, --tls-key | bin/harnessmesh mcp serve --listen 127.0.0.1:8787 --token "$HARNESSMESH_MCP_TOKEN" |
| mcp install | claude, codex, antigravity oder copilot; --scope project oder user | bin/harnessmesh mcp install codex --scope user |
| bridge serve | --repo, --config, --caller, --listen, --token, --websocket | bin/harnessmesh bridge serve --listen 127.0.0.1:8788 --token "$HARNESSMESH_BRIDGE_TOKEN" |
| integrate antigravity | --repo, --config, --dry-run | bin/harnessmesh integrate antigravity --repo . --dry-run |
| integrate chatgpt | --repo, --config, --listen, --token, --install-claude, --dry-run | bin/harnessmesh integrate chatgpt --listen 127.0.0.1:8787 --dry-run |
| integrate codex-provider | --scope user oder project, --repo, --listen, --model, --token-env-var, --dry-run, --check, --no-backup | bin/harnessmesh integrate codex-provider --scope user --dry-run |

### Collaboration Spaces

| Befehl | Wichtige Optionen | Beispiel |
| :--- | :--- | :--- |
| space list | --json | bin/harnessmesh space list --json |
| space show | <space-id>, optional --json | bin/harnessmesh space show <SPACE_ID> --json |
| space create | --id, --title, --purpose, --writer, --config | bin/harnessmesh space create --title "Release Review" --writer antigravity |
| space pause / resume / stop | <space-id> | bin/harnessmesh space pause <SPACE_ID> |
| channel list | --space, --json | bin/harnessmesh channel list --space <SPACE_ID> --json |
| channel create | --space, --name, --description | bin/harnessmesh channel create --space <SPACE_ID> --name security |
| thread list | --space, --channel, --json | bin/harnessmesh thread list --space <SPACE_ID> --channel security --json |
| thread show | <thread-id> | bin/harnessmesh thread show <THREAD_ID> |
| thread reply | --space, --channel, --thread, --message, --from | bin/harnessmesh thread reply --space <SPACE_ID> --thread <THREAD_ID> --message "Ack" |
| inbox list | --space, --participant, --unread, --json | bin/harnessmesh inbox list --space <SPACE_ID> --participant codex --unread --json |
| subscriptions list | --space, --participant | bin/harnessmesh subscriptions list --space <SPACE_ID> |
| subscriptions add | --space, --participant, --channels, --events, --scope, --mode | bin/harnessmesh subscriptions add --space <SPACE_ID> --participant codex --channels security --mode active |
| subscriptions remove | --space, --id oder <subscription-id> | bin/harnessmesh subscriptions remove --space <SPACE_ID> --id <SUBSCRIPTION_ID> |
| decide list | --space, --json | bin/harnessmesh decide list --space <SPACE_ID> --json |
| decide propose | --space, --title, --statement, --rationale, --from | bin/harnessmesh decide propose --space <SPACE_ID> --title "Use SQLite" --statement "Keep SQLite as default" |
| decide accept | --space, --decision, --from | bin/harnessmesh decide accept --space <SPACE_ID> --decision <DECISION_ID> |

### Change Control

| Befehl | Wichtige Optionen | Beispiel |
| :--- | :--- | :--- |
| change create | --title, --intent, --space, --branch, --author, --base, --config, --json | bin/harnessmesh change create --title "Refactor API" --intent "Reduce coupling" --json |
| change list | --space, --status, --json | bin/harnessmesh change list --status prepared --json |
| change show | <change-id>, optional --json | bin/harnessmesh change show <CHANGE_ID> --json |
| change prepare | <change-id> | bin/harnessmesh change prepare <CHANGE_ID> |
| change verify | <change-id>, --obligation, --json | bin/harnessmesh change verify <CHANGE_ID> --obligation <OBLIGATION_ID> --json |
| change evidence | <change-id>, --obligation, --type, --result, --source, --notes, --json | bin/harnessmesh change evidence <CHANGE_ID> --obligation <OBLIGATION_ID> --result passed --json |
| change gate | <change-id>, optional --json | bin/harnessmesh change gate <CHANGE_ID> --json |
| change commit | <change-id>, --message, --author, --json | bin/harnessmesh change commit <CHANGE_ID> --message "Verified change" |
| change abort | <change-id>, --reason | bin/harnessmesh change abort <CHANGE_ID> --reason "Superseded" |

### Knowledge Archive

| Befehl | Wichtige Optionen | Beispiel |
| :--- | :--- | :--- |
| knowledge search | --query, --project-id, --kind, --limit, --offset, --json | bin/harnessmesh knowledge search --query "migration" --project-id demo --json |
| knowledge import | --text oder --file, --kind, --source, --project-id | bin/harnessmesh knowledge import --file transcript.md --source codex --project-id demo |
| knowledge remember | --text oder --file, --kind, --source, --project-id | bin/harnessmesh knowledge remember --text "Decision: use WAL" --kind decision --source human |
| knowledge stats | keine | bin/harnessmesh knowledge stats |
| knowledge verify | keine | bin/harnessmesh knowledge verify |
| knowledge index | keine | bin/harnessmesh knowledge index |
| knowledge export | --file | bin/harnessmesh knowledge export --file /backup/knowledge.hmkz |
| knowledge restore | --file | bin/harnessmesh knowledge restore --file /backup/knowledge.hmkz |
| knowledge rotate-key | --key | bin/harnessmesh knowledge rotate-key --key "$NEW_KNOWLEDGE_KEY" |
| knowledge watch | --file, --interval, --project-id, --source | bin/harnessmesh knowledge watch --file conversation.md --interval 2s |
| knowledge compact | --before RFC3339 | bin/harnessmesh knowledge compact --before 2025-01-01T00:00:00Z |

### Provider, Routing und Tests

| Befehl | Wichtige Optionen | Beispiel |
| :--- | :--- | :--- |
| provider auth chatgpt | --token-path, --timeout | bin/harnessmesh provider auth chatgpt --timeout 10m |
| provider doctor | --config | bin/harnessmesh provider doctor --config harnessmesh.json |
| provider serve | --config, --listen, --token, --metadata-only | bin/harnessmesh provider serve --config harnessmesh.json --listen 127.0.0.1:8789 --token "$HARNESSMESH_PROVIDER_TOKEN" |
| switchyard doctor | --config | bin/harnessmesh switchyard doctor --config harnessmesh.json |
| switchyard routes | --config | bin/harnessmesh switchyard routes --config harnessmesh.json |
| switchyard config validate | --config | bin/harnessmesh switchyard config validate --config harnessmesh.json |
| smoke-test antigravity-codex | --config, --repo | bin/harnessmesh smoke-test antigravity-codex --config configs/antigravity-openai-peer.json --repo . |

### Sicherheits- und Betriebs-Hinweise

- mcp serve, bridge serve und provider serve bleiben aktiv, bis sie mit
  Ctrl-C beendet werden.
- Tokens über Umgebungsvariablen setzen; sie nicht in Git, Shell-History oder
  Konfigurationsdateien einchecken.
- change commit, knowledge restore, knowledge rotate-key und
  Integrationsbefehle können Dateien oder den Git-Stand verändern. Für
  Integrationen zuerst --dry-run oder --check verwenden.
- knowledge compact löscht Archivdatensätze vor dem angegebenen Zeitpunkt.
- change commit nur nach erfolgreichem change gate ausführen.

## CLI Cookbook

Die folgenden Beispiele verwenden das beim Build erzeugte Binary
bin/harnessmesh. Nach make build kann alternativ überall einfach
harnessmesh verwendet werden. Werte in spitzen Klammern sind Platzhalter.

### Start, Diagnose und Konfiguration

```bash
make build
bin/harnessmesh version
bin/harnessmesh doctor --config configs/antigravity-openai-peer.json
bin/harnessmesh print-config --config harnessmesh.json
bin/harnessmesh config validate --config harnessmesh.json
bin/harnessmesh config migrate harnessmesh.v1.json harnessmesh.v2.json
bin/harnessmesh config print --config harnessmesh.json
```

### Klassischer Collaborate-Workflow

```bash
bin/harnessmesh collaborate \
  --repo . --config configs/antigravity-openai-peer.json \
  --task "Prüfe die Authentifizierung und schlage sichere Verbesserungen vor"

bin/harnessmesh collaborate \
  --task-file ./task.md --executor antigravity --reviewer codex \
  --max-rounds 3 --dry-run
```

### MCP und lokale/remote Bridges

```bash
# Projektbezogene MCP-Konfiguration
bin/harnessmesh mcp install claude --scope project
bin/harnessmesh mcp install codex --scope project
bin/harnessmesh mcp install antigravity --scope project
bin/harnessmesh mcp install copilot --scope project

# MCP für neue Projekte im Benutzerprofil
bin/harnessmesh mcp install claude --scope user

# MCP über stdio
bin/harnessmesh mcp serve --repo . --config harnessmesh.json --caller claude

# Streamable HTTP für einen Remote-Connector
export HARNESSMESH_MCP_TOKEN="<long-random-token>"
bin/harnessmesh mcp serve \
  --repo . --config harnessmesh.json --caller chatgpt-browser \
  --listen 127.0.0.1:8787 --token "$HARNESSMESH_MCP_TOKEN"

# VS-Code-Bridge für REST/WebSocket
export HARNESSMESH_BRIDGE_TOKEN="<long-random-token>"
bin/harnessmesh bridge serve \
  --repo . --config harnessmesh.json --caller antigravity \
  --listen 127.0.0.1:8788 --token "$HARNESSMESH_BRIDGE_TOKEN"
```

### Integrationen

```bash
bin/harnessmesh integrate antigravity \
  --repo . --config configs/antigravity-openai-peer.json

bin/harnessmesh integrate chatgpt \
  --repo . --config configs/chatgpt-claude.json \
  --listen 127.0.0.1:8787 --dry-run

bin/harnessmesh integrate codex-provider \
  --scope user --listen 127.0.0.1:8789 \
  --model harnessmesh-local --dry-run
```

### Peer-Kommunikation und Reviews

```bash
bin/harnessmesh peer converse \
  --peer codex --message "Prüfe diesen API-Entwurf auf Risiken" \
  --outcome security_analysis --config harnessmesh.json

bin/harnessmesh peer converse \
  --capability security-review --message "Bewerte die Authentifizierungsänderung" --json

bin/harnessmesh peer ask \
  --peer codex --question "Welche Regressionen erkennst du in diesem Diff?" \
  --repo . --config harnessmesh.json --json

bin/harnessmesh peer review \
  --peer codex --focus security,tests,backwards-compatibility \
  --repo . --config harnessmesh.json --json

bin/harnessmesh peer status --session <SESSION_ID> --json
```

### Sessions, Findings und Evidence

```bash
bin/harnessmesh session list
bin/harnessmesh session show <SESSION_ID>
bin/harnessmesh session messages <SESSION_ID>
bin/harnessmesh session resume <SESSION_ID>
bin/harnessmesh session stop <SESSION_ID>
bin/harnessmesh findings <SESSION_ID>
bin/harnessmesh findings <SESSION_ID> --json
bin/harnessmesh evidence <SESSION_ID>
bin/harnessmesh evidence <SESSION_ID> --json
```

### Persistente Collaboration Spaces, Channels und Threads

```bash
bin/harnessmesh space create \
  --id project-review --title "Project Review" \
  --purpose "Sicherheits- und Architekturprüfung" \
  --writer antigravity --config harnessmesh.json
bin/harnessmesh space list --json
bin/harnessmesh space show <SPACE_ID> --json
bin/harnessmesh space pause <SPACE_ID>
bin/harnessmesh space resume <SPACE_ID>
bin/harnessmesh space stop <SPACE_ID>

bin/harnessmesh channel list --space <SPACE_ID> --json
bin/harnessmesh channel create --space <SPACE_ID> \
  --name security --description "Security findings and decisions"

bin/harnessmesh thread list --space <SPACE_ID> --channel security --json
bin/harnessmesh thread show <THREAD_ID>
bin/harnessmesh thread reply \
  --space <SPACE_ID> --channel security --thread <THREAD_ID> \
  --from human --message "Bitte mit einem reproduzierbaren Test belegen."

bin/harnessmesh inbox list --space <SPACE_ID> --participant codex --unread --json
bin/harnessmesh subscriptions list --space <SPACE_ID>
bin/harnessmesh subscriptions add \
  --space <SPACE_ID> --participant codex \
  --channels security,findings --events repository.changed,decision.* --mode active
bin/harnessmesh subscriptions remove --space <SPACE_ID> --id <SUBSCRIPTION_ID>
```

### Decisions

```bash
bin/harnessmesh decide propose \
  --space <SPACE_ID> --title "SQLite als Default-Store" \
  --statement "SQLite bleibt der lokale Standard für Sessions." \
  --rationale "Keine externe Infrastruktur für den lokalen Betrieb nötig."
bin/harnessmesh decide list --space <SPACE_ID> --json
bin/harnessmesh decide accept --space <SPACE_ID> --decision <DECISION_ID> --from human
```

### Change Transactions und Proof Gates

change commit schreibt bewusst in das Git-Repository. Die Schritte davor
ermöglichen einen überprüfbaren, konservativen Ablauf.

```bash
bin/harnessmesh change create \
  --title "Provider-Timeout verbessern" \
  --intent "Transient errors sauber retryen" --author operator --json
bin/harnessmesh change list --status draft --json
bin/harnessmesh change show <CHANGE_ID> --json
bin/harnessmesh change prepare <CHANGE_ID>
bin/harnessmesh change verify <CHANGE_ID> --json
bin/harnessmesh change verify <CHANGE_ID> --obligation <OBLIGATION_ID>
bin/harnessmesh change gate <CHANGE_ID> --json
bin/harnessmesh change evidence <CHANGE_ID> \
  --obligation <OBLIGATION_ID> --type security_audit --result passed \
  --source operator --notes "Peer-Review und Regressionstest bestanden" --json
bin/harnessmesh change abort <CHANGE_ID> --reason "Anforderung geändert"
bin/harnessmesh change commit <CHANGE_ID> \
  --author operator --message "Improve provider timeout handling" --json
```

### Knowledge Archive

```bash
bin/harnessmesh knowledge search \
  --query "sqlite migration rollback" --project-id harnessmesh \
  --kind decision --json
bin/harnessmesh knowledge import \
  --file conversation.md --source claude-code \
  --project-id harnessmesh --kind transcript
bin/harnessmesh knowledge remember \
  --text "Provider-Timeouts müssen fail-closed behandelt werden." \
  --kind lesson --source operator
bin/harnessmesh knowledge stats
bin/harnessmesh knowledge verify
bin/harnessmesh knowledge index
bin/harnessmesh knowledge export --file /backup/knowledge.hmkz
bin/harnessmesh knowledge restore --file /backup/knowledge.hmkz
export NEW_KNOWLEDGE_KEY="<new-secret>"
bin/harnessmesh knowledge rotate-key --key "$NEW_KNOWLEDGE_KEY"
bin/harnessmesh knowledge watch \
  --file conversation.md --project-id harnessmesh --interval 2s --source codex
bin/harnessmesh knowledge compact --before 2025-01-01T00:00:00Z
```

### Provider Gateway und Switchyard

```bash
bin/harnessmesh provider auth chatgpt
bin/harnessmesh provider doctor --config harnessmesh.json
export HARNESSMESH_PROVIDER_TOKEN="<provider-token>"
bin/harnessmesh provider serve \
  --config harnessmesh.json --listen 127.0.0.1:8789 \
  --token "$HARNESSMESH_PROVIDER_TOKEN"

bin/harnessmesh switchyard config validate --config harnessmesh.json
bin/harnessmesh switchyard routes --config harnessmesh.json
bin/harnessmesh switchyard doctor --config harnessmesh.json
```

### Smoke Test und Hilfe

```bash
bin/harnessmesh smoke-test antigravity-codex \
  --config configs/antigravity-openai-peer.json --repo .
bin/harnessmesh --help
```

## License

Apache 2.0. See [LICENSE](LICENSE) for details.
