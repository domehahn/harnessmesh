# Model Context Protocol (MCP) Server

HarnessMesh exposes a standards-compliant Model Context Protocol (MCP) server over standard input/output (`stdio`) and, when explicitly enabled, authenticated HTTP.

## Remote HTTP mode

```bash
export HARNESSMESH_MCP_TOKEN="$(openssl rand -hex 32)"
harnessmesh mcp serve --listen 127.0.0.1:8787 --token "$HARNESSMESH_MCP_TOKEN"
```

Remote JSON-RPC calls use `POST /` and `Authorization: Bearer <token>`. `GET /healthz` can be used for liveness checks and `GET /metrics` exposes request/error counters. Use TLS termination and network access controls outside HarnessMesh when exposing this beyond localhost.

The authenticated `/admin` dashboard shows operational state. Set `HARNESSMESH_MCP_ADMIN_CALLERS` to a comma-separated caller allowlist to restrict administrative access further; the regular project/caller ACLs remain active for MCP tools.

OAuth2 resource-server deployments may set `HARNESSMESH_MCP_OAUTH_INTROSPECTION_URL`; HarnessMesh then validates bearer tokens through the configured introspection endpoint. `HARNESSMESH_MCP_OAUTH_CLIENT_SECRET` optionally authenticates that introspection request.

## Exposed Tools (11 Tools)

| Tool | Purpose | Primary Inputs |
| :--- | :--- | :--- |
| `peer.converse` | Interactive conversation with peer (thread continuity across turns) | `message`, `peer`, `capability`, `scope`, `context`, `expected_outcome`, `causation_id` |
| `peer.list` | List all known peer agents, adapters, roles, and status | `session_id` (optional) |
| `peer.capabilities` | Discover peers matching a capability or role | `capability` (required) |
| `peer.ask` | Ask a peer agent for targeted inspection while working | `peer` or `capability`, `question`, `scope`, `context` (`include_diff`, `include_tests`, `include_git_status`), `idempotency_key` |
| `peer.reply` | Causal reply linked to parent message | `parent_message_id`, `target_peer`, `message`, `evidence_references` |
| `peer.request_review` | Request structured review from one or multiple peers | `peer` or `capability` or `reviewers`, `scope`, `focus`, `include_diff`, `include_tests`, `minimum_severity` |
| `peer.submit_finding` | Publish structured finding into shared session | `id`, `severity`, `category`, `claim`, `evidence`, `recommendation`, `file`, `line` |
| `peer.submit_evidence` | Submit verifiable repository evidence | `finding_id`, `type`, `command`, `result`, `excerpt`, `exit_code` |
| `peer.challenge` | Challenge a finding and mark as disputed | `finding_id`, `claim`, `evidence`, `requested_verification` |
| `peer.resolve` | Resolve an open or disputed finding | `finding_id`, `status` (`confirmed`, `rejected`, etc.), `rationale`, `evidence_references` |
| `peer.status` | Inspect session status, counts, and budget | *(none)* |
| `operations.status` | Inspect agent health, quota waits, circuit breakers, retry backlog, and metrics | *(none)* |
| `operations.approvals` | List human approval requests | `status` |
| `operations.approve` | Approve or reject an operation | `approval_id`, `approved` |
| `knowledge.search_advanced` | Search knowledge by time and source metadata | `query`, `since`, `until`, `agent`, `source`, `limit` |
| `operations.dead_letters` | List permanently failed deliveries | `limit` |
| `operations.requeue_dead_letter` | Requeue a dead-letter delivery | `id`, `priority` |

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

### 5. Manual Server Execution

You can run the MCP server directly via stdio:

```bash
harnessmesh mcp serve --repo . --session hm_12345 --caller claude
```
