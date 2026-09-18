# HarnessMesh Peer Protocol (`harnessmesh.peer/v1`)

The HarnessMesh Peer Protocol defines the strongly-typed, transport-independent message exchange between coding harnesses.

## Protocol Envelope

All peer messages are encapsulated in standard envelopes:

```json
{
  "protocol": "harnessmesh.peer/v1",
  "id": "msg_01J8R9XYZ...",
  "session_id": "hm_01J8R9...",
  "from": "antigravity-executor",
  "to": "codex-reviewer",
  "type": "review_request",
  "depth": 1,
  "created_at": "2026-09-18T22:00:00Z",
  "correlation_id": "msg_01J8R8...",
  "idempotency_key": "idem_hash_abc",
  "payload": {}
}
```

## Message Types

| Message Type | Direction | Purpose |
| :--- | :--- | :--- |
| `peer_list` / `peer_list_result` | Participant ➔ Broker | Enumerates active session participants and roles |
| `peer_capabilities` / `peer_capabilities_result` | Participant ➔ Broker | Discovers participants by declared capabilities |
| `question` / `answer` | Participant ➔ Peer | Targeted inspection and clarification |
| `review_request` / `review_result` | Participant ➔ Peer(s) | Structured review of changes |
| `finding` | Participant ➔ Shared Session | Structured bug or defect report |
| `evidence` | Participant ➔ Shared Session | Verifiable reproducer or check |
| `challenge` | Participant ➔ Peer Finding | Disagree with a finding with counter-claims |
| `resolution` | Participant ➔ Finding | Concludes an open or disputed finding |
| `reply` | Participant ➔ Peer | Causal response to a parent message |
| `status` | Participant ➔ Broker | Session health, metrics, and budget inquiry |

## Finding Structure

```json
{
  "id": "SEC-001",
  "severity": "high",
  "category": "security",
  "claim": "Potential SQL injection in parameter building",
  "evidence": "db/query.go:42",
  "recommendation": "Use parameterized queries",
  "file": "db/query.go",
  "line": 42,
  "line_start": 40,
  "line_end": 45,
  "status": "open",
  "source_participant": "codex-reviewer",
  "source_adapter": "codex",
  "duplicate_of": "",
  "related_findings": [],
  "evidence_refs": ["ev_12345"]
}
```

## Disagreement Invariant & Evidence Resolution

HarnessMesh explicitly forbids resolving disputes through model majority voting:
1. When a finding is contested, the challenging agent calls `peer.challenge` attaching counter-evidence.
2. The finding status is transitioned to `disputed`.
3. To resolve a finding (`peer.resolve`), an agent must provide verifiable evidence (e.g. `go test`, race detector, compiler output).
4. As long as any `high` or `critical` finding remains `open` or `disputed`, the review verdict remains `changes_required` (or `block`).

## Error Taxonomy

The protocol defines 18 standardized typed errors with code and retryability metadata:

| Error Type | Code | Retryable | Description |
| :--- | :--- | :--- | :--- |
| `PeerUnavailableError` | `PEER_UNAVAILABLE` | Yes | Target peer harness not found or unresponsive |
| `PeerTimeoutError` | `PEER_TIMEOUT` | Yes | Execution exceeded configured timeout limit |
| `PeerDepthExceededError` | `PEER_DEPTH_EXCEEDED` | No | Message exceeded maximum reentrancy depth |
| `PeerCallLimitExceededError` | `PEER_CALL_LIMIT_EXCEEDED` | No | Session exceeded total peer invocation budget |
| `CapabilityUnsupportedError` | `CAPABILITY_UNSUPPORTED` | No | Requested capability not offered by any peer |
| `BudgetExhaustedError` | `BUDGET_EXHAUSTED` | No | Token or USD cost budget exhausted |
| `WriterConflictError` | `WRITER_CONFLICT` | No | More than one writable harness configured |
| `WorkspaceInvalidError` | `WORKSPACE_INVALID` | No | Working directory traversal or symlink escape |
| `StalledError` | `STALLED` | No | Repeated findings or no progress across review cycles |
| `HarnessAuthenticationRequiredError` | `HARNESS_AUTH_REQUIRED` | No | Harness CLI requires login or API key |
