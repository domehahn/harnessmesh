# Agent Integrations

HarnessMesh exposes a generic harness abstraction so collaboration logic does not depend on vendor-specific CLI behavior.

## First-class adapters

| Harness | Typical use |
|---|---|
| Google Antigravity | interactive primary agent, executor, reviewer |
| OpenAI Codex | reviewer, architecture, correctness, executor |
| Claude Code | executor, reviewer, testing/analysis peer |
| GitHub Copilot CLI | reviewer, security/testing peer, optional executor |

## Antigravity

Recommended project setup:

```bash
bin/harnessmesh integrate antigravity \
  --config configs/antigravity-openai-peer.json \
  --repo .
```

The user can remain entirely in the Antigravity VS Code chat while Codex or other participants collaborate in the background.

## Codex

Codex can be used as a persistent peer/reviewer. HarnessMesh stores the external Codex thread/session mapping so follow-up turns can preserve continuity where supported.

## Claude Code

Install MCP project configuration with:

```bash
bin/harnessmesh mcp install claude --scope project
```

## GitHub Copilot CLI

Install project configuration with:

```bash
bin/harnessmesh mcp install copilot --scope project
```

## Adding another harness

A new harness should implement the generic adapter interface and declare capabilities. Collaboration-space, evidence, routing, and persistence logic should not require vendor-specific changes.
