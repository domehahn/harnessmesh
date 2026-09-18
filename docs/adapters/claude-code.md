# Claude Code Adapter

The Claude Code adapter integrates Anthropic's Claude Code CLI (`claude`) into the HarnessMesh collaboration fabric.

## Identifier & Registration

- **Adapter Type**: `claude-code` (aliases: `claude`, `claude_code`)
- **CLI Executable**: `claude` (configurable via `command`)
- **Supported Roles**: `executor` (writable), `reviewer` (read-only), `peer`

## Capabilities

- Repository reading (`read_repository: true`)
- Repository modification (`write_repository: true` when role is `executor`)
- Command execution (`run_commands: true`)
- Structured code review (`review: true`)
- Question answering (`answer_questions: true`)
- Evidence submission (`submit_evidence: true`)

## Configuration

In `harnessmesh.json`:

```json
{
  "agents": {
    "claude-executor": {
      "adapter": "claude-code",
      "role": "executor",
      "roles": ["executor"],
      "writable": true,
      "command": "claude",
      "mode": "acceptEdits",
      "timeout_minutes": 45
    }
  }
}
```

## MCP Setup

To install the HarnessMesh MCP server for Claude Code:

```bash
# Project-level configuration (writes .mcp.json in the repository root)
harnessmesh mcp install claude --scope project

# User-level global configuration
harnessmesh mcp install claude --scope user
```

Claude Code will automatically discover all 10 HarnessMesh tools (`peer.list`, `peer.capabilities`, `peer.ask`, `peer.reply`, `peer.request_review`, `peer.submit_finding`, `peer.submit_evidence`, `peer.challenge`, `peer.resolve`, `peer.status`).

## Diagnostics & Health

- `harnessmesh doctor` checks if `claude` binary exists and responds.
- Invocations capture stderr and structured output to surface actionable diagnostics when errors occur.

