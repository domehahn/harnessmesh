# HarnessMesh Wiki

HarnessMesh is a **persistent multi-agent collaboration fabric for agentic coding harnesses**.

It lets tools such as **Google Antigravity, OpenAI Codex, Claude Code, and GitHub Copilot CLI** communicate through a shared collaboration plane using MCP, persistent channels, threads, events, findings, evidence, and durable session state — without a human copying messages between chat windows.

## Start here

- [Getting Started](Getting-Started.md)
- [Architecture](Architecture.md)
- [Collaboration Spaces](Collaboration-Spaces.md)
- [Agent Integrations](Agent-Integrations.md)
- [MCP Tools](MCP-Tools.md)
- [Switchyard](Switchyard.md)
- [Security and Safety](Security-and-Safety.md)
- [Troubleshooting](Troubleshooting.md)

## Core idea

```text
User
  |
  v
Primary Harness (e.g. Antigravity)
  |
  v
HarnessMesh
  |-- shared channels / threads
  |-- peer conversations
  |-- findings / evidence
  |-- events / subscriptions
  |-- persistent state
  |
  +--> Codex
  +--> Claude Code
  +--> Copilot CLI
  +--> future harnesses
```

HarnessMesh chooses **which participant/harness** should collaborate. Optional NVIDIA NeMo Switchyard can choose **which model/provider** serves a compatible harness path.

## Design principles

1. The human is a participant, not the message bus.
2. Agents exchange structured, persistent collaboration state.
3. Evidence outranks model opinion.
4. Exactly one writer owns a shared worktree.
5. Autonomous loops are bounded and auditable.
6. Secrets and denied paths stay out of peer context.
7. Harness selection and model routing are separate concerns.
8. Adding another harness should require an adapter, not a rewrite of the collaboration core.

For exhaustive reference material, see the repository's [docs](../) directory.
