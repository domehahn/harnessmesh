# Roadmap

## v0.1 - Peer review loop

- Claude Code adapter
- Codex adapter
- executor/reviewer loop
- context projection
- structured findings
- loop guards
- optional Switchyard endpoint injection

## v0.2 - Collaboration daemon

- local HTTP API
- durable session registry
- SSE/WebSocket events
- CLI attaches to running collaboration
- cancellation and pause/resume

## v0.3 - Worktree isolation

- one worktree per writable harness
- patch exchange
- reconciliation phase
- conflict detection
- merge policy

## v0.4 - Collaboration policies

- planner/executor
- reverse review
- debate
- dual review
- evidence resolution
- quorum/consensus without majority-vote shortcuts

## v0.5 - MCP bridge

- HarnessMesh MCP server
- peer_message
- request_review
- submit_evidence
- resolve_disagreement
- collaboration_status

This would allow an interactive Claude Code or Codex session to invoke its peer
without leaving the native harness.

## v0.6 - Adaptive collaboration

- materiality scoring
- route only high-value reviews
- latency and cost ledger
- collaboration budget
- learned policy from accepted/rejected findings
- Switchyard signal integration
