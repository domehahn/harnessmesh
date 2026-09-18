# Getting Started

## Prerequisites

HarnessMesh is a local-first Go application. Install the coding harnesses you want to connect and authenticate them normally.

Common participants:

- Google Antigravity
- OpenAI Codex
- Claude Code
- GitHub Copilot CLI

At least one configured participant must be writable; all additional participants should normally be read-only.

## Build

```bash
go build -trimpath -o bin/harnessmesh ./cmd/harnessmesh
bin/harnessmesh version
```

## Validate the environment

```bash
bin/harnessmesh doctor
```

Doctor checks repository access, SQLite state, configured adapters, MCP integration, and model-routing backends.

## Antigravity + OpenAI Codex

Install the project-local Antigravity integration:

```bash
bin/harnessmesh integrate antigravity \
  --config configs/antigravity-openai-peer.json \
  --repo .
```

This installs the HarnessMesh MCP configuration and project collaboration rule for Antigravity.

Then reload the Antigravity/VS Code environment and work normally in the Antigravity chat. HarnessMesh can use Codex as a background peer without requiring copy/paste.

## Direct peer test

```bash
bin/harnessmesh peer ask \
  --config configs/antigravity-openai-peer.json \
  --peer openai-reviewer \
  --caller antigravity \
  --question "Review the current architecture for production-readiness risks."
```

## Collaboration-space CLI

Useful inspection commands include:

```bash
bin/harnessmesh space list
bin/harnessmesh channel list <space-id>
bin/harnessmesh inbox <participant>
bin/harnessmesh session list
bin/harnessmesh findings <session-id>
bin/harnessmesh evidence <session-id>
```

See [Collaboration Spaces](Collaboration-Spaces.md) for the shared-space model.
