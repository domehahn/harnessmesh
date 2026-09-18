# Architecture

HarnessMesh separates **agent collaboration** from **model routing**.

## Layers

```text
┌──────────────────────────────────────────────────────────────┐
│ User / CI / developer                                       │
└──────────────────────────┬───────────────────────────────────┘
                           │ task
                           ▼
┌──────────────────────────────────────────────────────────────┐
│ HarnessMesh                                                 │
│                                                              │
│ Collaboration session                                       │
│ Context projection                                          │
│ Peer message protocol                                       │
│ Review loop / loop guards                                   │
│ Evidence-oriented findings                                  │
│ Run report                                                  │
└─────────────┬───────────────────────────────┬────────────────┘
              │                               │
              ▼                               ▼
┌──────────────────────────┐      ┌──────────────────────────┐
│ Claude Code harness      │      │ Codex harness            │
│ tools / shell / edits    │      │ tools / shell / edits    │
│ native session state     │      │ native thread state      │
└─────────────┬────────────┘      └────────────┬─────────────┘
              │                                │
              └──────────────┬─────────────────┘
                             │ optional
                             ▼
                    ┌─────────────────┐
                    │ NeMo Switchyard │
                    │ model routing   │
                    └─────────────────┘
```

## Why not fork Switchyard?

Switchyard's concern is model routing. It already offers model selection,
protocol translation, routing algorithms, session affinity, stage signals,
advisor gating, and sub-agent-aware routing.

HarnessMesh treats that as an optional lower layer and focuses on a different
abstraction: **a peer relationship between complete coding harnesses**.

This keeps model-selection innovation upstream while HarnessMesh develops
collaboration primitives.

## v0.1 invariant: single writer

The executor is the only harness expected to modify the working tree.

The reviewer receives a context projection and runs in read-only/no-tool mode
where supported.

This invariant eliminates a large class of races:

- simultaneous edits,
- overwritten patches,
- index lock contention,
- conflicting dependency updates,
- tests running against unstable files.

Future writable peers require per-agent worktrees.

## Collaboration state

HarnessMesh stores only orchestration metadata and reports.

Native harness conversation state stays native:

- Claude Code session ID
- Codex thread ID

HarnessMesh passes peer feedback into those sessions rather than attempting to
reimplement the harness conversation database.

## Context projection

The projection is deliberately repository-centric instead of transcript-centric.

A peer receives:

- task
- executor final summary
- current HEAD
- branch
- status
- diff stat
- bounded unstaged + staged diff
- optional test command / exit code / output
- untracked filenames

This gives the reviewer direct evidence of what exists rather than relying on an
executor's self-report.

## Stop conditions

The loop ends when any of these happens:

- reviewer returns `approve`
- reviewer returns `block`
- max rounds reached
- wall time expires
- the same finding set repeats in consecutive rounds
- an agent process fails
- review output violates the schema

## Switchyard boundary

HarnessMesh can set the provider base URL environment used by the harnesses, but
it does not implement Switchyard's routing algorithms.

The clean dependency direction is:

```text
HarnessMesh -> harness CLI -> Switchyard -> provider/model
```

not:

```text
HarnessMesh fork contains Switchyard source
```
