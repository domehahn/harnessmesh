# Security and Safety

HarnessMesh treats multi-agent collaboration as a security boundary, not merely a messaging feature.

## Single writer

Exactly one participant may modify a shared worktree. Additional agents are read-only peers by default.

## Context minimization

Peer context is projected from relevant repository state instead of replaying complete agent transcripts.

Limits apply to:

- total context
- diffs
- file contents
- test/build output
- message history

## Secret protection

Sensitive paths and secret-like content should not be transmitted to peers that are not permitted to receive them.

Examples include:

- `.env` files
- private keys and certificates
- credentials
- state files containing secrets
- denied project-specific paths

## Evidence-first resolution

Model agreement is not proof. Findings should be resolved with repository evidence such as:

- code locations
- tests
- compiler/build results
- race detector output
- static analysis
- reproducible commands

HarnessMesh does not use majority voting as a truth mechanism.

## Autonomous-loop controls

Use bounded depth, peer-call limits, conversation-turn limits, cooldowns, idempotency, duplicate suppression, and cycle detection.

## Human control

A human can pause or stop collaboration. These controls must override autonomous activity.

For the complete threat model and invariants, see [docs/security.md](../security.md).
