# Event Bus & Event Coalescing

In HarnessMesh v0.3.0, asynchronous events (such as file modifications, test suite runs, and decision updates) are managed by an in-memory Pub/Sub **Event Bus** backed by durable SQLite delivery logs.

To prevent high-frequency event storms (e.g. 50 files saved by an executor in rapid succession), the Event Bus incorporates a **Sliding-Window Coalescing Engine**.

## Architecture

```text
  Local File Watcher / Harness Actions
                  │
                  ▼
  Raw Event Burst (e.g. 20 file writes within 200ms)
                  │
                  ▼
┌────────────────────────────────────────────────────────┐
│             Event Coalescing Engine                    │
│   Window: 500ms sliding buffer                         │
│   Group key: (space_id, "repository.changed")          │
│   Merges touched paths, dedupes, sorts                 │
└─────────────────────────┬──────────────────────────────┘
                          │
                          ▼ (Emits 1 unified event)
┌────────────────────────────────────────────────────────┐
│                   Event Bus                            │
│   Topic: repository.changed                            │
│   Payload: {"files": [...], "count": 20}               │
└─────────────────────────┬──────────────────────────────┘
                          │
            ┌─────────────┴─────────────┐
            ▼                           ▼
    Subscriber: codex           Subscriber: claude
    Mode: active                Mode: passive
    (Immediate delivery)        (Queued in inbox)
```

## Supported Event Topics

- `repository.changed`: File modifications, additions, or deletions within the workspace.
- `decision.proposed`: A participant has proposed a new architecture or design decision.
- `decision.accepted`: A proposed decision has met approval criteria and is finalized.
- `finding.created`: A new defect finding has been reported.
- `finding.resolved`: A disputed or open finding has been verified as fixed.
- `task.status`: Task execution milestone or state change.

## Sliding-Window Coalescing

When a batch of events arrives with a coalescing key:
1. The coalescing engine buffers events matching the key within a configurable sliding window (default: 500ms).
2. If another matching event arrives within the window, the timer resets and the payload data is merged.
3. Once the sliding window expires with no further events, a single coalesced event is dispatched onto the Event Bus with complete aggregate metadata.

## Subscription Rules & Filtering

Participants can subscribe to topics using wildcard patterns:
- `repository.*`: Matches all repository events.
- `decision.*`: Matches all decision lifecycle events.
- `*`: Matches all events within the collaboration space.

### MCP Subscription Management

```json
// Tool: collaboration.subscribe
{
  "space_id": "space-41be6ee8",
  "participant_id": "codex-reviewer",
  "topic": "repository.*"
}
```

```json
// Tool: collaboration.unsubscribe
{
  "space_id": "space-41be6ee8",
  "participant_id": "codex-reviewer",
  "topic": "repository.*"
}
```

## CLI Subscriptions

```bash
# List all active subscriptions in space
harnessmesh subscriptions list --space <space-id>

# Remove a specific subscription
harnessmesh subscriptions remove --space <space-id> --id <sub-id>
```

