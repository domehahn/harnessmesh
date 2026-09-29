# MeshCommit — Evidence-Gated Agentic Change Transactions

## Mission & Core Principle

In autonomous and multi-agent software engineering, agents frequently propose, review, and modify code. However, conventional PR reviews and conversational checks suffer from subjective opinion, hallucinated reviews, and temporal drift.

**MeshCommit** establishes a rigorous principle:

> **An agent may propose a change.**  
> **An agent may review a change.**  
> **An agent may provide evidence.**  
>  
> **BUT:**  
>  
> **NO AGENT DECIDES BY OPINION THAT THE CHANGE IS TRUSTWORTHY.**

HarnessMesh deterministically computes whether a specific repository state satisfies a pre-defined, policy-locked set of proof obligations. The evaluation is 100% deterministic pure code with zero LLM model calls.

The system conclusively answers:
1. **What exactly was changed?** (Computed diff & affected file paths).
2. **What exact Git state was reviewed?** (Bound `TreeHash`).
3. **What evidence proves it?** (Test logs, static analysis exit codes, peer review attestations).
4. **Which proof obligations were satisfied?** (Exact obligations locked at change preparation).
5. **Is the commit identical to what was verified?** (TOCTOU protection verifying `TreeHash` immediately before commit).

---

## Architecture Overview

```
                      +-----------------------------+
                      |   Agent Proposes Change     |
                      |   (Only designated writer)  |
                      +--------------+--------------+
                                     |
                                     v
                      +-----------------------------+
                      | Isolated Git Tree Fingerprint|
                      | (GIT_INDEX_FILE plumbing)   |
                      +--------------+--------------+
                                     |
                                     v
                      +-----------------------------+
                      |  Locked Proof Policy Spec   |
                      |  (No policy downgrade)      |
                      +--------------+--------------+
                                     |
                      +--------------v--------------+
                      |  Evidence Generation Graph  |
                      |  - Test execution logs      |
                      |  - Independent peer review  |
                      |  - Static analysis          |
                      +--------------+--------------+
                                     |
                                     v
                      +-----------------------------+
                      |  Deterministic ChangeGate   |
                      |  (Pure code, ZERO LLM)      |
                      +--------------+--------------+
                                     |
                                     v
                      +-----------------------------+
                      | TOCTOU-Guarded Commit Engine|
                      | (TreeHash match validation) |
                      +--------------+--------------+
                                     |
                                     v
                      +-----------------------------+
                      | Knowledge Archive Record    |
                      | + EventBus Emission         |
                      +-----------------------------+
```

---

## Core Invariants

1. **Single-Writer Safety**: Only the designated writer participant in a space may create or prepare changes. Reviewer participants are strictly read-only.
2. **Deterministic Tree Fingerprint**: Every change is anchored to a Git synthetic tree hash computed via isolated index plumbing (`GIT_INDEX_FILE`), guaranteeing no user working files or `.git/index` are touched.
3. **Anti-Spoofing & Independent Review**: The author of a change cannot review or self-certify review obligations (`SelfReviewForbiddenError`).
4. **Policy Downgrade Protection**: The proof obligations are evaluated against policy rules and permanently locked at change preparation. Malicious or accidental changes to config cannot weaken requirements for an in-flight change.
5. **Conservative Invalidation**: Modifying the worktree updates the tree hash and invalidates evidence for overlapping scoped paths. Repository-wide proofs (e.g. full build or test suite) are always invalidated when any file changes.
6. **Time-Of-Check To Time-Of-Use (TOCTOU) Protection**: Immediately before creating the final Git commit, the tree hash is recomputed and compared against the verified tree hash. If any file changed, commit is rejected (`CommitTreeMismatchError`).

