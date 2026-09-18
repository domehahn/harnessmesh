# GitHub Copilot CLI Adapter

The GitHub Copilot CLI adapter integrates the GitHub Copilot CLI (`copilot` or `gh copilot`) into the HarnessMesh collaboration fabric.

## Identifier & Registration

- **Adapter Type**: `copilot-cli` (aliases: `copilot`, `gh-copilot`)
- **CLI Executable**: `copilot` (falls back to `gh copilot`, configurable via `command`)
- **Supported Roles**: `executor` (writable), `reviewer` (read-only), `peer`

## Capabilities

- Repository reading (`read_repository: true`)
- Repository modification (`write_repository: true` when configured as executor)
- Command execution (`run_commands: true`)
- Code review and verification (`review: true`)
- Question answering (`answer_questions: true`)
- Evidence submission (`submit_evidence: true`)

## Invocation Flags & Execution Contract

The Copilot CLI adapter supports non-interactive execution:
- Executed via `copilot` or `gh copilot`.
- Passes prompt via `-p <prompt>` or standard headless arguments (`--allow-all-tools`, `--yes`).
- Captures output and parses structured review findings or command results.

## Configuration

In `harnessmesh.json`:

```json
{
  "agents": {
    "copilot-executor": {
      "adapter": "copilot-cli",
      "role": "executor",
      "roles": ["executor"],
      "writable": true,
      "command": "copilot",
      "timeout_minutes": 45
    }
  }
}
```

Or as a reviewer:

```json
{
  "agents": {
    "copilot-reviewer": {
      "adapter": "copilot-cli",
      "role": "reviewer",
      "roles": ["reviewer", "test_verification"],
      "writable": false,
      "command": "copilot",
      "timeout_minutes": 45
    }
  }
}
```

## MCP Setup

To install MCP configuration for Copilot CLI:

```bash
# Project-level configuration (writes .copilot/mcp.json)
harnessmesh mcp install copilot --scope project

# User-level global configuration
harnessmesh mcp install copilot --scope user
```

## Diagnostics & Health

- `harnessmesh doctor` tests `copilot --version` or `gh copilot --version`.
- Verifies GitHub CLI authentication via `gh auth status` or `GITHUB_TOKEN`.

