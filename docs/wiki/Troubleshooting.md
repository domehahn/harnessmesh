# Troubleshooting

## Start with doctor

```bash
bin/harnessmesh doctor
```

Doctor reports adapter availability, authentication readiness where detectable, MCP installation, repository health, SQLite state, and model-routing backends.

## Antigravity CLI warning

If Antigravity is used primarily through the VS Code extension, the CLI may not be installed even though MCP integration is configured. The MCP path can still be tested independently. Headless HarnessMesh-driven Antigravity invocation requires the CLI.

## Codex exits non-zero

Run Codex directly to verify authentication and automation readiness, then use HarnessMesh diagnostics. HarnessMesh should preserve safe stderr instead of reducing failures to only an exit code.

## Switchyard disabled

This is valid. HarnessMesh can operate without Switchyard using fixed or externally controlled model routing.

## Agent conversation stalls

Inspect:

```bash
bin/harnessmesh session list
bin/harnessmesh session show <session-id>
bin/harnessmesh findings <session-id>
bin/harnessmesh evidence <session-id>
```

Check peer-depth, call, conversation-turn, timeout, and budget limits before increasing them.

## MCP configuration

For Antigravity, project integration is installed with:

```bash
bin/harnessmesh integrate antigravity --repo .
```

For deeper diagnostics, see [docs/troubleshooting.md](../troubleshooting.md).
