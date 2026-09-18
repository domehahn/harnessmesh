# MCP Tools

HarnessMesh exposes a standards-compliant MCP server over stdio.

## Collaboration-space tools

- `collaboration.publish`
- `collaboration.reply`
- `collaboration.inbox`
- `collaboration.channels`
- `collaboration.thread`
- `collaboration.subscribe`
- `collaboration.unsubscribe`
- `collaboration.decide`
- `collaboration.status`

## Peer tools

- `peer.converse`
- `peer.list`
- `peer.capabilities`
- `peer.ask`
- `peer.reply`
- `peer.request_review`
- `peer.submit_finding`
- `peer.submit_evidence`
- `peer.challenge`
- `peer.resolve`
- `peer.status`

## When to use which abstraction

Use collaboration-space tools for persistent, asynchronous, channel/thread-oriented work.

Use peer tools for targeted synchronous interaction with a specific participant or capability.

Existing peer APIs remain useful and can coexist with collaboration spaces.

## Run the server manually

```bash
bin/harnessmesh mcp serve \
  --repo . \
  --config configs/antigravity-openai-peer.json \
  --caller antigravity
```

For the exact schemas and arguments, see [docs/mcp.md](../mcp.md).
