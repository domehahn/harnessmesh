# Architecture

HarnessMesh has two distinct orchestration layers.

```text
                         User / Primary Agent
                                 |
                                 v
                         +---------------+
                         |  HarnessMesh  |
                         |---------------|
                         | participants  |
                         | capabilities  |
                         | collaboration |
                         | policy        |
                         | evidence      |
                         | persistence   |
                         +-------+-------+
                                 |
               +-----------------+-----------------+
               |                 |                 |
               v                 v                 v
          Antigravity          Codex          Claude/Copilot
                                 |
                                 v
                      optional model routing
                                 |
                                 v
                            Switchyard
```

## HarnessMesh responsibilities

- participant/harness discovery and selection
- persistent collaboration spaces
- channels, threads, direct peer messages, and inboxes
- event subscriptions and autonomous participant activation
- findings, evidence, challenges, and decisions
- context projection and secret filtering
- single-writer enforcement
- budgets, cooldowns, depth limits, and cycle detection
- durable SQLite state and auditability

## Switchyard responsibilities

Where configured and technically supported, Switchyard handles model/provider selection behind a selected participant.

**HarnessMesh chooses the agent. Switchyard may choose the model.**

HarnessMesh must not duplicate Switchyard's model-routing policy.

## Persistence

Collaboration state is persisted in SQLite using WAL mode and versioned schema migrations. Vendor session/thread IDs are mappings inside a HarnessMesh session or collaboration space; they are not the primary HarnessMesh identity.

## Safety invariant

Exactly one participant may be writable for a shared worktree. Reviewers and background peers remain read-only unless a future isolated-worktree design explicitly changes that invariant.

For deeper implementation detail, see [docs/ARCHITECTURE.md](../ARCHITECTURE.md).
