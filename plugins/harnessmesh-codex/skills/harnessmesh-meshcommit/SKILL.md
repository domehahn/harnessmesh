---
name: harnessmesh-meshcommit
description: Use HarnessMesh MeshCommit tools for evidence-gated repository changes and transparent commit readiness.
---

# HarnessMesh MeshCommit

Use this skill when the user asks to prepare, verify, inspect or commit a
change through HarnessMesh's evidence-gated workflow.

## Workflow

1. Use `change.create` to open a scoped change transaction.
2. Use `change.diff` and `change.status` to inspect affected paths and obligations.
3. Run the requested proof obligations, then submit results with `change.evidence`.
4. Use `change.verify` for explicit verification steps.
5. Check `change.commit_status` before reporting that a change is committable.
6. Use `change.abort` when the transaction should not proceed.

Never claim that a change is ready solely because a diff exists. Report missing
or failed obligations and preserve the distinction between prepared and
committed state.
