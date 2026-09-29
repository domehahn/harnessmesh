# Model Context Protocol (MCP) Server

HarnessMesh exposes a standards-compliant Model Context Protocol (MCP) server over three transports:

| Transport | Path | Use case |
| :--- | :--- | :--- |
| `stdio` | n/a | Default. Claude Code, Codex CLI, Antigravity, Copilot CLI - any local harness launching HarnessMesh as a subprocess. |
| Legacy JSON-RPC over HTTP | `POST /` | Authenticated HTTP for existing integrations; kept for backwards compatibility. |
| **Streamable HTTP** | `POST/GET/DELETE /mcp` | The spec-compliant remote transport, including SSE, session management, and strict CORS. This is what a remote peer such as ChatGPT connects to. See [docs/chatgpt-integration.md](chatgpt-integration.md). |

All three transports serve the exact same tool surface and go through the exact same collaboration engine, authorization checks, and audit trail - there is no reduced-capability "remote mode."

## Remote / Streamable HTTP mode

```bash
export HARNESSMESH_MCP_TOKEN="$(openssl rand -hex 32)"
harnessmesh mcp serve --listen 127.0.0.1:8787 --token "$HARNESSMESH_MCP_TOKEN"
```

- `POST /mcp` - JSON-RPC 2.0 requests (`initialize`, `notifications/initialized`, `ping`, `tools/list`, `tools/call`).
- `GET /mcp` with `Accept: text/event-stream` - opens an SSE stream for the session; without that header, returns a small JSON status/info document.
- `DELETE /mcp` - terminates a session.
- `OPTIONS /mcp` - CORS preflight.

All requests require `Authorization: Bearer <token>` (constant-time compared). `GET /healthz` is unauthenticated for liveness checks. `GET /metrics` exposes Prometheus-text counters, including `harnessmesh_metered_backend_calls_total{backend="openai-api"|"codex"}` (see [Credit isolation](chatgpt-integration.md#credit-isolation)).

The legacy `POST /` endpoint and the `/mcp` endpoint share the same bearer-token / OAuth-introspection requirement: `serveHTTP` refuses to start at all if neither a token nor `HARNESSMESH_MCP_OAUTH_INTROSPECTION_URL` is configured. There is no way to run an unauthenticated remote MCP server.

### Protocol version negotiation

HarnessMesh supports MCP protocol revisions `2025-06-18` (default), `2025-03-26`, and `2024-11-05`. A client's `Mcp-Protocol-Version` header (or `initialize` params) is honored if supported; an unsupported version is rejected with a JSON-RPC-shaped `400`, not silently echoed back.

### Rate limits and payload limits

- Fixed-window rate limit, default 120 requests/minute per server process, configurable via `HARNESSMESH_MCP_RATE_LIMIT`. Exceeding it returns `429` with `Retry-After: 60`.
- Request bodies are capped at 10 MiB (`http.MaxBytesReader`).

### Authentication

- **Bearer token** (`HARNESSMESH_MCP_TOKEN` / `--token`): mandatory unless OAuth introspection is configured.
- **OAuth2 token introspection** (RFC 7662-style): set `HARNESSMESH_MCP_OAUTH_INTROSPECTION_URL` to validate bearer tokens against an external authorization server; `HARNESSMESH_MCP_OAUTH_CLIENT_SECRET` optionally authenticates the introspection request itself. This is introspection against an external AS, not a full local authorization-code/dynamic-client-registration flow.
- **Caller/project ACLs**: `HARNESSMESH_MCP_CALLERS` and `HARNESSMESH_MCP_PROJECTS` are comma-separated allowlists enforced per request.
- **Admin dashboard** (`/admin/*`): requires the bearer token plus, optionally, a further `HARNESSMESH_MCP_ADMIN_CALLERS` allowlist.

### Authorization beyond authentication

Authentication proves *who* is calling; it does not by itself grant write access. Every mutating tool re-validates the caller's claimed identity against the authenticated session (rejecting any `from`/`caller`/`source_*` field that doesn't match), and repository-changing operations (`change.*`) additionally enforce the single-writer invariant against the caller's own `execution_mode`/`writable` configuration - an `execution_mode: external` participant (the ChatGPT-browser role) can never open or commit a change transaction, regardless of what token it authenticates with.

## Exposed tools

HarnessMesh's tool surface (`tools/list`) currently exposes 41 tools, grouped by domain. A remote peer such as ChatGPT is exposed the same list; authorization (not tool visibility) is what limits what it can actually do - see [docs/chatgpt-integration.md](chatgpt-integration.md) for the ChatGPT-specific permission matrix.

### `peer.*` - direct peer interaction (managed harnesses only; blocked for `execution_mode: external`)

| Tool | Purpose |
| :--- | :--- |
| `peer.list` | List known peer agents, adapters, roles, and status |
| `peer.capabilities` | Discover peers matching a capability or role |
| `peer.ask` | Ask a peer for targeted inspection |
| `peer.request_review` | Request structured review from one or more peers |
| `peer.submit_finding` | Publish a structured finding |
| `peer.submit_evidence` | Submit verifiable repository evidence for a finding |
| `peer.challenge` | Challenge a finding, marking it disputed |
| `peer.resolve` | Resolve an open or disputed finding |
| `peer.reply` | Causal reply linked to a parent message |
| `peer.converse` | Interactive conversation with thread continuity |
| `peer.status` | Session status, counts, and budget |

### `collaboration.*` - shared workspace, channels, inbox

| Tool | Purpose |
| :--- | :--- |
| `collaboration.publish` | Publish a message to a channel, with `@mentions` |
| `collaboration.reply` | Reply within a thread |
| `collaboration.inbox` | Pull pending/unread items (the passive-participant pull model - see chatgpt-integration.md) |
| `collaboration.channels` | List channels in a space |
| `collaboration.thread` | Fetch a thread |
| `collaboration.subscribe` / `collaboration.unsubscribe` | Manage event subscriptions |
| `collaboration.decide` | Propose/accept a shared decision |
| `collaboration.status` | Space-level status |

### `knowledge.*` - shared knowledge archive

`knowledge.search`, `knowledge.context`, `knowledge.stats`, `knowledge.remember`, `knowledge.import`, `knowledge.compact`, `knowledge.summary`, `knowledge.quality`, `knowledge.search_advanced`. These operate over HarnessMesh's own local/provider-neutral index; they never require or trigger an external embedding API call (see [Credit isolation](chatgpt-integration.md#credit-isolation)).

### `operations.*` - operational visibility (typically admin-only via caller ACL)

`operations.status`, `operations.approvals`, `operations.approve`, `operations.dead_letters`, `operations.requeue_dead_letter`.

### `change.*` - MeshCommit evidence-gated change transactions

`change.create`, `change.prepare`, `change.status`, `change.diff`, `change.verify`, `change.evidence`, `change.abort`, `change.commit_status`. See [docs/meshcommit.md](meshcommit.md). `change.create` and the commit path enforce the single-writer invariant against the caller's own agent configuration - see [Authorization beyond authentication](#authorization-beyond-authentication) above.

## Quick Setup

HarnessMesh provides automated MCP configuration installation for all supported harness environments:

### 1. Google Antigravity (VS Code) Setup

```bash
# Recommended: Full integration with MCP config and collaboration rules
harnessmesh integrate antigravity --config configs/antigravity-openai-peer.json --repo .

# Or MCP server registration only
harnessmesh mcp install antigravity --scope project
```

This safely updates `.agent/mcp_config.json` (preserving existing servers) and installs `.agent/rules/harnessmesh.md`.

### 2. Claude Code Setup

```bash
# Project-level configuration (writes .mcp.json)
harnessmesh mcp install claude --scope project

# User-level global configuration
harnessmesh mcp install claude --scope user
```

Claude Code will automatically detect and load all `peer.*` tools upon startup.

### 3. OpenAI Codex Setup

```bash
harnessmesh mcp install codex --scope project
```

Generates `codex-mcp.json` configuring Codex with HarnessMesh stdio integration.

### 4. GitHub Copilot CLI Setup

```bash
# Project-level configuration (writes .copilot/mcp.json)
harnessmesh mcp install copilot --scope project

# User-level global configuration
harnessmesh mcp install copilot --scope user
```

### 5. ChatGPT (remote MCP) Setup

See [docs/chatgpt-integration.md](chatgpt-integration.md) for the full setup, security model, and credit-isolation guarantees.

### 6. Manual Server Execution

You can run the MCP server directly via stdio:

```bash
harnessmesh mcp serve --repo . --session hm_12345 --caller claude
```

Or over Streamable HTTP for a remote peer:

```bash
harnessmesh mcp serve --listen 127.0.0.1:8787 --token "$HARNESSMESH_MCP_TOKEN"
```
