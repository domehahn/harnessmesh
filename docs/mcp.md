# Model Context Protocol (MCP) Server

HarnessMesh v0.2.0 exposes a standards-compliant Model Context Protocol (MCP) server over standard input and output (`stdio`).

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
