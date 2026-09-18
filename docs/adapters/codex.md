# OpenAI Codex Adapter

The OpenAI Codex adapter integrates the Codex CLI (`codex`) into the HarnessMesh collaboration fabric.

## Identifier & Registration

- **Adapter Type**: `codex` (alias: `openai-codex`)
- **CLI Executable**: `codex` (configurable via `command`)
- **Supported Roles**: `reviewer` (read-only), `executor` (writable), `peer`

## Capabilities

- Repository reading (`read_repository: true`)
- Structured code review (`review: true`)
- Question answering (`answer_questions: true`)
- Evidence submission (`submit_evidence: true`)
- Structured output conformance (`additionalProperties: false`, required fields)

## Configuration

In `harnessmesh.json`:

```json
{
  "agents": {
    "codex-reviewer": {
      "adapter": "codex",
      "role": "reviewer",
      "roles": ["reviewer", "correctness_review"],
      "writable": false,
      "command": "codex",
      "mode": "read-only",
      "timeout_minutes": 45
    }
  }
}
```

## Structured Outputs Conformance

Codex uses JSON schema outputs adhering to strict OpenAI Structured Outputs requirements:
- All schema object properties are listed in `required`.
- `additionalProperties: false` is enforced on all object schemas.
- File and line fields are non-optional in schema shape with nullable fallbacks.

## Persistent Thread Continuation

When operating as an interactive peer in multi-turn conversations (`peer.converse`), Codex supports thread continuation:
- **Initial Turn**: HarnessMesh starts a thread and extracts `thread_id` from the Codex event stream.
- **Follow-up Turns**: HarnessMesh executes `codex exec resume <thread_id> -` with the new prompt passed via stdin.
- The session context is fully retained across multiple turns without needing to resend previous prompts.

## MCP Setup

To generate MCP configuration templates for Codex:

```bash
harnessmesh mcp install codex --scope project
```

This generates `codex-mcp.json` referencing `harnessmesh mcp serve --caller codex`.

## Diagnostics & Actionable Errors

- `harnessmesh doctor` tests `codex --version` and checks authentication status.
- Typed errors classify failure modes:
  - `HarnessAuthenticationRequiredError`: Codex CLI is not authenticated (`codex login` or `OPENAI_API_KEY` required).
  - `PeerUnavailableError`: Codex binary is missing from `PATH` or cannot be executed.
  - `PeerTimeoutError`: Codex exceeded the configured deadline.
  - `MalformedPeerResponseError`: Stderr and JSON diagnostic streams are analyzed instead of failing with raw exit codes.

