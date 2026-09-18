# Google Antigravity Adapter

The Google Antigravity adapter integrates the Antigravity CLI (`agy` or `antigravity`) into the HarnessMesh collaboration fabric.

## Identifier & Registration

- **Adapter Type**: `antigravity` (aliases: `agy`, `google-antigravity`)
- **CLI Executable**: `agy` (falls back to `antigravity`, configurable via `command`)
- **Supported Roles**: `executor` (writable), `reviewer` (read-only), `peer`

## Capabilities

- Repository reading (`read_repository: true`)
- Repository modification (`write_repository: true` when configured as executor)
- Command execution (`run_commands: true`)
- Security & architecture review (`review: true`, `security_review: true`)
- Targeted question answering (`answer_questions: true`)
- Evidence submission (`submit_evidence: true`)

## Invocation Flags & Execution Contract

Antigravity CLI is invoked non-interactively with:
- `-p <prompt>`: Headless prompt execution.
- `--dangerously-skip-permissions`: Autonomous headless execution when operating as an executor.
- Structured output parsing: Extracts markdown or JSON review payloads and finding evidence from the execution trajectory.

## Configuration

In `harnessmesh.json`:

```json
{
  "agents": {
    "antigravity-executor": {
      "adapter": "antigravity",
      "role": "executor",
      "roles": ["executor"],
      "writable": true,
      "command": "agy",
      "timeout_minutes": 45
    }
  }
}
```

Or as a dedicated reviewer with security focus:

```json
{
  "agents": {
    "antigravity-security": {
      "adapter": "antigravity",
      "role": "reviewer",
      "roles": ["reviewer", "security_review"],
      "writable": false,
      "command": "agy",
      "timeout_minutes": 45
    }
  },
  "capability_routing": {
    "security_review": ["antigravity-security"]
  }
}
```

## Autonomous Peer Collaboration (VS Code)

Antigravity operates as the primary user-facing agent within the VS Code Antigravity chat. Via HarnessMesh MCP, Antigravity autonomously queries peers (e.g. OpenAI Codex) without requiring human intervention or copy-pasting.

### One-Command Workspace Integration

```bash
# Integrates MCP configuration (.agent/mcp_config.json) and collaboration rules (.agent/rules/harnessmesh.md)
harnessmesh integrate antigravity --config configs/antigravity-openai-peer.json --repo .
```

### Manual MCP Setup

To configure HarnessMesh MCP server integration for Antigravity:

```bash
# Project-level configuration (writes .agent/mcp_config.json)
harnessmesh mcp install antigravity --scope project

# User-level global configuration (writes ~/.gemini/antigravity/mcp_config.json)
harnessmesh mcp install antigravity --scope user
```

## Diagnostics & Health

- `harnessmesh doctor` verifies Antigravity integration status (`.agent/mcp_config.json`, `.agent/rules/harnessmesh.md`), CLI executable presence (`agy` / `antigravity`), and credentials (`GOOGLE_API_KEY`, `GEMINI_API_KEY`, or gcloud auth).
- `harnessmesh smoke-test antigravity-codex` validates end-to-end communication readiness between Antigravity and the Codex peer.
- Execution failures surface detailed stderr context in the session log and error payloads.

