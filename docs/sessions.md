# Collaboration Sessions & Persistence

HarnessMesh stores collaboration session state in a durable local SQLite store (`~/.harnessmesh/harnessmesh.db`).

## Session Model

```text
HarnessMesh Collaboration Session
    ├── Claude Code harness session
    ├── Codex thread
    ├── Messages (Peer envelopes)
    ├── Findings (Structured findings)
    ├── Evidence (First-class artifacts)
    ├── Challenges (Disagreements)
    ├── Resolutions (Evidence-based decisions)
    └── Budget & Loop state
```

## Storage Architecture

- **SQLite Backend**: Embedded local database with zero network daemon dependencies.
- **WAL Journaling**: Uses `PRAGMA journal_mode = WAL;` for concurrent read performance.
- **Crash Safety**: Atomic transactional writes protect against corruption if processes terminate unexpectedly.
- **Foreign Keys**: Enabled (`PRAGMA foreign_keys = ON;`) to preserve relational integrity.
- **Versioned Migrations**: Managed via `schema_migrations` tracking schema evolution.

## CLI Session Management

```bash
# List all sessions
harnessmesh session list

# Inspect session details
harnessmesh session show <session-id>

# View session findings
harnessmesh findings <session-id>

# Stop an active session
harnessmesh session stop <session-id>
```

