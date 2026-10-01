# HarnessMesh

**A persistent, evidence-driven collaboration fabric and change-control plane for AI coding harnesses.**

HarnessMesh connects coding agents such as **Claude Code**, **OpenAI Codex**, **Google Antigravity**, **GitHub Copilot CLI**, ChatGPT-connected MCP clients, and custom adapters so they can collaborate through a shared, durable control plane instead of relying on human copy/paste between tools.

> HarnessMesh is an **agent collaboration plane**. It coordinates participants, context, evidence, decisions, durable knowledge, operational controls, and verified code changes. It is not a general-purpose LLM proxy and it does not replace a model router such as NVIDIA NeMo Switchyard.

## Why HarnessMesh

Typical multi-agent coding workflows break down at the boundaries between tools: context is copied manually, reviewers cannot challenge one another with durable evidence, concurrent writers corrupt worktrees, prior decisions disappear between sessions, and model/provider failures are handled ad hoc.

HarnessMesh provides a single control plane for those concerns:

```text
                                  ┌─────────────────────┐
                                  │   Human / CI / IDE  │
                                  └──────────┬──────────┘
                                             │
                                             v
┌────────────────┐                  ┌─────────────────────┐                  ┌────────────────┐
│  Claude Code   │<────────────────>│                     │<────────────────>│  OpenAI Codex  │
└────────────────┘                  │     HarnessMesh     │                  └────────────────┘
                                    │                     │
┌────────────────┐                  │ Collaboration Plane │                  ┌────────────────┐
│  Antigravity   │<────────────────>│ + Evidence Control  │<────────────────>│ Copilot CLI    │
└────────────────┘                  │ + Knowledge Archive │                  └────────────────┘
                                    │ + MeshCommit        │
┌────────────────┐                  │                     │                  ┌────────────────┐
│ ChatGPT / MCP  │<────────────────>│                     │<────────────────>│ Custom Agents  │
└────────────────┘                  └──────────┬──────────┘                  └────────────────┘
                                             │
                           optional model routing boundary
                                             │
                                             v
                                  ┌─────────────────────┐
                                  │ NVIDIA NeMo         │
                                  │ Switchyard / Router │
                                  └─────────────────────┘
```

## Current Version

The public `main` branch is currently **HarnessMesh v0.5.0**.

## Capability Map

| Area | What HarnessMesh provides |
| --- | --- |
| **Multi-agent collaboration** | Persistent spaces, channels, causal threads, mentions, subscriptions, inboxes, participant modes, peer conversations, parallel reviews, findings, evidence, challenges, and decisions |
| **Agent adapters** | First-class adapters for Claude Code, OpenAI Codex, Google Antigravity, GitHub Copilot CLI; deterministic fake adapter for tests; extensible adapter registry |
| **Capability routing** | Dynamic peer discovery and routing by declared capabilities instead of hard-coded agent names |
| **Evidence-driven review** | Structured findings, reproducible evidence, formal challenges, evidence-backed resolution, and conservative deduplication; no majority-vote shortcut |
| **MeshCommit change control** | Evidence-gated change transactions, proof obligations, tree fingerprints, stale-evidence invalidation, independent-review rules, deterministic gates, and TOCTOU-safe commit binding |
| **Single-writer safety** | Exactly one writable participant per workspace; reviewers and peers can be constrained to read-only operation |
| **Context protection** | Bounded repository projections, sensitive-file filtering, path traversal rejection, symlink-escape protection, and sandbox command/working-directory policies |
| **Persistent knowledge** | Compressed append-only knowledge archive, hybrid search, RAG context, summaries, quality signals, explicit memory, import, retention, verification, indexing, backup/restore, key rotation, and file watching |
| **Durable state** | SQLite/WAL persistence for sessions, spaces, threads, findings, evidence, decisions, routing decisions, retries, dead letters, health state, approvals, and MeshCommit transactions |
| **Operational resilience** | Retry outbox, priority, dead-letter recovery, quota-wait scheduling, circuit breakers, health tracking, budgets, human approvals, idempotency, and operational metrics |
| **MCP** | Standards-based MCP over stdio and authenticated Streamable HTTP, including SSE, sessions, bearer auth, OAuth2 introspection, CORS, TLS, ACLs, rate limiting, metrics, and admin endpoints |
| **Model-routing boundary** | Fixed routing, opaque external routing, and optional NVIDIA NeMo Switchyard integration while keeping participant selection separate from underlying model selection |
| **Observability & operations** | Health checks, Prometheus-style metrics, admin status endpoints, session transcripts, routing audit records, operational status, and optional OTLP HTTP export integration |
| **Release engineering** | Race tests, fuzz seed coverage, load benchmarks, vulnerability scanning, CodeQL, Docker builds, SBOM generation, checksums, and keyless Cosign signing for tagged releases |

---

# Core Functionality

## 1. Persistent Collaboration Spaces

HarnessMesh models collaboration as a durable workspace rather than a sequence of isolated prompts.

A collaboration space supports:

- lifecycle states such as `active`, `paused`, `stopped`, and archived persistence;
- a designated writer participant;
- multiple managed or external participants;
- predefined channels: `#general`, `#architecture`, `#security`, `#testing`, `#findings`, and `#decisions`;
- custom channels;
- causal threads with `root_id` / `parent_id` relationships;
- participant read cursors;
- durable inboxes for offline/passive participants;
- participant modes: `active`, `passive`, `on_demand`, and `paused`;
- channel/event subscriptions and scope patterns;
- formal, evidence-linked engineering decisions;
- human pause, resume, and emergency stop controls.

Rapid repository/event bursts can be coalesced through the event bus into semantic events such as `repository.changed`, reducing duplicate downstream work.

## 2. Peer-to-Peer Agent Collaboration

Participants can collaborate directly without routing every interaction through the human operator.

HarnessMesh supports:

- persistent multi-turn peer conversations;
- targeted questions and inspections;
- capability-based peer discovery;
- causal replies;
- single or parallel multi-peer review;
- bounded concurrency;
- structured findings with severity/category/location;
- evidence submission from tests, diffs, compilers, linters, or other reproducible sources;
- formal challenges to findings;
- evidence-driven resolution;
- conservative duplicate/related-finding handling;
- session status, call counts, and collaboration budgets;
- idempotency keys for expensive peer operations.

### Evidence, not majority voting

HarnessMesh does not resolve disagreements by counting model votes or assuming one model is authoritative:

```text
Claim
  -> repository evidence
  -> reproducer / test / compiler / static analysis
  -> challenge if necessary
  -> evidence-backed resolution
```

Unresolved high-impact findings remain unresolved until evidence closes them.

## 3. Agent Capability Model

Every configured participant can declare capabilities such as:

- `read_repository`
- `write_repository`
- `run_commands`
- `review`
- `answer_questions`
- `submit_evidence`

Peers can be discovered dynamically through `peer.capabilities`, and operations may route by capability instead of by a fixed participant name.

This makes the collaboration layer independent from any one vendor or agent implementation.

## 4. First-Class Adapters

| Harness | Typical use | Invocation model |
| --- | --- | --- |
| **Claude Code** | Executor, reviewer, peer | Headless Claude Code integration |
| **OpenAI Codex** | Reviewer, executor, peer | Codex CLI with structured output handling |
| **Google Antigravity** | Executor, reviewer, peer | Antigravity headless integration plus IDE MCP setup |
| **GitHub Copilot CLI** | Executor, reviewer, peer | Non-interactive Copilot CLI integration |
| **External MCP participant** | Browser/remote/externally managed participant | Passive/external participant through MCP |
| **Fake adapter** | Deterministic CI and integration testing | In-memory deterministic execution |

Adapters are registered dynamically through the common Harness abstraction. New adapters can be added without rewriting collaboration-domain logic.

## 5. Managed vs. External Participants

HarnessMesh distinguishes execution ownership:

- **managed** participants may be invoked as local/headless harness processes by HarnessMesh;
- **external** participants are not autonomously started as subprocesses and instead participate through MCP, mentions, pending inbox delivery, and external client sessions.

This enables browser/remote clients such as ChatGPT-connected MCP sessions without pretending that an external client is a locally managed executable.

## 6. Economy-Aware Participant Selection

HarnessMesh can select a suitable **participant** independently from the underlying model/provider.

The `cheapest_suitable` policy supports:

- task tiers: trivial, routine, complex, critical;
- capability filtering;
- privacy/sensitive-path filtering;
- local-participant preference where required;
- single-writer enforcement;
- relative-cost ranking;
- session affinity;
- bounded escalation after failed edits/builds/verifications;
- de-escalation after successful resolution;
- durable routing-decision audit records in SQLite.

The architectural boundary is deliberate:

```text
HarnessMesh            selects the participant / harness
Switchyard (optional)  selects the underlying model / provider
```

HarnessMesh does not second-guess model choices owned by Switchyard or opaque external clients.

---

# MeshCommit: Evidence-Gated Agentic Change Control

HarnessMesh v0.5.0 adds **MeshCommit**, a deterministic change-control plane for agent-authored repository changes.

## Change lifecycle

```text
 draft
   |
   v
 prepared
   |
   v
 under_verification
   |\
   | +--> blocked --------+
   |                      |
   +--> committable <-----+
            |
            v
        committed

 aborted  <- terminal exit from any non-committed state
```

A `MeshChange` tracks:

- base commit and current working-tree fingerprint;
- affected paths and content hashes;
- policy-locked proof obligations;
- evidence tied to an exact tree hash;
- stale-evidence invalidation when relevant paths change;
- independent review constraints and self-review protection;
- deterministic ChangeGate evaluation without an LLM in the gate;
- verified tree hash;
- final commit SHA;
- TOCTOU protection that prevents committing a tree different from the verified tree.

### MeshCommit CLI

```bash
harnessmesh change create --title "Add user auth" --intent "Support bearer tokens"
harnessmesh change list
harnessmesh change show <change_id>
harnessmesh change prepare <change_id>
harnessmesh change verify <change_id>
harnessmesh change evidence <change_id> --obligation <id> --type peer_review --result passed --source claude
harnessmesh change gate <change_id>
harnessmesh change commit <change_id> --message "Add user auth"
harnessmesh change abort <change_id> --reason "Superseded"
```

---

# Knowledge & Cross-Session Memory

HarnessMesh persists operational state in SQLite and historical knowledge in a separate compressed archive.

## Compressed Knowledge Archive

By default:

```text
~/.harnessmesh/harnessmesh.db
~/.harnessmesh/knowledge.hmkz
```

The `.hmkz` archive is an append-only stream of framed, block-compressed NDJSON using Zstandard. Records are flushed in bounded blocks so large histories do not require loading the complete archive into memory.

Stored knowledge can include:

- messages and conversations;
- collaboration events;
- findings and challenges;
- evidence;
- engineering decisions;
- explicit lessons/problems/solutions;
- source, project, repository, branch, commit, agent, model, tags, confidence, and sensitivity metadata.

### Knowledge features

- bounded relevance-ranked search;
- persistent block index;
- deterministic local vector-similarity fallback;
- project/session/space/kind filtering;
- verified-only and confidence filtering;
- bounded RAG context generation;
- deterministic summaries;
- quality analysis for verification, confidence, expiry, duplicates, and conflicts;
- explicit `remember` records;
- transcript import;
- retention compaction;
- archive verification;
- index rebuild;
- atomic export/backup and restore;
- encryption-key rotation;
- polling-based transcript file watch/import;
- optional provider-generated vector embeddings through the embedding-provider interface.

Common credentials are redacted before archive persistence. Set `HARNESSMESH_KNOWLEDGE_KEY` to enable AES-256-GCM encryption at rest.

### Knowledge CLI

```bash
harnessmesh knowledge search --query "sqlite migration rollback" --project-id my-project --json
harnessmesh knowledge import --file conversation.md --source claude-code --project-id my-project
harnessmesh knowledge remember --text "Use WAL for concurrent readers" --kind lesson
harnessmesh knowledge stats
harnessmesh knowledge compact --before 2026-01-01T00:00:00Z
harnessmesh knowledge verify
harnessmesh knowledge index
harnessmesh knowledge export --file /backup/knowledge.hmkz
harnessmesh knowledge restore --file /backup/knowledge.hmkz
harnessmesh knowledge rotate-key --key "$NEW_KNOWLEDGE_KEY"
harnessmesh knowledge watch --file conversation.md --project-id my-project
```

---

# Resilience, Quotas & Human Control

HarnessMesh includes durable operational controls for long-running agent workflows.

## Delivery resilience

Failed participant deliveries can be persisted in a retry outbox with:

- attempt count;
- error information;
- retry timestamp;
- priority;
- automatic transient retry;
- permanent dead-letter storage after exhaustion/non-retryable failure;
- operator requeue support.

## Provider quota waits

Normalized errors such as `usage limit`, `credits exhausted`, `quota exceeded`, explicit reset timestamps, or `try again in ...` can be classified as quota waits.

Quota waits:

- keep work pending;
- schedule resumption for the provider reset time;
- do not consume a normal retry attempt;
- fall back to a configurable wait when no reset time is available.

This does **not** bypass provider limits; it only makes the workflow durable across them.

## Budgets, approvals and circuit breakers

Operational controls include:

- global token budgets;
- global cost budgets;
- approval thresholds for expensive/sensitive actions;
- human approval/rejection records;
- agent health tracking;
- persisted circuit-breaker state;
- configurable failure thresholds and cooldowns;
- operational metrics.

Example:

```json
{
  "collaboration": {
    "global_max_tokens": 200000,
    "global_max_cost_usd": 10,
    "approval_cost_usd": 1,
    "circuit_breaker_failures": 3,
    "circuit_breaker_cooldown": "2m",
    "quota_fallback_wait": "15m"
  }
}
```

---

# MCP Interface

HarnessMesh exposes **42 MCP tools** on the current v0.5.0 public `main` branch.

## Peer tools — 11

| Tool | Purpose |
| --- | --- |
| `peer.list` | List known participants, adapter types, roles and status |
| `peer.capabilities` | Discover peers by capability/role |
| `peer.ask` | Ask a peer for targeted help or inspection |
| `peer.request_review` | Request single or parallel structured review |
| `peer.submit_finding` | Publish a structured finding |
| `peer.submit_evidence` | Attach reproducible evidence |
| `peer.challenge` | Challenge a finding and mark it disputed |
| `peer.resolve` | Resolve a finding with evidence-backed rationale |
| `peer.reply` | Send a causal structured reply |
| `peer.converse` | Maintain an interactive multi-turn peer conversation |
| `peer.status` | Inspect session, finding counts and budget/round state |

## Collaboration-space tools — 9

| Tool | Purpose |
| --- | --- |
| `collaboration.publish` | Publish a message/proposal/question to a channel/thread |
| `collaboration.reply` | Reply inside an existing thread |
| `collaboration.inbox` | Retrieve participant messages, mentions and notifications |
| `collaboration.channels` | Discover channels |
| `collaboration.thread` | Retrieve a thread transcript |
| `collaboration.subscribe` | Subscribe to channels/events/scope patterns |
| `collaboration.unsubscribe` | Remove a subscription |
| `collaboration.decide` | Propose or accept an evidence-backed decision |
| `collaboration.status` | Inspect collaboration-space state |

## Knowledge tools — 9

| Tool | Purpose |
| --- | --- |
| `knowledge.search` | Search durable historical knowledge |
| `knowledge.context` | Build bounded RAG-ready context |
| `knowledge.stats` | Inspect archive statistics |
| `knowledge.remember` | Store an explicit durable lesson/decision/problem/solution |
| `knowledge.import` | Import external transcript/content |
| `knowledge.compact` | Apply retention by timestamp |
| `knowledge.summary` | Produce bounded evidence-oriented historical summary |
| `knowledge.quality` | Inspect verification/confidence/expiry/duplicate/conflict signals |
| `knowledge.search_advanced` | Search with time/source/agent metadata filters |

## Operations tools — 5

| Tool | Purpose |
| --- | --- |
| `operations.status` | Agent health, quota waits, circuit breakers, retry backlog and metrics |
| `operations.approvals` | List approval requests |
| `operations.approve` | Approve/reject a protected operation |
| `operations.dead_letters` | List permanently failed deliveries |
| `operations.requeue_dead_letter` | Requeue a dead-letter item with optional priority |

## MeshCommit tools — 8

| Tool | Purpose |
| --- | --- |
| `change.create` | Create an evidence-gated change transaction |
| `change.prepare` | Prepare/re-fingerprint working-tree state |
| `change.status` | Inspect state, hashes, obligations, evidence and gate result |
| `change.diff` | Inspect affected paths and content hashes |
| `change.verify` | Execute an automated proof obligation |
| `change.evidence` | Submit structured verification evidence |
| `change.abort` | Abort an in-flight change |
| `change.commit_status` | Check committability and missing/failed obligations |

> `harnessmesh change commit` is intentionally a CLI-controlled commit operation after the deterministic gate; it is not exposed as an unrestricted MCP tool.

---

# MCP Transports & Remote Access

## Stdio

Local harnesses can launch HarnessMesh as an MCP server over standard input/output.

```bash
harnessmesh mcp serve --repo . --caller claude
```

## Authenticated HTTP

Remote clients can use authenticated JSON-RPC / MCP over HTTP.

```bash
export HARNESSMESH_MCP_TOKEN="$(openssl rand -hex 32)"
harnessmesh mcp serve \
  --listen 127.0.0.1:8787 \
  --token "$HARNESSMESH_MCP_TOKEN" \
  --caller remote-agent
```

The server provides:

- legacy JSON-RPC `POST /` support;
- Streamable HTTP MCP on `/mcp`;
- POST/GET/DELETE/OPTIONS handling;
- SSE responses;
- MCP session IDs and protocol-version negotiation;
- bearer-token authentication with constant-time token comparison;
- optional OAuth2 token introspection via `HARNESSMESH_MCP_OAUTH_INTROSPECTION_URL`;
- optional introspection client secret via `HARNESSMESH_MCP_OAUTH_CLIENT_SECRET`;
- strict CORS handling with configurable allowlisted origins;
- native TLS when certificate/key flags are supplied;
- request body limits;
- request-rate limits (`HARNESSMESH_MCP_RATE_LIMIT`, default 120/minute);
- project ACLs (`HARNESSMESH_MCP_PROJECTS`);
- caller ACLs (`HARNESSMESH_MCP_CALLERS`);
- admin-caller ACLs (`HARNESSMESH_MCP_ADMIN_CALLERS`);
- security headers;
- graceful server shutdown.

Operational endpoints include:

```text
GET /healthz            liveness
GET /metrics            Prometheus-style metrics
GET /admin              authenticated operations UI
GET /admin/knowledge    authenticated knowledge statistics
GET /admin/operations   authenticated health/retry/dead-letter/metrics state
```

---

# Integrations

## Install MCP for local coding harnesses

```bash
# Claude Code
harnessmesh mcp install claude --scope project
harnessmesh mcp install claude --scope user

# OpenAI Codex
harnessmesh mcp install codex --scope project

# Google Antigravity
harnessmesh mcp install antigravity --scope project
harnessmesh mcp install antigravity --scope user

# GitHub Copilot CLI
harnessmesh mcp install copilot --scope project
harnessmesh mcp install copilot --scope user
```

## Full Antigravity integration

```bash
harnessmesh integrate antigravity \
  --repo . \
  --config configs/antigravity-openai-peer.json
```

This installs/merges the MCP configuration and the HarnessMesh collaboration rule used by Antigravity.

## ChatGPT / external MCP integration

HarnessMesh can expose the Streamable HTTP MCP endpoint for an external ChatGPT-compatible MCP client:

```bash
harnessmesh integrate chatgpt \
  --repo . \
  --config configs/chatgpt-claude.json \
  --listen 127.0.0.1:8787
```

The integration command prepares instructions and a bearer token, and can also configure Claude Code for the same repository. A locally bound HTTP service must be exposed through an appropriately secured network path/tunnel before a remote web client can reach it.

Plain ChatGPT conversations that are not connected to HarnessMesh cannot access the local knowledge archive automatically.

---

# Security Model

HarnessMesh deliberately constrains agent collaboration instead of assuming every participant is trusted.

## Workspace and context controls

- exactly one writable participant per workspace;
- read-only reviewer/peer operation where configured;
- path traversal rejection;
- symlink escape rejection outside repository root;
- sensitive file filtering for `.env`, private keys, credentials, Terraform state, `.git`, and related secret-bearing paths;
- bounded context projection rather than forwarding complete raw transcripts;
- command and working-directory allowlists through sandbox policy helpers.

## Identity and MCP controls

- authenticated remote MCP;
- anti-spoofing checks for caller/source/challenger/resolver identity;
- constant-time bearer token comparison;
- OAuth2 introspection option;
- caller/project/admin ACLs;
- rate limiting;
- body-size bounds;
- security response headers;
- CORS restrictions;
- optional TLS.

## Change-control controls

- evidence tied to tree hashes;
- policy-locked proof obligations;
- path-scoped evidence invalidation;
- self-review/independent-review constraints;
- deterministic, non-LLM commit gate;
- TOCTOU protection between verified tree and committed tree.

---

# Persistence

SQLite is the default transactional backend and uses WAL journaling, foreign keys, busy timeouts, and versioned migrations.

Persisted domains include:

- collaboration sessions and messages;
- spaces, channels, threads and subscriptions;
- participant state and inbox delivery;
- findings, evidence, challenges and decisions;
- routing decisions and usage records;
- retry queue and dead letters;
- health/circuit-breaker state;
- human approvals;
- MeshChange transactions, paths, proof obligations, evidence and gate results.

The storage layer exposes a backend-factory seam for alternate deployments. SQLite is the fully supported built-in default; references to future PostgreSQL/object-storage deployments describe the extension seam rather than a bundled PostgreSQL implementation.

---

# Model Routing

HarnessMesh supports three routing-backend modes:

| Backend | Behavior |
| --- | --- |
| `fixed` | Direct/unproxied participant model configuration |
| `external` | Model routing is owned by an external/opaque client such as Copilot |
| `switchyard` | Optional NVIDIA NeMo Switchyard backend with health checks and route metadata |

Inspect routing configuration with:

```bash
harnessmesh switchyard doctor --config harnessmesh.json
harnessmesh switchyard routes --config harnessmesh.json
harnessmesh switchyard config validate --config harnessmesh.json
```

---

# CLI Reference

The current top-level command surface is:

```text
harnessmesh collaborate --task "..." [options]
harnessmesh space <list|show|create|pause|resume|stop> [options]
harnessmesh channel <list|create|show> [options]
harnessmesh thread <list|show|reply> [options]
harnessmesh inbox list --space <id> [options]
harnessmesh subscriptions <list|add|remove> [options]
harnessmesh decide <list|propose|accept> [options]
harnessmesh change <create|list|show|prepare|verify|evidence|gate|commit|abort> [options]
harnessmesh integrate <antigravity|chatgpt> [options]
harnessmesh mcp serve [options]
harnessmesh mcp install <claude|codex|antigravity|copilot> [--scope <project|user>]
harnessmesh peer converse --message "..." [options]
harnessmesh peer ask --peer <name> --question "..." [options]
harnessmesh peer review --peer <name> [options]
harnessmesh peer status --session <id>
harnessmesh agents <list|show <name>>
harnessmesh session <list|show|messages|resume|stop> [options]
harnessmesh findings <session-id> [--json]
harnessmesh evidence <session-id> [--json]
harnessmesh knowledge <search|import|remember|stats|compact|verify|index|export|restore|rotate-key|watch> [options]
harnessmesh config <validate|migrate|print> [options]
harnessmesh switchyard <doctor|routes|config validate> [options]
harnessmesh smoke-test antigravity-codex [options]
harnessmesh doctor [--config <path>]
harnessmesh print-config [--config <path>]
harnessmesh version
```

## Session inspection

```bash
harnessmesh session list
harnessmesh session show <session-id>
harnessmesh session messages <session-id>
harnessmesh session resume <session-id>
harnessmesh session stop <session-id>
harnessmesh findings <session-id>
harnessmesh evidence <session-id>
```

Session transcripts preserve causal IDs, status and duration metadata so peer interactions can be audited after the fact.

## Configuration management

```bash
harnessmesh config validate harnessmesh.json
harnessmesh config migrate old-v1.json new-v2.json
harnessmesh config print --config harnessmesh.json
harnessmesh print-config --config harnessmesh.json
```

Printed configuration is redacted before output.

## Diagnostics

```bash
harnessmesh doctor --config configs/antigravity-openai-peer.json
harnessmesh smoke-test antigravity-codex --config configs/antigravity-openai-peer.json
```

`doctor` checks configured harness health, relevant authentication state, model-routing backends, Git availability and the SQLite store, and reports Antigravity integration state when applicable.

---

# Quick Start

## Prerequisites

- Go 1.23+
- Git
- the CLI(s) for the harness adapters you intend to use

## Build

```bash
git clone https://github.com/domehahn/harnessmesh.git
cd harnessmesh
make build
./bin/harnessmesh version
./bin/harnessmesh doctor
```

## Pick a profile

Ready-to-use examples include:

```text
configs/claude-codex.json
configs/antigravity-codex.json
configs/copilot-codex.json
configs/antigravity-multi-review.json
configs/codex-multi-review.json
configs/antigravity-openai-peer.json
configs/chatgpt-claude.json
configs/harnessmesh.example.json
configs/harnessmesh.switchyard.example.json
configs/no-switchyard-example.json
configs/switchyard-example.json
```

## Run a collaboration task

```bash
./bin/harnessmesh collaborate \
  --task "Review the authentication changes and verify the test coverage" \
  --config configs/claude-codex.json \
  --repo .
```

## Or start MCP

```bash
./bin/harnessmesh mcp serve \
  --repo . \
  --config configs/claude-codex.json \
  --caller claude
```

---

# Validation, CI & Release Engineering

Local validation targets:

```bash
make test
make vet
make race
make fuzz
make load
make security
make build
```

The repository also includes CI/release automation for:

- race-enabled Go tests;
- knowledge/archive benchmark coverage;
- `go vet`;
- `govulncheck`;
- CodeQL;
- Docker builds;
- tagged-release binaries;
- SHA-256 checksums;
- CycloneDX SBOM generation;
- keyless Cosign signing;
- GitHub release publication.

---

# Design Invariants

HarnessMesh is built around a small set of non-negotiable invariants:

1. **One writer, many reviewers.** Concurrent writers are rejected rather than reconciled after corruption.
2. **Evidence beats model consensus.** Technical disagreement is resolved using repository evidence and reproducible proofs.
3. **Context is projected, not blindly copied.** Only bounded, policy-filtered repository context is shared.
4. **Loops are bounded.** Causal cycle detection, peer depth limits, call limits and budgets prevent uncontrolled agent recursion.
5. **Routing boundaries stay explicit.** HarnessMesh selects participants; model routers select models.
6. **External means external.** Browser/remote participants are not silently treated as managed local subprocesses.
7. **Verified code must be the code that is committed.** MeshCommit binds the commit gate to the verified tree hash.
8. **History is durable.** Operational state belongs in SQLite; long-term collaboration knowledge belongs in the append-only knowledge archive.

---

# Documentation

## Architecture & protocols

- [Architecture](docs/ARCHITECTURE.md)
- [Protocol](docs/PROTOCOL.md)
- [Model Context Protocol server](docs/mcp.md)
- [Peer protocol](docs/peer-protocol.md)
- [Capability discovery](docs/capabilities.md)
- [Economy & participant routing](docs/economy-routing.md)
- [MeshChange transactions](docs/change-transactions.md)

## Collaboration

- [Collaboration spaces](docs/collaboration-spaces.md)
- [Channels & threads](docs/channels-and-threads.md)
- [Events & coalescing](docs/events-and-coalescing.md)
- [Inbox & activation](docs/inbox-and-activation.md)
- [Decisions & evidence](docs/decisions-and-evidence.md)
- [Human controls](docs/human-controls.md)
- [Collaboration patterns](docs/collaboration-patterns.md)

## Integration & security

- [Adapters](docs/adapters.md)
- [Antigravity integration](docs/antigravity-integration.md)
- [Context projection](docs/context-projection.md)
- [Security](docs/security.md)
- [Sessions & persistence](docs/sessions.md)
- [Troubleshooting](docs/troubleshooting.md)

## Project

- [Changelog](CHANGELOG.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Governance](GOVERNANCE.md)
- [Support](SUPPORT.md)
- [Release process](RELEASE.md)

---

# License

Apache License 2.0. See [LICENSE](LICENSE).
