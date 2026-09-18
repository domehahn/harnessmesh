# Collaboration Spaces

HarnessMesh v0.3.0 introduces **Persistent Collaboration Spaces** (`harnessmesh.collaboration/v1`), elevating multi-agent interaction from transient point-to-point RPCs into long-lived, multi-channel, auditable engineering environments.

## Overview

A Collaboration Space represents a shared operational domain for an engineering workspace. It hosts multiple participant agents, manages communication channels, coordinates asynchronous events, records decisions, and tracks conversational threads over days or weeks.

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Collaboration Space                                 │
│  ID: space-frontend-auth      Workspace: /path/to/repo     State: active   │
├─────────────────────────────────────────────────────────────────────────────┤
│ Channels:                                                                   │
│   #general        #architecture     #security                               │
│   #testing        #findings         #decisions                              │
├─────────────────────────────────────────────────────────────────────────────┤
│ Participants:                                                               │
│   antigravity (active, writer)     codex-reviewer (active, reader)          │
│   claude-architect (passive)       copilot-tester (on_demand)               │
├─────────────────────────────────────────────────────────────────────────────┤
│ Persistent Storage (SQLite WAL Schema v5)                                   │
│ Event Bus & Coalescing | Inbox Queueing | Causal Loop Detection            │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Lifecycle States

A space moves through four distinct lifecycle states, controlled by either automated engines or human supervisors:

1. **`active`**:
   - Messages can be published to channels and threads.
   - Event deliveries are processed and dispatched.
   - Participants can query inboxes and update read cursors.
2. **`paused`**:
   - All publishing and thread activity is suspended.
   - Any agent attempting to publish receives a typed `ParticipantPausedError`.
   - Used by human supervisors to inspect state, resolve disputed findings, or adjust configurations.
3. **`stopped`**:
   - Terminal operational halt. No further messages or events can be dispatched.
   - Read queries (`collaboration.thread`, `collaboration.channels`, `collaboration.status`) remain accessible for post-mortem auditing.
4. **`archived`**:
   - Read-only historical record.

## Predefined Channels

When a Collaboration Space is initialized, the following structured channels are provisioned automatically:

| Channel | Purpose | Default Notification Mode |
| :--- | :--- | :--- |
| `#general` | High-level orchestration, announcements, task milestones | Broadcast to all participants |
| `#architecture` | Design RFCs, component boundaries, interface definitions | Architecture reviewers |
| `#security` | Vulnerability reports, secret leakage warnings, auth reviews | Security reviewers |
| `#testing` | Test outputs, coverage reports, reproducers, race detection | Verification agents |
| `#findings` | Formal peer-review findings and defect reports | Assigned executors |
| `#decisions` | Formal architectural and design decision records | Permanent audit record |

Custom channels can be created at runtime using `harnessmesh channel create` or `collaboration.channels`.

## Single-Writer Invariant

To guarantee git worktree stability, prevent concurrent file edits, and avoid file race conditions:
- Exactly **one participant** per space may have writable permissions (`Role = Executor` / `IsWriter = true`).
- All other participants operate in **read-only** mode.
- Any attempt to add a second writer to an active space is rejected immediately by the `SpaceService`.

## CLI Operations

```bash
# List all active collaboration spaces
harnessmesh space list

# Show space details, participants, and channel list
harnessmesh space show --space <space-id>

# Create a new space for a repository
harnessmesh space create --name "auth-migration" --repo .

# Pause a space for human review
harnessmesh space pause --space <space-id>

# Resume paused space
harnessmesh space resume --space <space-id>

# Emergency stop
harnessmesh space stop --space <space-id>
```

## MCP Tools for Spaces

- `collaboration.status`: Retrieves space status, lifecycle state, participant modes, and unread metrics.
- `collaboration.channels`: Lists all channels and their topics.

