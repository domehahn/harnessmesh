# Interactive Peer Conversation Protocol

HarnessMesh v0.2.0 introduces interactive peer conversations (`peer.converse`), enabling real-time dialogue and iterative collaboration between AI coding harnesses.

---

## Overview

Unlike one-shot question answering (`peer.ask`) or formal batch review loops (`peer.request_review`), `peer.converse` provides continuous multi-turn conversations with a peer agent while maintaining:
- **Session Mapping**: Links the HarnessMesh collaboration session to native harness threads (e.g. `codex exec resume <thread_id> -`).
- **Causality Tracking**: Enforces parent-child causation chains (`causation_id`, `correlation_id`).
- **Context Projection**: Dynamically injects git diffs, git status, test execution results, and recent conversation history.
- **Budget & Turn Limits**: Enforces strict bounds on turn count and depth to prevent runaways.

---

## Tool Specification: `peer.converse`

### Input Arguments

| Field | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `message` | string | **Yes** | The prompt, question, code snippet, or follow-up to the peer. |
| `peer` | string | No | Specific peer agent name (e.g. `openai-reviewer`). If omitted, routed by capability. |
| `capability` | string | No | Capability or role for dynamic peer selection (e.g. `architecture`, `review`, `security`). |
| `scope` | []string | No | Repository file paths or directories under discussion. |
| `context` | object | No | Context options: `include_diff`, `include_tests`, `include_git_status`, `files`. |
| `expected_outcome` | string | No | Target outcome: `second_opinion`, `review`, `architecture_check`, `debugging_help`, `security_analysis`, `validation`. |
| `causation_id` | string | No | Message ID of the parent message being replied to or followed up on. |
| `idempotency_key` | string | No | Idempotency key to prevent duplicate expensive executions. |

### Output Response (`ConverseResponse`)

```json
{
  "type": "approval",
  "peer": "openai-reviewer",
  "conversation_id": "hm_948a92f0",
  "message_id": "msg_003f2a1b",
  "response": "LGTM! The connection pool configuration and backoff retry logic look robust.",
  "status": "approved",
  "findings": [],
  "evidence": [],
  "requires_reply": false
}
```

### Response Classification (`type`)

- `approval`: The peer validates the proposed architecture or changes (`LGTM`, approved).
- `review`: The peer identified structured findings (`changes_required` or advisory notes).
- `question`: The peer asks for clarification before proceeding.
- `challenge`: The peer questions an assumption or design choice with counter-arguments.
- `answer`: General consultative answer.
- `requires_human`: The peer determined human input or business decision is needed.

---

## Thread Continuity & Resumption

HarnessMesh manages persistent thread continuity on behalf of the caller:

1. **Turn 1 (Initial Request)**:
   - HarnessMesh invokes `Harness.Invoke(ctx, req)`.
   - The harness returns output along with a native session identifier (e.g. `codex-thread-100` from `thread.started`).
   - HarnessMesh saves the session ID in SQLite (`session_participants.harness_session_id`).

2. **Turn 2+ (Follow-up Turn)**:
   - HarnessMesh checks `session_participants` for the active `harness_session_id`.
   - HarnessMesh invokes `Harness.Invoke(ctx, req)` with `req.SessionID = <thread_id>`.
   - The Codex adapter executes:
     ```bash
     codex exec resume <thread_id> -
     ```
   - The peer receives the follow-up prompt within the existing context thread, avoiding repetitive prompt engineering or state loss.

---

## Causality & Envelope Schema

Every conversational turn is recorded as a `PeerEnvelope` in the SQLite store:

```json
{
  "protocol": "harnessmesh/peer/v1",
  "id": "msg_77a28b12",
  "session_id": "hm_18fa30c9",
  "from": "antigravity-main",
  "to": "openai-reviewer",
  "type": "converse",
  "created_at": "2026-09-19T00:15:30.000Z",
  "correlation_id": "hm_18fa30c9",
  "causation_id": "msg_33d901f4",
  "depth": 1,
  "external_session_id": "codex-thread-100",
  "duration_ms": 1420,
  "status": "completed",
  "payload": { ... }
}
```

Inspect conversation causality using the CLI:
```bash
harnessmesh session messages <session-id>
```

