# Harness Economy & Participant Selection in HarnessMesh v0.2.0

HarnessMesh v0.2.0 introduces an economic model for multi-agent coding sessions. Rather than defaulting all requests to the most expensive frontier model, HarnessMesh applies a **cheap-first execution policy with bounded escalation and reversible de-escalation**.

---

## The Two-Level Orchestration Architecture

To avoid redundant, conflicting, or double model-routing:

1. **Level 1 (HarnessMesh)**: Selects the **Participant / Harness** (e.g. Codex vs. Claude vs. Antigravity) based on capabilities, task difficulty tier, privacy boundaries, and relative cost.
2. **Level 2 (Switchyard / Provider Router)**: Where supported, selects the **underlying Model / Provider** (e.g. Claude 3.5 Sonnet vs. DeepSeek Chat) based on token context, latency, and provider health.

> **Boundary Invariant**: HarnessMesh never second-guesses or overrides model choices for turns managed by Switchyard. For fixed or external harnesses (like GitHub Copilot CLI), model selection remains opaque to HarnessMesh.

---

## Task Difficulty Tiering

HarnessMesh classifies task difficulty using runtime signals into four distinct tiers:

| Tier | Characteristics | Preferred Participant Class |
|---|---|---|
| **Trivial** | Typo fixes, simple documentation edits, formatting, single-line changes | `local`, `efficient` |
| **Routine** | Standard bug fixes, linter remediation, isolated unit tests, localized refactors | `efficient` (cost <= 2.0) |
| **Complex** | Multi-file architectural refactors, cross-package API updates, failing integration tests | `capable` (cost >= 3.0) |
| **Critical** | Security vulnerability remediation, database migrations, high-risk production fixes | `capable`, `premium` |

### Difficulty Signals

- `ChangedFiles` / `DiffChars`: Scale of code modification under review.
- `HasFailingTests` / `HasBuildFailures`: Compilation or test runner status.
- `OpenHighFindings`: Number of unresolved high or blocker findings in the session.
- `HasSensitivePaths`: Whether private credentials or sensitive files are touched.

---

## Selection Pipeline (`cheapest_suitable`)

When `peer_selection_policy` is set to `"cheapest_suitable"`, candidates are filtered through the following strict sequence:

1. **Safety & Capability Filtering**: Candidate must possess the requested capability (e.g. `review`, `run_commands`).
2. **Data Privacy & Scope**:
   - If `HasSensitivePaths` or `scope: ["private"]` is set, cloud participants without local compliance are rejected in favor of local participants (`is_local: true`).
3. **Workspace Invariant**:
   - Exactly one writable participant per session.
4. **Task Tier Filtering**:
   - Routine/Trivial tasks filter for efficient participants (`class: "efficient"`, `relative_cost <= 2.0`).
   - Complex/Critical or escalated tasks filter for capable participants (`class: "capable"`, `relative_cost >= 3.0`).
5. **Cost Ranking**:
   - Remaining candidates are sorted by `relative_cost` ascending, breaking ties by lowest call count.
6. **Session Affinity**:
   - Preference for the participant that already has established conversation cache in this session.

---

## Bounded Escalation and Reversible De-escalation

1. **Escalation**:
   - If an efficient participant produces an invalid edit, causes a build failure, or verification fails, the session registers an escalation (`eng.EconomyController().Escalate`).
   - The subsequent turn automatically escalates to a `capable` participant.
   - Bounded escalation ensures that costs do not spiral unbounded: maximum rounds and turn depth are enforced.

2. **Reversible De-escalation**:
   - Once the complex problem is resolved and verification passes, the controller clears the escalation (`eng.EconomyController().Deescalate`).
   - Subsequent routine review rounds or queries immediately revert to the efficient participant.

---

## Auditing Routing Decisions

Every routing decision is recorded in SQLite with full explainability metadata:

```bash
# SQLite table: routing_decisions
# Columns: id, session_id, operation, capability, chosen_agent, tier, policy, reasons_json, created_at
```

Decision records include reasons such as `["capability_match", "tier_efficient_match", "lowest_relative_cost"]` for complete auditability.

