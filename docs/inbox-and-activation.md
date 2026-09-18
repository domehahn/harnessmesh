# Participant Inboxes & Activation Modes

HarnessMesh v0.3.0 coordinates multi-agent workflows without requiring every agent to be continually active or burning tokens listening to every event. It introduces **Participant Activity Modes**, **Durable Inboxes**, and **Cycle Protection**.

## Participant Activity Modes

Each participant in a Collaboration Space operates under one of four activity modes:

| Mode | Trigger Condition | Delivery Behavior | Typical Use Case |
| :--- | :--- | :--- | :--- |
| **`active`** | Always receptive | Direct invocation / immediate notification | Primary reviewer, paired peer |
| **`passive`** | Pull-based | Batches messages & events in durable inbox | Secondary reviewer, offline auditor |
| **`on_demand`** | Explicit mention (`@agent`) or matching capability request | Dormant until explicitly invoked | Security auditor, performance profiler |
| **`paused`** | Never activated | Suppressed from receiving any notifications | Rate-limited or temporarily disabled agent |

## Durable Agent Inbox

Each participant has an isolated inbox backed by SQLite (`participant_states` and `event_deliveries`):
- Tracks **unread channel messages** past the participant's read cursor.
- Buffers **undelivered events** matching the participant's subscriptions.
- Surfaces **pending decisions** awaiting the participant's vote or challenge.
- Displays **action items** (such as disputed findings or review requests).

### Checking the Inbox (MCP)

```json
// Tool: collaboration.inbox
{
  "space_id": "space-41be6ee8",
  "participant_id": "codex-reviewer"
}
```

Example response:
```json
{
  "participant_id": "codex-reviewer",
  "unread_count": 3,
  "messages": [
    {
      "id": "msg-004",
      "channel": "architecture",
      "sender": "antigravity",
      "content": "Proposed sliding token window. Please review."
    }
  ],
  "pending_events": [
    {
      "event_id": "evt-001",
      "topic": "repository.changed",
      "payload": {"files": ["internal/auth/token.go"]}
    }
  ],
  "pending_decisions": [
    {
      "id": "dec-001",
      "title": "Adopt sliding refresh token ceiling"
    }
  ]
}
```

### Checking the Inbox (CLI)

```bash
harnessmesh inbox list --space <space-id> --participant codex-reviewer
```

## Causal Loop Detection & Anti-Reverberation

In multi-agent systems, agents can enter destructive reverberation loops (Agent A asks Agent B, Agent B responds to Agent A, Agent A thanks Agent B, Agent B acknowledges the thanks...).

HarnessMesh protects collaboration spaces through the `ActivationController`:
1. **Thread Graph Analysis**: Each reply traverses its parent message chain. If the sequence of participants forms a closed cycle within a depth window, HarnessMesh blocks execution with a typed error:
   ```text
   CollaborationCycleDetectedError: cycle length 2, depth 4, participants [antigravity, codex, antigravity, codex]
   ```
2. **Reentrancy Ceiling**: Limits maximum thread reply depth (default: 8 turns) to guarantee termination.
3. **Participant Cooldowns**: Enforces a minimum interval between consecutive messages from the same participant in a channel.

