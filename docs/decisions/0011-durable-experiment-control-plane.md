# ADR 0011: Durable, Core-Owned Experiment Control Plane

## Status

Accepted for Phase 10.

## Decision

HarnessMesh keeps experiment authority in Core. An experiment has a durable
identifier, versioned allocation semantics, an explicit lifecycle, a tenant,
task and optional risk scope, and a challenger policy/agent identity. SQLite
stores the definition, budget counters, decision reservations, admissions and
append-only audit events through the existing `store.Store` boundary.

Challenger execution is possible only after Core has supplied a decision ID,
the exact decision-scoped candidate snapshot, complete evaluation context,
canonical-v2 challenger identity, scope checks, live eligibility and an atomic
budget reservation. Any unavailable or failed gate selects the Champion and
does not call an Executor. Allocation is deterministic and versioned by
`hash-v1`; no learning, bandit, promotion, or policy mutation is introduced.

Reservations are keyed by `(ExperimentID, DecisionID)` and are idempotent.
Consumption and release are explicit state transitions. Lifecycle and audit
records are durable, so restart recovery does not infer authority from
process memory. Tenant queries and scope mismatches fail closed.

## Consequences

The pre-existing shadow evaluation records remain valid and are not rewritten.
The scheduler receives an optional Core admission hook; when absent, behavior
is unchanged and Champion-only. A future execution observer must consume the
admission reservation and attribute the Core-owned ExecutionID to the actual
selected policy. No direct Challenger-to-Executor coupling is permitted.

## Verification boundary

This ADR is implemented with unit and race-safe in-memory tests plus the
existing SQLite store migration. Live multi-process database and production
provider tests remain environment-dependent and must be marked UNVERIFIED
until run against the deployment backend.
