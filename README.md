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
    ├── Model Context Protocol (MCP) Server (10 Tools)
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

### 2. Standards-Compliant MCP Server (20 Tools)
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

---

## License

Apache 2.0. See [LICENSE](LICENSE) for details.
