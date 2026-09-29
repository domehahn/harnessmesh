# Evidence Freshness & Invalidation Graph

## Evidence Freshness Model

Evidence in MeshCommit is strictly bound to the exact state of the repository at the moment it was produced. Every `ChangeEvidence` record contains:
- `TreeHash`: The exact 40-character Git tree hash (or deterministic SHA-256 tree hash) verified.
- `ObligationID`: The specific proof obligation satisfied.
- `Valid`: Boolean flag indicating whether the evidence remains fresh.
- `InvalidationReason`: Explanation if evidence was marked stale.

---

## Invalidation Rules

When a file in the repository changes:
1. **Tree Hash Drift**: HarnessMesh computes the new `CurrentTreeHash` and compares it to the evidence `TreeHash`.
2. **Path Scoping**:
   - **Repository-Wide Proofs**: Obligations with an empty `Scope` (e.g. whole test suite, full system build, comprehensive race detector) are **always invalidated** whenever any file in the repository changes.
   - **Path-Scoped Proofs**: Obligations scoped to specific paths (e.g. `src/auth.go`) remain **fresh and valid** if the modified files do not intersect with `obl.Scope`.
3. **Evidence Status Transition**:
   - Stale evidence is marked `Valid = false`.
   - The associated obligation is set to `status = stale` or `pending`.
   - The overall change gate is automatically demoted from `committable` back to `blocked`.

---

## Anti-Spoofing & Self-Review Invariant

To ensure verifiable peer accountability:
- The `SourceParticipant` submitting evidence for review obligations (`independent_review`, `security_review`) cannot match the `AuthorParticipant` of the change.
- Any attempt by an author to submit review evidence for their own change results in immediate rejection with `SelfReviewForbiddenError`.

---

## TOCTOU Protection

A fundamental vulnerability in automated change management is the Time-Of-Check to Time-Of-Use race: an agent passes all checks, and an attacker (or concurrent process) modifies the code before `git commit` occurs.

MeshCommit prevents this through a mandatory pre-commit check:
```go
currentTreeHash, err := workspace.ComputeTreeHash(ctx, c.repoPath)
if currentTreeHash != chg.CurrentTreeHash || currentTreeHash != chg.VerifiedTreeHash {
    return nil, &protocol.CommitTreeMismatchError{
        ChangeID:     chg.ID,
        VerifiedTree: chg.VerifiedTreeHash,
        CommitTree:   currentTreeHash,
    }
}
```
If any file was touched after the gate was verified, the commit is rejected immediately.

