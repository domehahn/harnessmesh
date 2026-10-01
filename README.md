# HarnessMesh

**A persistent, evidence-driven collaboration fabric, change-control plane, and Codex-compatible provider gateway for AI coding harnesses.**

HarnessMesh connects coding agents such as **Claude Code**, **OpenAI Codex**, **Google Antigravity**, **GitHub Copilot CLI**, ChatGPT-connected MCP clients, and custom adapters so they can collaborate through a shared durable control plane instead of relying on human copy/paste between tools.

It also provides a separate, bounded **model-provider plane** that lets the official Codex client talk to HarnessMesh over the Responses API wire and route inference to an explicitly configured backend.

> HarnessMesh is not a generic public LLM proxy and it is not a replacement for a model router such as NVIDIA NeMo Switchyard. Its core is agent collaboration, evidence, durable knowledge, operational control, and verified change delivery. The provider gateway is an independent plane designed specifically for controlled Codex-compatible inference routing.

Current source version: **v0.5.0**.

---

## Why HarnessMesh

Multi-agent coding workflows usually fail at the boundaries between tools: context is copied manually, reviewers cannot challenge one another with durable evidence, concurrent writers corrupt worktrees, prior decisions disappear between sessions, provider limits interrupt work, and agent-authored changes can be merged without a deterministic proof boundary.

HarnessMesh addresses those problems with four cooperating but deliberately separated planes:

```text
                                  Human / CI / IDE
                                         |
                                         v
          +----------------------+ HarnessMesh +----------------------+
          |                                                          |
          |  Collaboration Plane                                     |
          |  - spaces / channels / threads / inboxes                 |
          |  - peer conversations / reviews / findings / evidence    |
          |  - decisions / retries / budgets / approvals             |
          |                                                          |
          |  Change-Control Plane                                    |
          |  - MeshCommit transactions                               |
          |  - proof obligations / tree fingerprints / gates         |
          |                                                          |
          |  Knowledge Plane                                         |
          |  - SQLite operational state                              |
          |  - compressed cross-session archive / search / RAG       |
          |                                                          |
          |  Provider Plane                                          |
          |  - Codex-compatible /v1/responses                        |
          |  - model catalog / streaming / tool replay               |
          |  - explicit backend and billing policy                   |
          +--------------------------+-------------------------------+
                                         |
                    +--------------------+--------------------+
                    |                    |                    |
                    v                    v                    v
             Claude / Codex        MCP / VS Code        Inference backend
             Antigravity /         remote clients      local / Bedrock /
             Copilot / custom      ChatGPT peer        SIWC / explicit API
```

The **provider plane and collaboration plane share no server-side state dependency**. You can run either one independently or use both in the same deployment.

---

# Capability Map

| Area | What HarnessMesh provides |
| --- | --- |
| **Multi-agent collaboration** | Persistent spaces, channels, causal threads, mentions, subscriptions, inboxes, participant modes, peer conversations, parallel reviews, findings, evidence, challenges, and decisions |
| **Agent adapters** | Claude Code, OpenAI Codex, Google Antigravity, GitHub Copilot CLI, external MCP participants, deterministic fake adapter, extensible adapter registry |
| **Capability routing** | Dynamic peer discovery and selection by declared capabilities instead of hard-coded agent names |
| **Economy routing** | Task-tier classification, cheapest-suitable participant selection, privacy filtering, session affinity, bounded escalation and de-escalation |
| **Evidence-driven review** | Structured findings, reproducible evidence, challenges, evidence-backed resolution and conservative deduplication; no majority-vote shortcut |
| **MeshCommit** | Evidence-gated change transactions, proof obligations, tree fingerprints, stale-evidence invalidation, independent-review rules, deterministic gates and TOCTOU-safe commit binding |
| **Single-writer safety** | Exactly one writable participant per workspace; reviewers/peers can be constrained to read-only operation |
| **Context protection** | Bounded repository projections, sensitive-file filtering, traversal rejection, symlink-escape protection and command/working-directory sandbox policies |
| **Persistent knowledge** | Compressed append-only archive, hybrid search, RAG context, summaries, quality signals, explicit memory, import, retention, verification, indexing, backup/restore, encryption and key rotation |
| **Operational resilience** | Retry outbox, priorities, dead letters, quota waits, circuit breakers, agent health, budgets, approvals, idempotency and metrics |
| **MCP** | 42 tools over stdio and authenticated Streamable HTTP/SSE with sessions, bearer auth, optional OAuth2 introspection, TLS, CORS, ACLs and rate limiting |
| **Collaboration bridge** | Authenticated REST + WebSocket bridge for workspaces, inbox, messages, MeshCommit tasks, artifacts, reviews/findings and live events |
| **VS Code UI** | Participants, Tasks, Reviews and Findings views; connection status; commands; reconnect/backoff; event deduplication; SecretStorage token handling |
| **Codex provider gateway** | Authenticated `/v1/responses`, `/v1/models`, Codex-native model catalog, health/readiness/status/metrics, structured audit logs and explicit backend policy |
| **Responses compatibility** | Streaming, reasoning/instructions, function/custom tools, namespaces/additional tools, tool-call output replay and lossless SSE lifecycle handling |
| **Provider backends** | OpenAI-compatible local/self-hosted servers, AWS Bedrock, ChatGPT-plan SIWC, plus explicitly opt-in OpenAI API and Codex inference backends |
| **Billing guard** | Fail-closed `zero_api_billing_mode`, denied metered backend types, explicit fallback rules and metered-backend counters |
| **Model-routing boundary** | Fixed, external and optional NVIDIA NeMo Switchyard routing without conflating participant selection and underlying model selection |
| **Observability** | Liveness/readiness, Prometheus-style metrics, request/correlation IDs, structured audit records, operational/admin endpoints and optional OTLP integration |
| **Production validation** | Build, vet, unit, race, fuzz, protocol-contract, auth, model-catalog, filesystem write/edit, recovery, concurrency, observability, redaction and zero-API-billing gates |
| **Release engineering** | Docker, CodeQL, govulncheck, race tests, archive benchmarks, SBOM generation, checksums and keyless Cosign signing for tagged releases |

---

# 1. Persistent Multi-Agent Collaboration

HarnessMesh models collaboration as durable state rather than a sequence of isolated prompts.

A collaboration space supports:

- lifecycle states such as `active`, `paused`, `stopped` and persisted/archived history;
- a designated writer participant;
- multiple managed or external participants;
- predefined channels: `#general`, `#architecture`, `#security`, `#testing`, `#findings`, `#decisions`;
- custom channels;
- causal threads with `root_id` / `parent_id` relationships;
- participant read cursors;
- durable inboxes for offline or passive participants;
- participant activity modes: `active`, `passive`, `on_demand`, `paused`;
- channel/event subscriptions and scope patterns;
- mentions that can activate relevant participants;
- formal evidence-linked engineering decisions;
- human pause, resume and emergency-stop controls;
- event-bus delivery with burst coalescing for high-frequency repository events.

Rapid filesystem/event bursts can be collapsed into semantic events such as `repository.changed` rather than dispatching duplicate work to every peer.

---

# 2. Peer-to-Peer Agent Collaboration

Agents can consult one another directly while the user remains in one working context.

HarnessMesh supports:

- persistent multi-turn `peer.converse` threads;
- targeted `peer.ask` questions and inspections;
- capability-based discovery with `peer.capabilities`;
- causal replies;
- single-reviewer and parallel multi-review execution;
- bounded peer concurrency;
- structured findings with severity, category, file and line;
- test/diff/compiler/static-analysis evidence;
- formal challenge and resolution workflows;
- conservative duplicate/related-finding handling;
- collaboration round/depth/call ceilings;
- idempotency keys for expensive peer operations;
- durable session state and transcripts.

## Evidence, not majority voting

HarnessMesh does not settle technical disagreements by counting model votes:

```text
Claim
  -> repository evidence
  -> reproducible test / compiler / race detector / static analysis
  -> challenge if necessary
  -> evidence-backed resolution
```

Unresolved high-impact findings remain unresolved until evidence closes them or a human decision is explicitly required.

---

# 3. Capability Model & Economy Routing

Participants declare capabilities such as:

```text
read_repository
write_repository
run_commands
review
answer_questions
submit_evidence
```

Operations can target a named participant or ask HarnessMesh to resolve a suitable peer by capability.

The optional `cheapest_suitable` policy adds participant-level economy routing using:

- task tiers: `trivial`, `routine`, `complex`, `critical`;
- required capabilities;
- privacy/sensitive-path constraints;
- local-vs-cloud suitability;
- single-writer constraints;
- relative participant cost;
- participant call counts;
- session affinity;
- bounded escalation after invalid edits, build failures or failed verification;
- de-escalation after successful resolution;
- persisted routing decisions and reasons.

The routing boundary is intentional:

```text
HarnessMesh            selects the participant / harness
Switchyard (optional)  selects the underlying model / provider
```

HarnessMesh does not second-guess model selection owned by Switchyard or opaque external clients.

---

# 4. First-Class Harness Adapters

| Harness | Typical use | Execution model |
| --- | --- | --- |
| **Claude Code** | Executor, reviewer, peer | Managed/headless Claude Code adapter |
| **OpenAI Codex** | Executor, reviewer, peer | Managed Codex adapter with structured output handling |
| **Google Antigravity** | Executor, reviewer, peer | Managed Antigravity adapter plus IDE MCP integration |
| **GitHub Copilot CLI** | Executor, reviewer, peer | Managed/non-interactive Copilot CLI adapter |
| **External MCP participant** | Browser, ChatGPT-connected or remote peer | External/passive participation; no local subprocess invocation |
| **Fake adapter** | CI and deterministic tests | In-memory/local deterministic execution |

Adapters are registered through a common Harness abstraction so additional participants can be added without rewriting collaboration-domain logic.

## Managed vs external participants

- `managed`: HarnessMesh may invoke the harness process.
- `external`: HarnessMesh never pretends the participant is a local executable; work is delivered through MCP, inboxes, mentions and external client sessions.

This distinction is critical for browser/remote peers and for preventing accidental execution assumptions.

---

# 5. MeshCommit: Evidence-Gated Change Control

MeshCommit is HarnessMesh's deterministic control plane for agent-authored repository changes.

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

 aborted <- terminal exit from any non-committed state
```

A `MeshChange` tracks:

- base commit;
- current and verified working-tree fingerprints;
- affected paths and content hashes;
- policy-locked proof obligations;
- path-scoped proof requirements;
- evidence tied to an exact tree hash;
- invalidation of stale evidence when relevant files change;
- independent-review requirements;
- self-review/identity protections;
- deterministic ChangeGate evaluation without an LLM in the gate;
- verified tree binding;
- final commit SHA;
- TOCTOU protection preventing a different tree from being committed after verification.

Supported proof categories include normalized forms of unit tests, race detection, build/compile, lint/vet, independent review, security review and architecture review.

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

# 6. Durable Knowledge & Cross-Session Memory

Operational state is stored transactionally in SQLite. Historical knowledge is also appended to a separate compressed archive.

Default locations:

```text
~/.harnessmesh/harnessmesh.db
~/.harnessmesh/knowledge.hmkz
```

The `.hmkz` archive is an append-only stream of framed, block-compressed NDJSON using Zstandard. Records are flushed in bounded blocks, allowing very large histories without loading the entire archive into memory.

Knowledge can include:

- messages and conversations;
- collaboration events;
- findings, challenges and resolutions;
- evidence;
- engineering decisions;
- explicit lessons/problems/solutions;
- source/project/repository/branch/commit/agent/model/tags/confidence/sensitivity metadata.

Knowledge functions include:

- relevance-ranked search;
- persistent block index;
- deterministic local vector-similarity fallback;
- project/session/space/kind filters;
- verified-only and minimum-confidence filters;
- time/source/agent filters through advanced search;
- bounded RAG context generation;
- deterministic summaries;
- quality analysis for verification, confidence, expiry, duplicates and conflicts;
- explicit `remember` records;
- transcript import;
- retention compaction;
- archive verification;
- index rebuild;
- atomic export/backup and restore;
- encryption-key rotation;
- polling-based transcript watch/import;
- optional provider-generated embeddings through the embedding-provider interface;
- persistent vector index with project filtering and cosine ranking.

Common credential patterns are redacted before archive persistence. Set `HARNESSMESH_KNOWLEDGE_KEY` to enable AES-256-GCM encryption at rest.

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

# 7. Operational Resilience, Quotas & Human Control

HarnessMesh includes durable controls for long-running agent workflows.

## Retry and dead-letter handling

Failed deliveries can be persisted with:

- payload;
- attempt count;
- error;
- next retry timestamp;
- priority;
- automatic transient retry;
- permanent dead-letter storage after exhaustion/non-retryable failure;
- operator inspection and requeue.

## Provider quota waits

Normalized errors such as `usage limit`, `credits exhausted`, `quota exceeded`, explicit reset timestamps or `try again in ...` can be classified as quota waits.

Quota waits:

- keep work pending;
- schedule resumption for the reported provider reset time;
- do not consume a normal retry attempt;
- use a configurable fallback wait when the provider gives no reset time.

This does **not** bypass provider limits; it only makes work durable across them.

## Budgets, approvals and circuit breakers

Controls include:

- per-session and global token ceilings;
- input/output/total token bounds;
- cost budgets;
- human approval thresholds for expensive/sensitive actions;
- persisted approval/rejection records;
- agent health tracking;
- durable circuit-breaker state;
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

# 8. MCP Interface — 42 Tools

HarnessMesh exposes a standards-based MCP server over stdio and authenticated Streamable HTTP.

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
| `knowledge.search_advanced` | Search by time, source, agent and related metadata |

## Operations tools — 5

| Tool | Purpose |
| --- | --- |
| `operations.status` | Agent health, quota waits, circuit breakers, retry backlog and metrics |
| `operations.approvals` | List human approval records |
| `operations.approve` | Approve or reject an operation |
| `operations.dead_letters` | List permanently failed deliveries |
| `operations.requeue_dead_letter` | Requeue a dead-letter delivery |

## MeshCommit tools — 8

| Tool | Purpose |
| --- | --- |
| `change.create` | Create an evidence-gated change transaction |
| `change.prepare` | Freeze/prepare current change state for verification |
| `change.status` | Inspect tree hashes, obligations, evidence and gate state |
| `change.diff` | Inspect affected paths/content hashes |
| `change.verify` | Execute a proof obligation |
| `change.evidence` | Submit structured proof evidence |
| `change.abort` | Abort an in-flight change |
| `change.commit_status` | Check committability and missing/failed obligations |

Total: **11 + 9 + 9 + 5 + 8 = 42 MCP tools**.

---

# 9. MCP Transports, Authentication & Administration

## Local stdio

```bash
harnessmesh mcp serve --repo . --config harnessmesh.json --caller claude
```

## Remote Streamable HTTP/SSE

```bash
export HARNESSMESH_MCP_TOKEN="$(openssl rand -hex 32)"
harnessmesh mcp serve \
  --listen 127.0.0.1:8787 \
  --token "$HARNESSMESH_MCP_TOKEN" \
  --caller remote-agent
```

Capabilities include:

- JSON-RPC over HTTP;
- MCP Streamable HTTP endpoint;
- SSE responses;
- MCP session creation and termination;
- protocol-version negotiation;
- bearer-token authentication with constant-time comparison;
- optional OAuth2 token introspection;
- TLS certificate/key support;
- strict CORS handling and explicit origin allowlisting;
- project and caller ACLs;
- separate admin-caller ACL;
- request-rate limiting;
- bounded request bodies;
- security headers;
- liveness and metrics endpoints;
- admin knowledge/operations endpoints and lightweight operations UI.

Useful environment variables:

```text
HARNESSMESH_MCP_PROJECTS
HARNESSMESH_MCP_CALLERS
HARNESSMESH_MCP_ADMIN_CALLERS
HARNESSMESH_MCP_RATE_LIMIT
HARNESSMESH_MCP_OAUTH_INTROSPECTION_URL
HARNESSMESH_MCP_OAUTH_CLIENT_SECRET
HARNESSMESH_CORS_ALLOWED_ORIGINS
```

---

# 10. Collaboration Bridge & VS Code Extension

The collaboration bridge is a separate REST/WebSocket transport for UI clients. It does **not** perform LLM reasoning itself.

Start it with:

```bash
harnessmesh bridge serve \
  --repo . \
  --config harnessmesh.json \
  --token <strong-token> \
  --listen 127.0.0.1:8788
```

The API exposes collaboration state such as:

- workspace list/details/status;
- participant inbox;
- message publication;
- MeshCommit-backed tasks/change transactions;
- prepare/abort task actions;
- session-scoped evidence/artifacts;
- findings and review aliases;
- recent-event polling/catch-up;
- WebSocket live event delivery;
- idempotent mutation handling;
- authenticated caller identity enforcement.

## VS Code extension

`extensions/vscode` provides a thin UI over the bridge:

- Participants view;
- Tasks view (backed by `MeshChange`, not a second task model);
- Reviews view;
- Findings view;
- bridge connection status in the status bar;
- connect/disconnect commands;
- workspace selection;
- collaboration status and inbox commands;
- task/review/finding commands;
- send-message command;
- bridge-token command;
- token storage through VS Code `SecretStorage`;
- WebSocket reconnect with backoff;
- event-ID deduplication.

The extension itself never calls OpenAI, Codex or ChatGPT directly and cannot bypass HarnessMesh's single-writer enforcement.

---

# 11. Codex-Compatible Provider Gateway

HarnessMesh can act as a custom model provider for the **official Codex VS Code extension / Codex CLI**.

```text
Official Codex client
        |
        | Responses wire
        v
HarnessMesh provider :8789
        |
        +--> OpenAI-compatible local/self-hosted server
        +--> AWS Bedrock
        +--> ChatGPT plan via SIWC
        +--> OpenAI API       (explicit opt-in; denied by default)
        +--> Codex inference  (explicit opt-in; denied by default)
```

The provider plane is independent of MCP collaboration. It does not create spaces, findings or MeshCommit transactions.

## HTTP surface

| Route | Purpose |
| --- | --- |
| `GET /healthz` | Process liveness |
| `GET /readyz` | At least one allowed backend is healthy |
| `GET /metrics` | Provider metrics |
| `GET /v1/status` | Authenticated provider status |
| `GET /v1/models` | Generic or Codex-native model catalog |
| `POST /v1/responses` | Authenticated Responses-compatible inference endpoint |

The default provider listen address is `127.0.0.1:8789`. Public binding requires explicit configuration rather than silently exposing the gateway.

Provider requests require a bearer token supplied through configuration, `--token`, or `HARNESSMESH_PROVIDER_TOKEN`.

## Supported provider backends

| Backend type | Purpose | OpenAI API-key billing under strict mode |
| --- | --- | --- |
| `openai-compatible` | Local/self-hosted OpenAI-compatible server such as vLLM, Ollama, LM Studio or llama.cpp | Not involved |
| `bedrock` | AWS Bedrock Converse/streaming backend | Not involved; normal AWS costs may still apply |
| `chatgpt-subscription` | ChatGPT-plan Responses usage through Sign in with ChatGPT | No API-key billing, but plan allowance may be consumed |
| `openai-api` | Direct API-key-authenticated OpenAI backend | **Denied by default** |
| `codex` | Codex CLI used as an inference backend | **Denied by default** |

Backend selection is explicit through `default_backend` or the gateway's explicit backend-selection mechanism. Credentials merely existing in the environment do not make a backend reachable.

Fallback is optional and ordered. Every candidate is rechecked against policy before use.

---

# 12. `zero_api_billing_mode`: Exact Guarantee

The canonical provider safety setting is:

```json
{
  "provider": {
    "zero_api_billing_mode": true
  }
}
```

It defaults to **true**. The older `zero_credit_mode` setting remains a compatibility alias but is no longer the preferred user-facing term.

When strict mode is enabled HarnessMesh guarantees, within its routing boundary:

```text
OpenAI API-key-billed backend   DENIED
Codex CLI inference backend     DENIED
Fallback into either backend    DENIED
```

This is deliberately **not** called "zero credits", because that would overstate what HarnessMesh controls.

### Important SIWC allowance caveat

`chatgpt-subscription` uses OpenAI's Sign in with ChatGPT flow to authorize eligible Responses API calls against the signed-in ChatGPT plan. It does not use an `OPENAI_API_KEY` and it does not invoke the Codex CLI.

However:

> HarnessMesh cannot guarantee zero ChatGPT/Codex plan allowance usage for SIWC. OpenAI controls that accounting, and bundled products may draw from a shared allowance/credit pool.

Therefore:

```text
zero API-key billing                    yes, enforced by strict mode
zero Codex backend invocation           yes, enforced by strict mode
zero ChatGPT/Codex plan allowance       NOT guaranteed for SIWC
```

HarnessMesh does not turn ChatGPT into a free API, and SIWC usage is not equivalent to normal browser-chat usage.

---

# 13. Sign in with ChatGPT (SIWC)

Authenticate the provider gateway for `chatgpt-subscription` with:

```bash
harnessmesh provider auth chatgpt
```

HarnessMesh implements the local OAuth/OIDC + PKCE lifecycle needed to obtain and persist the ChatGPT-plan credential, including token/ID-token validation and refresh handling.

Then configure a backend:

```json
{
  "provider": {
    "enabled": true,
    "default_backend": "chatgpt",
    "backends": {
      "chatgpt": {
        "type": "chatgpt-subscription"
      }
    }
  }
}
```

SIWC is an inference credential only. It does not expose existing ChatGPT conversation history, memory, custom instructions or browser-session context to HarnessMesh.

---

# 14. Responses Protocol Compatibility

The Codex-facing provider uses the Responses wire and includes contract coverage for real Codex request/replay shapes.

Supported/covered behaviors include:

- string and structured message input;
- system/developer/user role handling;
- content arrays;
- top-level `instructions`;
- reasoning payload preservation/replay;
- text/store/stream/parallel-tool-call fields;
- `tool_choice`;
- function tools;
- custom tools;
- namespaces and recursively nested namespace definitions;
- `additional_tools`;
- custom tool calls and outputs;
- function calls and outputs;
- tool output as strings and structured content arrays;
- replay across subsequent Responses turns;
- final post-tool model response;
- unknown-field preservation where the transparent transport contract requires it.

## SSE streaming

The provider includes lifecycle/relay coverage for events such as:

```text
response.created
response.in_progress
response.output_item.added
response.output_item.done
response.content_part.added
response.content_part.done
response.output_text.delta
response.output_text.done
response.function_call_arguments.delta
response.function_call_arguments.done
response.completed
response.failed
response.incomplete
```

Sequence numbers and payload fidelity are preserved where the provider acts as a transparent relay, with tests for malformed/truncated streams and terminal-event behavior.

---

# 15. Codex Model Catalog Compatibility

HarnessMesh serves two model-catalog dialects from the same endpoint:

```text
GET /v1/models
  -> generic OpenAI-compatible {"object":"list","data":[...]}

GET /v1/models?client_version=<codex-version>
  -> Codex-native {"models":[...]}
```

The vendored Codex catalog schema originates from OpenAI Codex `rust-v0.155.0-alpha.16.3`. Contract tests also cover later observed Codex clients, while the live metadata check dynamically discovers the installed `codex --version` rather than claiming untested future compatibility.

Do not invent model metadata when upgrading Codex: run the model-contract tests and live metadata check first.

---

# 16. Codex Provider Setup

Example provider configuration:

```json
{
  "version": 2,
  "agents": {
    "placeholder": {
      "kind": "fake",
      "role": "executor",
      "writable": true
    }
  },
  "provider": {
    "enabled": true,
    "listen": "127.0.0.1:8789",
    "zero_api_billing_mode": true,
    "default_backend": "local",
    "backends": {
      "local": {
        "type": "openai-compatible",
        "base_url": "http://127.0.0.1:8000/v1",
        "model": "your-local-model-id",
        "timeout_sec": 120
      }
    },
    "denied_backend_types": ["openai-api", "codex"]
  }
}
```

Generate a strong provider token and start the gateway:

```bash
export HARNESSMESH_PROVIDER_TOKEN="$(openssl rand -hex 32)"
harnessmesh provider serve --config harnessmesh.json
```

Generate/merge the Codex custom-provider configuration:

```bash
harnessmesh integrate codex-provider \
  --scope user \
  --listen 127.0.0.1:8789 \
  --model harnessmesh-local
```

Then validate the deployment:

```bash
harnessmesh provider doctor --config harnessmesh.json
```

`integrate codex-provider` preserves unrelated TOML content, supports dry-run/check modes, and backs up an existing config unless backup is explicitly disabled. Project scope can install the provider table, while Codex's active top-level provider selection remains a user-scope concern.

See [`docs/codex-provider.md`](docs/codex-provider.md) for the full configuration and billing model.

---

# 17. Provider Security & Observability

The provider gateway includes:

- required bearer authentication;
- constant-time token comparison;
- loopback-safe defaults and explicit public-listen opt-in;
- absolute HTTP(S) upstream validation;
- unsupported-backend rejection;
- explicit allowed/denied backend types;
- fail-closed metered-backend policy;
- bounded request bodies/timeouts;
- health/readiness separation;
- request/correlation IDs;
- structured JSON audit records;
- route/method/status/duration/backend/stream/error-class fields;
- cancellation and timeout classification;
- policy-denial counters;
- backend call/failure counters;
- prompt/credential redaction from default audit output;
- metered-backend counters for `openai-api` and `codex`.

Provider metrics include total requests/errors, active streams, `/responses` request count, policy denials and per-backend call/failure counts.

The Codex UI may independently request routes such as `/settings/user`, `/wham/usage`, `/subscriptions`, `/plugins/featured` or `/accounts/optimized/check`. Those are external UI requests and are not the contract used to determine `/v1/responses` compatibility.

---

# 18. Production Readiness

The authoritative deterministic gate is:

```bash
make production-readiness
```

It intentionally uses local/fake/fixture-backed execution and refuses to run when live OpenAI inference has explicitly been enabled.

The gate reports:

```text
BUILD
VET
UNIT
RACE
FUZZ
CONTRACT
AUTH
MODEL_CATALOG
WRITE_EDIT
RECOVERY
CONCURRENCY
OBSERVABILITY
REDACTION
ZERO_API_BILLING
PRODUCTION_READY
```

`WRITE_EDIT` is backed by a real disposable-filesystem E2E that exercises create -> replay -> edit -> replay -> verify -> delete through the provider's production paths and requires workspace cleanup.

## Inference-safe live metadata check

```bash
make live-metadata-check
```

This check:

- discovers the installed `codex --version`;
- creates an isolated temporary `CODEX_HOME`;
- starts the actual Codex app-server;
- starts HarnessMesh in explicit metadata-only test mode;
- verifies the Codex-native `/v1/models?client_version=...` contract;
- verifies the expected model appears through Codex's own catalog decoder;
- checks for model-refresh/decode failures;
- validates structured `/v1/models` audit data;
- asserts `responses_requests=0`;
- rejects `/v1/responses` locally before backend resolution;
- performs no model turn.

The metadata-only mode is guarded by `HARNESSMESH_METADATA_ONLY_TEST=1` and is not a normal production operating mode.

## Live inference is explicitly outside the deterministic gate

`make live-openai-e2e` is guarded by:

```text
HARNESSMESH_ALLOW_LIVE_OPENAI_INFERENCE=1
```

The deterministic production gate never requires real OpenAI inference or agentic-plan usage.

---

# 19. Security Model

Core safety invariants include:

## Single writer

Exactly one participant may be writable for a worktree/space. Reviewers and peers can be constrained to read-only operation.

## Bounded context projection

Agents receive bounded repository-centric context rather than unfiltered transcripts/workspaces.

## Sensitive file filtering

Common credential/private-key/state paths are excluded or redacted, including patterns such as:

```text
.env
*.pem
*.key
*.p12
id_rsa
id_ed25519
secrets/
credentials/
terraform.tfstate
.git/
```

## Workspace escape protection

Path traversal and symlink escapes outside the repository root are rejected.

## Sandbox policy

Adapters can enforce command and working-directory allowlists through the execution policy layer.

## Identity anti-spoofing

Authenticated MCP/bridge callers cannot submit another participant's identity in protected operations.

## Change-control integrity

MeshCommit enforces independent review, exact-tree evidence binding and TOCTOU-safe commit checks.

## Credential separation

MCP tokens, bridge tokens, provider tokens, SIWC credentials and optional provider API credentials are separate concerns. The VS Code bridge extension stores its token in `SecretStorage`, not plaintext settings.

See [`docs/security.md`](docs/security.md).

---

# 20. Persistence & Storage

SQLite/WAL is the default transactional backend and stores collaboration/session state such as:

- sessions and messages;
- participant state;
- spaces/channels/threads/subscriptions;
- findings/evidence/decisions;
- usage and routing decisions;
- retry outbox/dead letters;
- agent health/circuit breakers;
- approvals;
- MeshCommit changes/paths/proof obligations/evidence/gate results.

The knowledge archive remains a separate append-only historical store.

A storage `BackendFactory` exists as an extension seam for deployments that provide other backends. SQLite is the implemented default; the README does not imply a bundled production PostgreSQL implementation.

---

# 21. NVIDIA NeMo Switchyard & Model Routing

HarnessMesh supports routing backends:

```text
fixed
external
switchyard
```

Switchyard integration provides:

- backend registry integration;
- health checks;
- route IDs;
- endpoint configuration;
- participant configuration for routed providers;
- routing metadata/diagnostics;
- CLI doctor/routes/config validation.

```bash
harnessmesh switchyard doctor --config harnessmesh.json
harnessmesh switchyard routes --config harnessmesh.json
harnessmesh switchyard config validate --config harnessmesh.json
```

Switchyard is optional. HarnessMesh remains responsible for **which participant** should perform work; Switchyard may decide **which underlying model/provider** serves that participant.

---

# 22. Integration Helpers

## MCP installers

```bash
harnessmesh mcp install claude --scope project
harnessmesh mcp install codex --scope project
harnessmesh mcp install antigravity --scope project
harnessmesh mcp install copilot --scope project
```

Supported integrations can also be installed at user scope where applicable.

## Antigravity

```bash
harnessmesh integrate antigravity --repo . --config configs/antigravity-openai-peer.json
```

This installs MCP configuration and collaboration rules so Antigravity can autonomously use HarnessMesh peer/collaboration tools.

## ChatGPT as an external collaboration peer

```bash
harnessmesh integrate chatgpt --repo . --config configs/chatgpt-claude.json
```

This prepares the Streamable HTTP MCP path for a ChatGPT-connected external participant. This is a **collaboration** role and is distinct from the SIWC `chatgpt-subscription` **inference** backend.

## Codex model provider

```bash
harnessmesh integrate codex-provider --scope user --listen 127.0.0.1:8789
```

This configures the official Codex client to use the separate provider gateway.

---

# 23. Diagnostics & Inspection

```bash
harnessmesh doctor
harnessmesh provider doctor --config harnessmesh.json
harnessmesh agents list
harnessmesh agents show <name>
harnessmesh session list
harnessmesh session show <id>
harnessmesh session messages <id>
harnessmesh findings <session-id>
harnessmesh evidence <session-id>
```

`doctor` checks configured harnesses, integrations, routing dependencies, Git and SQLite. `provider doctor` separately validates the provider plane, backend health and billing-policy constraints.

---

# 24. CLI Reference

```text
harnessmesh collaborate --task "..." [options]

harnessmesh space <list|show|create|pause|resume|stop> [options]
harnessmesh channel <list|create|show> [options]
harnessmesh thread <list|show|reply> [options]
harnessmesh inbox list --space <id> [options]
harnessmesh subscriptions <list|add|remove> [options]
harnessmesh decide <list|propose|accept> [options]

harnessmesh change <create|list|show|prepare|verify|evidence|gate|commit|abort> [options]

harnessmesh peer converse --message "..." [options]
harnessmesh peer ask --peer <name> --question "..." [options]
harnessmesh peer review --peer <name> [options]
harnessmesh peer status --session <id>

harnessmesh mcp serve [options]
harnessmesh mcp install <claude|codex|antigravity|copilot> [--scope <project|user>]

harnessmesh bridge serve [--repo <path>] [--config <path>] [--caller <name>] [--listen <addr>]

harnessmesh provider serve [--config <path>] [--listen <addr>] [--token <token>]
harnessmesh provider doctor [--config <path>]
harnessmesh provider auth chatgpt [--token-path <path>]

harnessmesh integrate <antigravity|chatgpt|codex-provider> [options]

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

---

# 25. Configuration Profiles

Ready-to-use examples in `configs/` include:

- `claude-codex.json`
- `antigravity-codex.json`
- `copilot-codex.json`
- `antigravity-multi-review.json`
- `codex-multi-review.json`
- `antigravity-openai-peer.json`
- `chatgpt-claude.json`
- `harnessmesh.example.json`
- `codex-provider.example.json`
- `harnessmesh.switchyard.example.json`
- `switchyard-example.json`
- `no-switchyard-example.json`

Validate or migrate configuration with:

```bash
harnessmesh config validate harnessmesh.json
harnessmesh config migrate old.json migrated.json
harnessmesh config print --config harnessmesh.json
```

User-facing config output is redacted where sensitive values are involved.

---

# 26. Build, Test & Release Engineering

## Build

```bash
git clone https://github.com/domehahn/harnessmesh.git
cd harnessmesh
make build
```

Prerequisites:

- Go 1.23+
- Git
- SQLite/CGO build prerequisites for the configured target environment

## Validation targets

```bash
make test
make vet
make race
make fuzz
make load
make security
make production-readiness
make live-metadata-check
```

CI/release engineering includes combinations of:

- unit tests;
- race detector;
- fuzz seed/smoke coverage;
- archive benchmarks;
- `go vet`;
- `govulncheck`;
- CodeQL;
- Docker builds;
- tagged release binaries;
- SHA-256 checksums;
- CycloneDX SBOM generation;
- keyless Cosign signing.

---

# 27. Design Invariants

The following rules define the architecture:

1. **Evidence beats model voting.** Claims are resolved by reproducible proof, not popularity.
2. **One worktree, one writer.** Parallel reviewers are encouraged; parallel unsynchronized writers are not.
3. **Agent selection and model selection are different decisions.** HarnessMesh owns participant orchestration; Switchyard/provider backends own model/provider selection within their boundary.
4. **External participants remain external.** Browser/remote clients are not silently executed as local subprocesses.
5. **Historical knowledge is durable but bounded at retrieval time.** Large archives do not imply unbounded prompts.
6. **Verification is tied to exact repository state.** Relevant changes invalidate stale proof.
7. **The final change gate is deterministic.** An LLM cannot simply declare its own change safe.
8. **Metered backend access is explicit.** Credentials in the environment do not silently override provider policy.
9. **`zero_api_billing_mode` says exactly what it guarantees.** It does not make claims about OpenAI-controlled SIWC plan accounting.
10. **Production validation must be inference-safe by default.** Live OpenAI inference is not required to prove deterministic provider correctness.

---

# 28. Documentation

## Architecture and collaboration

- [Architecture](docs/ARCHITECTURE.md)
- [Collaboration Spaces](docs/collaboration-spaces.md)
- [Channels & Threads](docs/channels-and-threads.md)
- [Events & Coalescing](docs/events-and-coalescing.md)
- [Inbox & Activation](docs/inbox-and-activation.md)
- [Decisions & Evidence](docs/decisions-and-evidence.md)
- [Human Controls](docs/human-controls.md)
- [Peer Conversation](docs/peer-conversation.md)
- [Collaboration Patterns](docs/collaboration-patterns.md)
- [Capabilities](docs/capabilities.md)
- [Economy Routing](docs/economy-routing.md)

## Change control

- [MeshChange Transactions](docs/change-transactions.md)

## Interfaces and integrations

- [MCP](docs/mcp.md)
- [ChatGPT Integration](docs/chatgpt-integration.md)
- [Codex Provider Gateway](docs/codex-provider.md)
- [Antigravity Integration](docs/antigravity-integration.md)
- [Switchyard](docs/switchyard.md)

## Security and operations

- [Security](docs/security.md)
- [Context Projection](docs/context-projection.md)
- [Sessions & Persistence](docs/sessions.md)
- [Troubleshooting](docs/troubleshooting.md)

## Adapter guides

- [Claude Code](docs/adapters/claude-code.md)
- [OpenAI Codex](docs/adapters/codex.md)
- [Google Antigravity](docs/adapters/antigravity.md)
- [GitHub Copilot CLI](docs/adapters/copilot-cli.md)

## Project

- [Contributing](CONTRIBUTING.md)
- [Security Policy](SECURITY.md)
- [Support](SUPPORT.md)
- [Governance](GOVERNANCE.md)
- [Release Process](RELEASE.md)
- [Code of Conduct](CODE_OF_CONDUCT.md)

---

# 29. What HarnessMesh Does Not Promise

For clarity:

- It does not make multiple LLM votes equivalent to evidence.
- It does not permit multiple unsynchronized writers to mutate the same worktree.
- It does not expose ChatGPT browser history or account memory through SIWC.
- It does not guarantee that SIWC is free or outside ChatGPT/Codex plan allowances.
- It does not silently use `OPENAI_API_KEY` merely because one exists.
- It does not claim future Codex model-catalog compatibility without contract/live-metadata verification.
- It does not ship a complete PostgreSQL/object-store backend merely because a storage extension seam exists.
- It does not turn the collaboration bridge or VS Code extension into an LLM runtime; those are state/UI transports.
- It does not require real OpenAI inference in the deterministic production-readiness gate.

---

## License

Apache 2.0. See [LICENSE](LICENSE).
