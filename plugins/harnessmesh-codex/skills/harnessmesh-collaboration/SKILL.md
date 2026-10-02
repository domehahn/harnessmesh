---
name: harnessmesh-collaboration
description: Use HarnessMesh MCP tools to coordinate coding-agent work, inspect shared status, exchange messages and preserve evidence-backed decisions.
---

# HarnessMesh collaboration

Use this skill when the user asks about shared agent work, pending requests,
collaboration status, reviews, findings, messages or durable project knowledge.

## Workflow

1. Start with `collaboration.status` for the current shared-space state.
2. Use `collaboration.inbox` for pending messages and mentions.
3. Use `peer.list` or `peer.capabilities` before asking a specific peer for help.
4. Use `peer.ask` or `peer.request_review` for targeted work.
5. Publish important conclusions with `collaboration.publish` or persist a durable lesson with `knowledge.remember`.

Keep the authenticated caller identity intact. Do not claim to be another
participant or copy another participant's identity into request fields.
