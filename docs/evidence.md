# Evidence-Based Collaboration

HarnessMesh replaces subjective conversational back-and-forth between agents with **evidence-oriented peer collaboration**. Findings are grounded in concrete code locations, test executions, or benchmark results, and can be challenged, defended, and resolved via durable evidence records.

---

## Evidence Model

All evidence items submitted to a session implement the `EvidencePayload` structure:

| Field | Type | Description |
|---|---|---|
| `id` | string | Unique identifier (`ev_<timestamp>`) |
| `finding_id` | string | ID of the finding this evidence supports or refutes |
| `source_agent` | string | Identifier of the agent submitting the evidence |
| `type` | `EvidenceType` | Type of evidence (see below) |
| `command` | string | Executed command line (if command or test output) |
| `result` | string | Command stdout/stderr or analyzer summary |
| `excerpt` | string | Relevant code snippet, diff, or error trace |
| `exit_code` | integer | Process exit status |
| `created_at` | timestamp | ISO 8601 UTC timestamp |

### Supported Evidence Types

- `code_location`: Precise `file:line` pointer to source code.
- `git_diff`: Uncommitted changes or patch hunk demonstrating behavior.
- `test_result`: Test runner execution output demonstrating pass or fail.
- `command_result`: Shell command invocation result (e.g., linter, compiler).
- `static_analysis`: Output from a static security or type-check tool.
- `build_result`: Compiler or build system diagnostic.
- `log_excerpt`: Runtime log snippet captured during reproduction.
- `user_requirement`: Citation from task specification or issue description.
- `benchmark_result`: Performance or memory profiling metric.
- `reasoning_note`: Analytical proof or logical derivation.

---

## The Challenge and Resolution Workflow

When a reviewer raises a finding, the collaboration fabric allows participants to engage in an evidence-grounded debate:

```
 Reviewer                      Engine / Store                      Executor
    |                                |                                |
    |-- peer.submit_finding -------->|                                |
    |   [FIND-001: SQL injection]    |-- finding.created ------------>|
    |                                |                                |
    |                                |<-- peer.challenge -------------|
    |                                |    [Target: FIND-001]          |
    |<-- challenge.created ----------|    "Parameterized by ORM"      |
    |                                |                                |
    |                                |<-- peer.submit_evidence -------|
    |                                |    [type: code_location/diff]  |
    |<-- evidence.created -----------|    "query.go:42 parameterized" |
    |                                |                                |
    |-- peer.resolve --------------->|                                |
    |   [status: confirmed|rejected] |                                |
    |   "Verified ORM behavior"      |-- resolution.created --------->|
```

### Resolution Statuses

- `confirmed`: The finding was verified as a genuine issue; fix required.
- `rejected`: The finding was refuted by evidence (e.g. false positive); closed without code modification.
- `partially_confirmed`: Issue acknowledged, but scope or severity adjusted.
- `superseded`: The finding was rendered obsolete by an alternative fix or refactoring.
- `requires_human`: Conflicting claims cannot be automated; escalated to human operator.

---

## Inspecting Evidence via CLI

```bash
# List all findings recorded for a session
harnessmesh findings <session-id> [--json]

# List all evidence items attached to findings in a session
harnessmesh evidence <session-id> [--json]
```

