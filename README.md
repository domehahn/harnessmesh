# HarnessMesh

**A persistent, evidence-driven collaboration fabric, change-control plane, and Codex-compatible provider gateway for AI coding harnesses.**

HarnessMesh connects coding agents such as **Claude Code**, **OpenAI Codex**, **Google Antigravity**, **GitHub Copilot CLI**, ChatGPT-connected MCP clients, and custom adapters so they can collaborate through a shared durable control plane instead of relying on human copy/paste between tools.

It also provides a separate, bounded **model-provider plane** that lets the official Codex client talk to HarnessMesh over the Responses API wire and route inference to an explicitly configured backend.

> HarnessMesh is not a generic public LLM proxy and it is not a replacement for a model router such as NVIDIA NeMo Switchyard. Its core is agent collaboration, evidence, durable knowledge, operational control, and verified change delivery. The provider gateway is an independent plane designed specifically for controlled Codex-compatible inference routing.

Current source version: **v0.1.0**.

## Installation und vollständiges Setup

### Native Installation

Voraussetzungen sind Go 1.25 oder neuer, Git und optional Docker Compose.
Das Repository klonen und den lokalen Binär-Wrapper bauen:

```bash
git clone https://github.com/domehahn/harnessmesh.git
cd harnessmesh
./scripts/setup.sh
```

`setup.sh` erzeugt bei Bedarf `harnessmesh.json`, baut
`bin/harnessmesh` und führt keine Zugangsdaten in die Konfiguration ein.
Anschließend kann die Installation geprüft werden:

```bash
bin/harnessmesh config validate --config harnessmesh.json
bin/harnessmesh doctor --config harnessmesh.json
bin/harnessmesh --help
```

### Installation über Go

Für die reine CLI-Installation ist kein Repository-Checkout erforderlich:

```bash
go install github.com/domehahn/harnessmesh/cmd/harnessmesh@v0.1.0
harnessmesh version
```

Go installiert das Programm in `GOBIN` oder standardmäßig nach
`$(go env GOPATH)/bin`. Falls dieser Ordner noch nicht im `PATH` liegt:

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
harnessmesh --help
```

Für ein reproduzierbares Upgrade kann eine konkrete Version verwendet werden;
`@latest` folgt dagegen dem neuesten veröffentlichten Modulstand:

```bash
go install github.com/domehahn/harnessmesh/cmd/harnessmesh@v0.1.0
# später:
go install github.com/domehahn/harnessmesh/cmd/harnessmesh@latest
```

Die Go-Installation enthält die CLI, aber keine globale Datenbank oder
Konfiguration. Diese entstehen erst bei der Verwendung unter
`~/.harnessmesh` beziehungsweise im jeweiligen Projektverzeichnis.

### Vereinfachte Docker-Installation für Codex und ChatGPT-Plan-Inferenz

Für die Codex-VS-Code-Extension ist der empfohlene lokale Ablauf:

```bash
./scripts/setup.sh
./scripts/start-codex-compose.sh
```

Das Startskript:

- erzeugt einmalig die ignorierte Datei `.env` mit einem zufälligen
  `HARNESSMESH_PROVIDER_TOKEN`;
- verwendet standardmäßig `configs/codex-chatgpt.example.json`;
- integriert den Custom Provider in `~/.codex/config.toml`;
- führt beim ersten Start den SIWC-Browser-Login auf dem Host aus;
- verwendet `~/.harnessmesh` für ChatGPT-Credentials, Refresh-Status und
  HarnessMesh-Daten;
- startet `harnessmesh-provider` mit Docker Compose;
- startet VS Code optional mit `HARNESSMESH_OPEN_CODE=1`.

```bash
HARNESSMESH_OPEN_CODE=1 ./scripts/start-codex-compose.sh .
```

Das Token wird in Codex über `env_key` aus der Umgebung gelesen und nicht als
Klartext in `config.toml` gespeichert. Wenn VS Code bereits läuft, muss es
mit der Umgebung aus `.env` neu gestartet werden:

```bash
set -a; . ./.env; set +a
code .
```

Der Browser-Login ist nur beim ersten Start oder nach dem Löschen der
SIWC-Credentials erforderlich. Der Callback läuft absichtlich auf dem Host;
der Container verwendet anschließend denselben Credential-Ordner.

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

# 10. Codex Plugin & Marketplace

HarnessMesh includes a local Codex plugin under
[`plugins/harnessmesh-codex`](plugins/harnessmesh-codex). The plugin connects
Codex to the existing local stdio MCP server and adds reusable skills for
collaboration, reviews and MeshCommit. It does not bundle an LLM or replace
the HarnessMesh CLI.

## Install the local marketplace

Install HarnessMesh first and make sure `harnessmesh.json` exists in the
repository you want Codex to work on:

```bash
go install github.com/domehahn/harnessmesh/cmd/harnessmesh@v0.1.0
cd /path/to/your/repository
cp /path/to/harnessmesh/configs/harnessmesh.example.json harnessmesh.json
harnessmesh config validate --config harnessmesh.json
```

Register the repository's plugin catalog with Codex:

```bash
cd /path/to/harnessmesh
codex plugin marketplace add ./plugins
codex plugin marketplace list
```

The catalog contains the `harnessmesh-codex` plugin. Enable it for the target
repository through the Codex plugin UI or project configuration. The plugin
starts this local MCP process on demand:

```bash
harnessmesh mcp serve --caller codex
```

The process uses the current working directory as the repository and reads
`harnessmesh.json` by default. For a manual smoke test, run it directly from
the target repository and confirm that the MCP client can enumerate the
HarnessMesh tools. Stop it with `Ctrl-C` when finished; Codex normally manages
the process lifecycle itself.

## Plugin capabilities

The plugin exposes the existing HarnessMesh MCP surface, including:

- collaboration status, inbox, channels, threads and peer communication;
- evidence-oriented peer reviews, findings and review resolution;
- durable knowledge search and project context;
- operational status and approval visibility;
- evidence-gated MeshCommit change preparation and verification.

The plugin's skills are guidance around these tools. They do not authorize
destructive changes by themselves, and write operations remain subject to the
configured HarnessMesh caller, project and single-writer checks.

## Use the plugin from a Git checkout

For a local checkout, refresh the marketplace after changing plugin files:

```bash
codex plugin marketplace upgrade harnessmesh
codex plugin marketplace list
```

The current catalog is intentionally local and repository-backed. A public
ChatGPT/Codex plugin directory release additionally requires a hosted MCP
endpoint, authentication suitable for remote clients, verified publisher
metadata and the OpenAI plugin submission/review process.

---

# 11. Collaboration Bridge & VS Code Extension

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

### HarnessMesh in VS Code integrieren

Die HarnessMesh-Erweiterung ist die UI für die Collaboration-Bridge. Sie ist
nicht die offizielle Codex-Erweiterung und führt selbst keine LLM-Inferenz aus.
Für die Integration werden CLI, Bridge und Erweiterung in dieser Reihenfolge
gestartet.

#### 1. CLI und Projekt vorbereiten

```bash
go install github.com/domehahn/harnessmesh/cmd/harnessmesh@v0.1.0
export PATH="$(go env GOPATH)/bin:$PATH"

cd /path/to/dein/repository
cp /path/to/harnessmesh/configs/harnessmesh.example.json harnessmesh.json
harnessmesh config validate --config harnessmesh.json
```

#### 2. Bridge starten

Native auf dem Host:

```bash
export HARNESSMESH_BRIDGE_TOKEN="$(openssl rand -hex 32)"
harnessmesh bridge serve \
  --repo "$PWD" \
  --config harnessmesh.json \
  --listen 127.0.0.1:8788 \
  --token "$HARNESSMESH_BRIDGE_TOKEN"
```

Oder mit Docker Compose aus dem HarnessMesh-Repository:

```bash
export HARNESSMESH_BRIDGE_TOKEN="$(openssl rand -hex 32)"
docker compose --profile bridge up --build -d harnessmesh-bridge
```

Die lokale Bridge ist anschließend unter
`http://127.0.0.1:8788` erreichbar. Prüfe sie vor der Verbindung:

```bash
curl -fsS http://127.0.0.1:8788/healthz
```

#### 3. Erweiterung installieren

Wenn die Marketplace-Version veröffentlicht ist, in VS Code nach
`HarnessMesh` suchen und die Erweiterung des Publishers `harnessmesh`
installieren. Bis dahin kann die VSIX lokal installiert werden:

```bash
cd /path/to/harnessmesh/extensions/vscode
npm install
npm run compile
npx @vscode/vsce package
code --install-extension harnessmesh-vscode-0.1.0.vsix
```

Danach den Zielordner als Workspace in VS Code öffnen.

#### 4. Token und Workspace verbinden

Öffne die Command Palette (`Cmd+Shift+P` beziehungsweise `Ctrl+Shift+P`)
und führe diese Befehle aus:

1. `HarnessMesh: Set Bridge Token` — denselben Wert wie
   `HARNESSMESH_BRIDGE_TOKEN` eingeben;
2. `HarnessMesh: Select Workspace` — den Workspace auswählen;
3. `HarnessMesh: Connect to Bridge` — die Verbindung herstellen.

Der Token wird über VS Code `SecretStorage` gespeichert und nicht in
`settings.json` oder im Repository abgelegt. Der Statusbalken muss danach
`HarnessMesh: Connected` anzeigen.

#### 5. Optionale VS-Code-Einstellungen

In `.vscode/settings.json` oder den Benutzereinstellungen:

```json
{
  "harnessmesh.bridgeUrl": "http://127.0.0.1:8788",
  "harnessmesh.autoConnect": true
}
```

`autoConnect` funktioniert, sobald zuvor ein Token gespeichert wurde. Die
Ansichten `Participants`, `Tasks`, `Reviews` und `Findings` erscheinen dann in
der HarnessMesh-Seitenleiste.

#### Codex und HarnessMesh gemeinsam verwenden

Die beiden Erweiterungen haben unterschiedliche Aufgaben:

| Komponente | Aufgabe |
| :--- | :--- |
| HarnessMesh VS Code Extension | Collaboration-Status, Tasks, Reviews, Findings und Nachrichten |
| Offizielle Codex VS Code Extension | IDE-Agent, Repository-Kontext und Modellinteraktion |
| HarnessMesh Provider Gateway | Optionaler Custom-Provider für Codex über `/v1/responses` |

Für reine Collaboration genügt die HarnessMesh-Erweiterung mit der Bridge.
Für ChatGPT-Plan-Inferenz über Codex wird zusätzlich der Provider aus dem
Abschnitt [Codex VS Code Extension mit ChatGPT-Plan-Inferenz](#codex-vs-code-extension-mit-chatgpt-plan-inferenz) benötigt.

#### Fehlerbehebung

- `401 Unauthorized`: Bridge-Token in VS Code und Shell stimmen nicht überein;
  Token erneut über `HarnessMesh: Set Bridge Token` speichern.
- `ECONNREFUSED`: Bridge läuft nicht oder `harnessmesh.bridgeUrl` zeigt auf
  den falschen Port.
- Keine Workspace-Daten: richtigen Workspace auswählen und prüfen, dass
  `--repo` auf das geöffnete Repository zeigt.
- Keine Live-Updates: Bridge neu starten und anschließend in VS Code
  `HarnessMesh: Disconnect from Bridge` und `HarnessMesh: Connect to Bridge` ausführen.


### VS Code Marketplace

Die Erweiterung ist für eine Veröffentlichung unter dem Publisher
`harnessmesh` vorbereitet. Sie stellt die Collaboration-Bridge-Oberfläche
bereit; sie ersetzt weder die offizielle Codex-Erweiterung noch den
Codex-kompatiblen Provider. Der Provider wird weiterhin über
`~/.codex/config.toml` konfiguriert.

#### Lokale VSIX-Installation

```bash
cd extensions/vscode
npm install
npm run compile
npx @vscode/vsce package
code --install-extension harnessmesh-vscode-0.1.0.vsix
```

Alternativ kann die Erweiterung in VS Code über `Run and Debug` als Extension
Development Host gestartet werden. Der Bridge-Token wird über VS Code
`SecretStorage` gespeichert und nicht in `settings.json` geschrieben.

#### Veröffentlichung im Marketplace

Dafür benötigt das Projekt einmalig einen verifizierten VS-Code-Marketplace-
Publisher namens `harnessmesh` und ein dafür ausgestelltes PAT. Das PAT darf
nicht in Git oder in `package.json` abgelegt werden:

```bash
cd extensions/vscode
npm install
npm run compile
npx @vscode/vsce package
npx @vscode/vsce publish --pat "$VSCE_PAT"
```

Die Marketplace-Veröffentlichung sollte als eigener Extension-Release mit
der Version aus `extensions/vscode/package.json` erfolgen. CLI-, Docker- und
VS-Code-Extension-Versionen können dabei unabhängig voneinander veröffentlicht
werden, sollten für eine gemeinsame Produktversion aber synchronisiert werden.

---

# 12. Codex-Compatible Provider Gateway

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

# 13. `zero_api_billing_mode`: Exact Guarantee

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

# 14. Sign in with ChatGPT (SIWC)

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

# 15. Responses Protocol Compatibility

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

# 16. Codex Model Catalog Compatibility

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

# 17. Codex Provider Setup

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

# 18. Provider Security & Observability

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

# 19. Production Readiness

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

# 20. Security Model

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

# 21. Persistence & Storage

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

# 22. NVIDIA NeMo Switchyard & Model Routing

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

# 23. Integration Helpers

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

# 24. Diagnostics & Inspection

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

# 25. CLI Reference

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

# 26. Configuration Profiles

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

## Configuration File Guide

### Collaboration profiles

Every profile below is a complete JSON configuration for a different
collaboration topology. Validate any profile before using it:

```bash
bin/harnessmesh config validate configs/claude-codex.json
```

| Profile | Intended topology | Executor | Reviewer(s) | Example |
| :--- | :--- | :--- | :--- | :--- |
| configs/claude-codex.json | Standard two-agent review | Claude | Codex | bin/harnessmesh collaborate --config configs/claude-codex.json --task "Review the API" |
| configs/antigravity-codex.json | Antigravity implementation with Codex review | Antigravity | Codex | bin/harnessmesh collaborate --config configs/antigravity-codex.json --task "Implement the feature" |
| configs/copilot-codex.json | Copilot implementation with Codex review | Copilot CLI | Codex | bin/harnessmesh collaborate --config configs/copilot-codex.json --task "Fix the failing test" |
| configs/antigravity-multi-review.json | Parallel multi-review | Antigravity | Codex + Claude security reviewer | bin/harnessmesh collaborate --config configs/antigravity-multi-review.json --task "Audit the change" |
| configs/codex-multi-review.json | Codex implementation with multiple reviewers | Codex | Antigravity + Claude | bin/harnessmesh collaborate --config configs/codex-multi-review.json --task "Refactor the provider" |
| configs/antigravity-openai-peer.json | Antigravity with an OpenAI peer | Antigravity | OpenAI peer | bin/harnessmesh smoke-test antigravity-codex --config configs/antigravity-openai-peer.json |
| configs/chatgpt-claude.json | ChatGPT remote participant with local Claude executor | Claude | ChatGPT browser participant | bin/harnessmesh integrate chatgpt --config configs/chatgpt-claude.json --dry-run |
| configs/harnessmesh.example.json | General Claude/Codex starter profile | Claude | Codex | bin/harnessmesh doctor --config configs/harnessmesh.example.json |
| configs/codex-provider.example.json | Codex-compatible provider gateway | Provider gateway | — | bin/harnessmesh provider doctor --config configs/codex-provider.example.json |
| configs/harnessmesh.switchyard.example.json | Legacy Switchyard-enabled profile format | Claude | Codex | bin/harnessmesh switchyard config validate --config configs/harnessmesh.switchyard.example.json |
| configs/switchyard-example.json | Current Switchyard model-routing profile | Claude | Codex | bin/harnessmesh switchyard routes --config configs/switchyard-example.json |
| configs/no-switchyard-example.json | Direct-backend profile without Switchyard | Claude | Codex | bin/harnessmesh switchyard config validate --config configs/no-switchyard-example.json |

The profiles are templates, not guaranteed credentials. Replace executable
paths, model names, endpoints, and environment-variable references for the
local machine. Keep secrets in environment variables or the native credential
store rather than adding literal keys to JSON.

### Project configuration: harnessmesh.json

harnessmesh.json is the project-specific runtime configuration. It can
combine the following sections:

| Section | Controls |
| :--- | :--- |
| version | Configuration schema version |
| agents | Harness adapters, roles, capabilities, and writable/read-only status |
| workflow | Executor, reviewer, round limits, tests, and timeouts |
| collaboration | Peer depth, call/token/cost budgets, approvals, retries, and quotas |
| context | Projection limits and sensitive-path filtering |
| provider | Codex-compatible provider gateway and backend policy |
| switchyard | Optional model-routing service |
| model_routing_backends | Named routing backends and endpoints |
| change_control | Proof obligations and commit-gate policy |
| bridge / chatgpt | Local bridge and external ChatGPT participant settings |

Inspect a configuration without exposing sensitive values:

```bash
bin/harnessmesh config validate harnessmesh.json
bin/harnessmesh config print --config harnessmesh.json
bin/harnessmesh doctor --config harnessmesh.json
```

### MCP configuration: .mcp.json

.mcp.json configures the local MCP client for a project. It is normally
written by mcp install and points the client at harnessmesh mcp serve.
Regenerate it instead of hand-editing it:

```bash
bin/harnessmesh mcp install claude --scope project
bin/harnessmesh mcp install codex --scope project
```

User-scoped installations are stored in the harness's user configuration:

```bash
bin/harnessmesh mcp install claude --scope user
```

### Docker Compose: compose.yaml

compose.yaml provides four services. Optional services are disabled by default
and enabled through Compose profiles:

| Service | Profile | Port | Zweck |
| :--- | :--- | :--- | :--- |
| harnessmesh | — | — | Read-only configuration/diagnostic container |
| harnessmesh-mcp | mcp | 127.0.0.1:8787 | Remote Streamable HTTP MCP server |
| harnessmesh-bridge | bridge | 127.0.0.1:8788 | REST/WebSocket bridge for VS Code |
| harnessmesh-provider | provider | 127.0.0.1:8789 | Codex-compatible /v1/responses gateway |

#### 1. Repository und Konfiguration vorbereiten

Compose mountet das Repository nach `/workspace`. Für MCP und Bridge wird eine
Projektkonfiguration erwartet:

```bash
cp configs/harnessmesh.example.json harnessmesh.json
bin/harnessmesh config validate --config harnessmesh.json
```

Für den Provider mit einem lokalen oder Bedrock-Backend:

```bash
cp configs/codex-provider.example.json harnessmesh.json
bin/harnessmesh config validate --config harnessmesh.json
```

Für die vereinfachte ChatGPT-SIWC-Variante ist keine eigene
`harnessmesh.json` erforderlich:

```bash
./scripts/setup.sh
./scripts/start-codex-compose.sh
```

Das Skript verwendet automatisch
`configs/codex-chatgpt.example.json`, `.env` und `~/.harnessmesh`.

Backend-URLs müssen aus dem Container erreichbar sein. 127.0.0.1 bezeichnet
innerhalb des Containers den Container selbst; für einen Dienst auf dem Host
ist unter Docker Desktop typischerweise host.docker.internal zu verwenden.

#### 2. Image und Standardservice prüfen

```bash
docker compose build harnessmesh
docker compose run --rm harnessmesh
```

Der Standardservice führt print-config aus und gibt eine redigierte
Konfiguration aus. Die Container laufen read-only, ohne Linux-Capabilities und
mit einem temporären /tmp-Dateisystem.

#### 3. Remote-MCP starten

```bash
export HARNESSMESH_MCP_TOKEN="<long-random-token>"
docker compose --profile mcp up --build -d harnessmesh-mcp
curl -fsS http://127.0.0.1:8787/healthz
docker compose logs -f harnessmesh-mcp
```

Der MCP-Endpunkt ist http://127.0.0.1:8787/mcp. Für Remote-Zugriff muss ein
TLS-Reverse-Proxy oder ein sicherer Tunnel davorliegen. Den Entwicklungsport
nicht direkt öffentlich exponieren.

```bash
docker compose --profile mcp down
```

#### 4. VS-Code-Bridge starten

```bash
export HARNESSMESH_BRIDGE_TOKEN="<long-random-token>"
docker compose --profile bridge up --build -d harnessmesh-bridge
docker compose logs -f harnessmesh-bridge
docker compose --profile bridge down
```

Die Bridge lauscht auf 127.0.0.1:8788. Der Collaboration-State wird im
benannten Volume harnessmesh-data gespeichert.

#### 5. Provider-Gateway manuell starten

Der manuelle Ablauf ist für lokale/OpenAI-kompatible Backends oder
administrative Deployments gedacht:

```bash
export HARNESSMESH_PROVIDER_TOKEN="<long-random-token>"
docker compose --profile provider up --build -d harnessmesh-provider
curl -fsS http://127.0.0.1:8789/healthz
curl -fsS http://127.0.0.1:8789/v1/models \
  -H "Authorization: Bearer $HARNESSMESH_PROVIDER_TOKEN"
docker compose logs -f harnessmesh-provider
```

Das Provider-Gateway verwendet im zero_api_billing_mode nur erlaubte lokale
oder kompatible Backends und fällt nicht stillschweigend auf OPENAI_API_KEY
oder Codex-Abrechnung zurück.

#### 6. Backups und Restore

Das Volume `harnessmesh-data` enthält die SQLite-Datenbank und das persistente
Knowledge-Archiv. Ein Online-Backup kann ohne vorheriges Stoppen der Services
erstellt werden:

```bash
mkdir -p backups
HARNESSMESH_UID="$(id -u)" HARNESSMESH_GID="$(id -g)" \
HARNESSMESH_BACKUP_DIR="$PWD/backups" \
  docker compose --profile backup run --rm harnessmesh-backup
ls -la backups/harnessmesh-*/
```

Für einen Restore zuerst alle Services stoppen. Das Restore verlangt zusätzlich
eine Umgebungsbestätigung und führt vor dem Überschreiben eine SQLite-
Integritätsprüfung durch:

```bash
docker compose --profile mcp --profile bridge --profile provider down
HARNESSMESH_UID="$(id -u)" HARNESSMESH_GID="$(id -g)" \
HARNESSMESH_BACKUP_DIR="$PWD/backups" \
HARNESSMESH_RESTORE_SERVICES_STOPPED=1 \
  docker compose --profile backup run --rm harnessmesh-restore \
  /backup/harnessmesh-YYYYmmddTHHMMSSZ --confirm
```

Vorhandene Datenbank und Knowledge-Datei werden als `*.pre-restore-*`
gesichert. Nach dem Restore die gewünschten Profile erneut starten und
`/healthz` sowie `harnessmesh doctor` prüfen. Backups müssen außerhalb des
Containers verschlüsselt, mit Retention versehen und regelmäßig testweise
wiederhergestellt werden.

#### 7. Gemeinsamer Betrieb und Fehleranalyse

```bash
docker compose ps
docker compose --profile mcp --profile bridge --profile provider up --build -d
docker compose logs --tail=200 harnessmesh-mcp
docker compose config
docker compose --profile mcp --profile bridge --profile provider down
```

Secrets gehören in die Shell-Umgebung oder einen Secret-Manager, nicht in
compose.yaml oder Git.

### Vollständige Deinstallation und Datenlöschung

Der folgende Ablauf ist destruktiv. Er löscht lokale HarnessMesh-Daten,
Docker-Container/Volumes, Credentials, Tokens und generierte Projektdateien.
Er kann nicht rückgängig gemacht werden. Vorher benötigte Reports oder
Backups außerhalb der folgenden Pfade sichern.

#### Docker-Container, Images und Volumes entfernen

Im Repository ausführen:

```bash
docker compose --profile mcp --profile bridge --profile provider \
  down --volumes --remove-orphans --rmi local
```

Damit werden insbesondere das benannte Volume `harnessmesh-data`, die
zugehörigen Container und lokal für dieses Compose-Projekt gebaute Images
entfernt. Der gemountete SIWC-Ordner unter `~/.harnessmesh` ist davon nicht
betroffen und wird separat gelöscht.

#### Projektdateien entfernen

Nur ausführen, wenn diese Dateien ausschließlich für HarnessMesh verwendet
werden:

```bash
rm -f harnessmesh.json .env bin/harnessmesh .mcp.json codex-mcp.json
rm -rf .harnessmesh
```

Falls die Integration angelegt wurde und die Dateien nicht anderweitig
benötigt werden:

```bash
rm -f .agent/mcp_config.json .agent/rules/harnessmesh.md
```

#### Globale Daten, Datenbank und Credentials löschen

`~/.harnessmesh` enthält standardmäßig die SQLite-Datenbank
`harnessmesh.db`, `knowledge.hmkz`, Reports, Laufzeitdaten sowie die SIWC-
Credentials `chatgpt-siwc-auth.json`, Host-ID und Client-ID. Für eine
vollständige Löschung:

```bash
rm -rf "$HOME/.harnessmesh"
```

Das löscht auch Daten anderer HarnessMesh-Projekte, die denselben globalen
Ordner verwenden.

#### Codex-Konfiguration bereinigen

`~/.codex/config.toml` nicht blind löschen, weil dort auch andere Codex-
Einstellungen stehen können. Entferne den HarnessMesh-Eintrag manuell oder
stelle den von `integrate codex-provider` erzeugten Backup-Stand wieder her:

```bash
ls -1t "$HOME"/.codex/config.toml.bak-* 2>/dev/null | head
```

Zu entfernen sind der Block `[model_providers.harnessmesh]` sowie die von
HarnessMesh gesetzten Werte `model_provider = "harnessmesh"` und das damit
verbundene HarnessMesh-Modell. Wenn Codex ausschließlich für HarnessMesh
installiert wurde, können zusätzlich die gesamte Codex-Konfiguration und die
Codex-Erweiterung nach den jeweiligen Codex-/VS-Code-Anweisungen entfernt
werden.

#### Prüfen, ob noch HarnessMesh-Reste vorhanden sind

```bash
docker ps -a --filter name=harnessmesh
docker volume ls --filter name=harnessmesh
find "$HOME/.harnessmesh" -maxdepth 2 -print 2>/dev/null
```

Wenn die letzten beiden Befehle keine Ausgabe liefern und im Projekt keine
der oben genannten Dateien mehr vorhanden ist, sind Datenbank, Secrets,
Tokens und Compose-Ressourcen entfernt.

### Codex VS Code Extension mit ChatGPT-Plan-Inferenz

#### Kurzsetup: Container starten und VS Code verbinden

Für den normalen lokalen Betrieb übernimmt dieses Skript die wiederkehrende
Konfiguration. Es erzeugt einmalig `.env`, integriert den Provider in die
benutzerspezifische Codex-Konfiguration, startet den Compose-Container und
führt den ChatGPT-Login bei Bedarf auf dem Host aus:

```bash
./scripts/setup.sh
./scripts/start-codex-compose.sh
```

Beim ersten Lauf öffnet sich der SIWC-Login. Danach werden die Zugangsdaten
unter `~/.harnessmesh` wiederverwendet. Das Provider-Token liegt in der lokal
ignorierten `.env` und wird über `env_key = "HARNESSMESH_PROVIDER_TOKEN"`
von Codex aus der Umgebung gelesen; es wird absichtlich nicht als Klartext in
`config.toml` gespeichert.

VS Code muss mit derselben Umgebung gestartet werden:

```bash
set -a; . ./.env; set +a
code .
```

Alternativ startet dieser Befehl den Container und VS Code zusammen:

```bash
HARNESSMESH_OPEN_CODE=1 ./scripts/start-codex-compose.sh .
```

Der Container verwendet standardmäßig
`configs/codex-chatgpt.example.json`. Für ein anderes Provider-Profil kann in
`.env` beispielsweise gesetzt werden:

```dotenv
HARNESSMESH_PROVIDER_CONFIG=configs/codex-provider.example.json
```

Dieser Ablauf verwendet die offizielle Sign-in-with-ChatGPT-(SIWC)-Inferenz
über HarnessMesh als Custom Provider der Codex VS Code Extension:

```text
Codex VS Code Extension
        | custom provider
        v
HarnessMesh Provider Gateway
        | chatgpt-subscription / SIWC
        v
ChatGPT-Plan-Inferenz
```

Das ist nicht dasselbe wie ein bereits geöffneter Browser-Chat. SIWC liefert
keinen Zugriff auf ChatGPT-Verlauf, Memory, Custom Instructions oder den
Kontext einer bestehenden Unterhaltung. Für ChatGPT als kollaborierenden
Teilnehmer ist stattdessen die separate MCP-Integration vorgesehen.

#### 1. Provider-Konfiguration

Erstelle oder kopiere eine harnessmesh.json mit einem ChatGPT-Subscription-Backend:

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
    "default_backend": "chatgpt",
    "backends": {
      "chatgpt": {
        "type": "chatgpt-subscription"
      }
    }
  }
}
```

Validiere die Datei:

```bash
bin/harnessmesh config validate --config harnessmesh.json
```

#### 2. ChatGPT-Plan-Zugriff autorisieren

Führe den Login auf dem Host-System aus:

```bash
bin/harnessmesh provider auth chatgpt
```

Öffne die ausgegebene URL, melde dich an und bestätige die ChatGPT-Plan-Nutzung.
Die Zugangsdaten werden standardmäßig unter
`~/.harnessmesh/chatgpt-siwc-auth.json` gespeichert.

#### 3. Provider-Gateway starten

Für SIWC wird zunächst der Host-Betrieb empfohlen, weil dort der Browser-
Callback und der lokale Credential-Store direkt verfügbar sind:

```bash
export HARNESSMESH_PROVIDER_TOKEN="$(openssl rand -hex 32)"
bin/harnessmesh provider serve \
  --config harnessmesh.json \
  --listen 127.0.0.1:8789 \
  --token "$HARNESSMESH_PROVIDER_TOKEN"
```

Prüfe die Bereitschaft:

```bash
bin/harnessmesh provider doctor --config harnessmesh.json
curl http://127.0.0.1:8789/readyz
```

#### 4. Codex VS Code Extension verbinden

Generiere oder aktualisiere die benutzerspezifische Codex-Konfiguration:

```bash
bin/harnessmesh integrate codex-provider \
  --scope user \
  --listen 127.0.0.1:8789 \
  --model harnessmesh-chatgpt
```

Exportiere den gleichen Token in der Umgebung, aus der VS Code/Codex gestartet
wird, und starte die Extension neu:

```bash
export HARNESSMESH_PROVIDER_TOKEN="<der-provider-token>"
```

Die relevante Codex-Konfiguration enthält dann:

```toml
model = "harnessmesh-chatgpt"
model_provider = "harnessmesh"

[model_providers.harnessmesh]
name = "HarnessMesh"
base_url = "http://127.0.0.1:8789/v1"
wire_api = "responses"
env_key = "HARNESSMESH_PROVIDER_TOKEN"
```

#### Docker-Hinweis

Der Compose-Provider ist standardmäßig für lokale oder OpenAI-kompatible
Backends vorbereitet. Für `chatgpt-subscription` muss der Container zusätzlich
auf den SIWC-Credential-Store zugreifen können. Die Datei muss wegen möglicher
Token-Erneuerung beschreibbar sein; der Browser-Login sollte daher zunächst
auf dem Host erfolgen.

Der einfachste Ablauf ist deshalb:

- Codex VS Code Extension auf dem Host
- SIWC-Login und Credential-Store auf dem Host
- HarnessMesh Provider Gateway auf dem Host
- lokale Inferenz-Backends optional per Docker Compose

Die Compose-Variante für lokale Backends ist im Abschnitt Docker Compose oben
beschrieben. Weitere SIWC-Limits und Abrechnungssemantik stehen in
`docs/codex-provider.md`.

### Switchyard route files

configs/switchyard.routes.example.toml is a standalone NVIDIA NeMo
Switchyard server configuration. It defines LLM clients, targets, and routes
for routine, coding, architecture, and security work:

```bash
switchyard-server \
  --config configs/switchyard.routes.example.toml \
  --host 127.0.0.1 --port 4000

bin/harnessmesh switchyard doctor \
  --config configs/switchyard-example.json
bin/harnessmesh switchyard routes \
  --config configs/switchyard-example.json
```

configs/switchyard.routes.toml is the local route configuration when using
the non-example setup. Never commit literal provider API keys; use the
configured environment-variable references such as OPENROUTER_API_KEY.

---

# 27. Build, Test & Release Engineering

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

# 28. Design Invariants

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

# 29. Documentation

## Architecture and collaboration

- [Architecture](docs/ARCHITECTURE.md)
- [Collaboration Spaces](docs/collaboration-spaces.md)
- [Channels & Threads](docs/channels-and-threads.md)
- [Events & Coalescing](docs/events-and-coalescing.md)
- [Inbox & Activation](docs/inbox-and-activation.md)
- [Decisions & Evidence](docs/decisions-and-evidence.md)
- [Human Supervision & Emergency Controls](docs/human-controls.md)

### Core Architecture & Integration
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

# 30. What HarnessMesh Does Not Promise

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

Apache 2.0. See [LICENSE](LICENSE).
