# ADR 0010: Decision-Scoped Evaluation Persistence (ADR 0002 Phase 9D)

## Status

Implemented and verified. Depends on [ADR 0001](0001-agent-control-plane-switchyard-model-routing.md)-[0009](0009-production-evaluation-wiring-and-policy-identity-closure.md).

## Purpose

Phase 9C introduced `supervisor.DecisionID`, Core-owned `CandidateSnapshot`
capture, automatic production evaluation wiring, Champion policy
provenance, canonical policy fingerprinting, coverage accounting, and
typed invalid-recommendation reasons - but left one central
persistence/identity gap open: `CandidateSnapshotStore` was still keyed
by `(Tenant, TaskID)`, so two legitimate, independent routing decisions
for the same task (e.g. a retry whose candidate universe genuinely
changed) could not both be persisted and audited. Phase 9D closes
exactly this gap.

```text
Task identity is not routing-decision identity.
Every routing decision has its own immutable decision-scoped
candidate context.
Multiple routing decisions for the same Task may legitimately
have different candidate snapshots.
Future Challenger execution requires canonical policy identity
and a complete decision-scoped evaluation context.
```

## Non-goals (explicit)

No exploration, no Challenger execution, no bandits, no RL, no
automatic promotion. `ValidateExperimentContext`/
`ValidateExperimentPolicyIdentity` are READ-ONLY admission checks for a
FUTURE Phase 10 execution gate - they allocate no traffic, select no
Challenger, and execute nothing.

## Repository analysis (performed before writing code)

- `supervisor.DecisionID`/`RoutingDecisionContext.DecisionID` (ADR
  0009C) already existed and were already threaded correctly through
  `Scheduler.selectAgent` -> `captureDecision` -> the
  `RoutingDecisionObserver` hook. The gap was entirely on the
  `internal/performance` side: `CandidateSnapshot` itself had no
  `DecisionID` field, and `CandidateSnapshotStore`/`ComputeEvaluationID`
  both keyed off `(Tenant, TaskID)` (store) or `TaskID`+`CapturedAt` (as
  a proxy for decision identity, inside the snapshot fingerprint) rather
  than the real `DecisionID`.
- Confirmed by direct inspection: NO real, non-Memory-backed persisted
  store exists for `CandidateSnapshot`/`RoutingEvaluationRecord`
  anywhere in the repository (both ADR 0008 and ADR 0009C shipped only
  `MemoryCandidateSnapshotStore`/`MemoryEvaluationStore`). This
  significantly simplified the "legacy compatibility"/"schema
  migration" concerns the mandate raised: there is no SQLite schema to
  migrate and no real historical persisted data to preserve or
  heuristically reconstruct - the "legacy compatibility" requirement
  instead applies to `RoutingPolicyDescriptor.FingerprintScheme ==
  "manual-v1"` records (ADR 0009C), which remain fully readable/usable
  for historical evaluation and are simply never ADMITTED into the new
  `ValidateExperimentPolicyIdentity` check.
- Traced every current `Tenant+TaskID` key/lookup:
  `CandidateSnapshotStore.Save/Get` (the one that needed to change),
  `ReviewFinalizationHook`'s exact `ExecutionID` resolution (ADR 0006
  Phase 7C, already correct and untouched - it was never TaskID-keyed
  at the persistence-identity level to begin with, only used `TaskID`
  as a correlation FIELD, same as it remains here).

### Gap matrix

| Concern | Current | Target | Gap | Action |
|---|---|---|---|---|
| Snapshot key | `(Tenant, TaskID)` (ADR 0008/9C) | `(Tenant, DecisionID)` | full | `CandidateSnapshot.DecisionID` field; `CandidateSnapshotStore.Save/Get` rekeyed |
| Decision lookup | None beyond the single-snapshot-per-task `Get` | `Get` by exact decision; `ListByTaskID` for the task-level audit view | full | new `ListByTaskID` method |
| Evaluation binding | `ComputeEvaluationID` used `TaskID`+`CapturedAt` as a decision-identity proxy | Explicit `DecisionID` binding, first-class field | full | `RoutingEvaluationRecord.DecisionID`; `ComputeEvaluationID` hashes `snapshot.DecisionID` explicitly |
| Execution binding | `ExecutionID`-keyed outcome attachment (ADR 0009C, unchanged) | unchanged - already correct per-execution | none | no change needed |
| Retry semantics | Reselection already generates a new `DecisionID` (ADR 0009C); execution-retry-without-reselection analyzed below | documented explicitly | partial | analysis only, see "Retry vs. reselection" |
| Legacy compatibility | No real persisted store exists; `FingerprintSchemeManualV1` already distinguished (ADR 0009C) | Manual-scheme policies remain usable, never promoted to experiment-eligible | none (already correct) | `ValidateExperimentPolicyIdentity` enforces this |
| Canonical policy eligibility | `FingerprintSchemeCanonicalV2` existed with no admission check consuming it | explicit admission validator | full | `ValidateExperimentPolicyIdentity`/`ValidateExperimentContext` |

## Identity relationships

```text
TaskID       = logical work identity (fachliche Gruppierung)
DecisionID   = one concrete routing-selection event
ExecutionID  = one concrete execution attempt
```

Confirmed cardinality by tracing `Scheduler.selectAgent`/`Executor.Execute`:

```text
TaskID      1 -> N  DecisionID   (every selectAgent call, including retries/reselections, generates a NEW DecisionID - ADR 0009C, unchanged)
DecisionID  1 -> 1  CandidateSnapshot  (exactly one snapshot per decision, immutable once saved)
DecisionID  1 -> N  RoutingEvaluationRecord  (one per configured Challenger)
DecisionID  1 -> 0..1  selected production ExecutionID  (zero if Executor's TOCTOU re-check later rejects the task before any execution occurs - the decision/evaluation context still persists; see "No execution yet" below)
```

A decision never produces MORE than one real Champion `ExecutionID`:
`Scheduler.Tick`'s dispatch loop calls `Executor.Execute` exactly once
per successful `Applier.Apply` for that specific dispatch - a genuine
retry of the SAME failed task requires a NEW `Applier.Apply`
(`RetryTaskProposal` + re-`AssignTaskProposal`) and a NEW
`Executor.Execute` call, which is architecturally indistinguishable
from - and, per ADR 0009D's analysis below, correctly treated as -
requiring a NEW decision, since Scheduler's `selectAgent` runs again as
part of the normal dispatch flow for the task's next ready cycle.

## CandidateSnapshot now includes DecisionID

```go
type CandidateSnapshot struct {
    DecisionID supervisor.DecisionID
    Tenant     string
    TaskID     supervisor.TaskID
    TaskClass  TaskClass
    CapturedAt time.Time
    AgentIDs   []string
}
```

`NewCandidateSnapshot` now REQUIRES a non-empty, Core-owned
`decisionID` as its first parameter (`ErrMissingDecisionID` otherwise) -
never caller-, Challenger-, or agent-supplied in the production path
(`ProductionEvaluationCoordinator.ObserveRoutingDecision` passes
`decision.DecisionID`, itself generated exclusively by
`Scheduler.selectAgent`'s `captureDecision`, unchanged from ADR 0009C).

## Snapshot store rekeyed to (Tenant, DecisionID)

```go
type CandidateSnapshotStore interface {
    Save(ctx context.Context, snap CandidateSnapshot) error
    Get(ctx context.Context, tenant string, decisionID supervisor.DecisionID) (CandidateSnapshot, bool, error)
    ListByTaskID(ctx context.Context, tenant string, taskID supervisor.TaskID) ([]CandidateSnapshot, error)
}
```

`MemoryCandidateSnapshotStore` maintains the primary map keyed by
`(Tenant, DecisionID)` plus a secondary index `(Tenant, TaskID) ->
[]decisionKey` purely to answer `ListByTaskID` without a linear scan -
`DecisionID` via the primary map remains the sole authoritative
identity; the secondary index is never consulted for conflict detection
or `Get`.

**Save semantics**: an exact replay of the SAME `DecisionID` with
IDENTICAL snapshot content (fingerprint AND `TaskID` match) is an
idempotent no-op. The SAME `DecisionID` with DIFFERENT content is a
`*DecisionSnapshotConflict` - fails closed, never silently overwritten.
Two DIFFERENT decisions for the SAME `TaskID` - whether their candidate
sets differ or happen to be identical - both persist independently and
NEVER conflict with each other merely because `TaskID` matches
(`TestDecisionScoped_TwoDecisionsSameTaskDifferentSnapshotsCoexist`,
`TestDecisionScoped_TwoDecisionsSameTaskSameSnapshotCoexist`).

`SnapshotIdentityConflict` (ADR 0008/9C, keyed-by-TaskID era) was
REMOVED, not kept as dead code - nothing constructs it anymore, and no
historical persisted data exists anywhere to still reference it.

## Snapshot fingerprint semantics (deliberately unchanged in kind)

`CandidateSnapshot.fingerprint()` continues to hash `(Tenant, TaskID,
TaskClass, CapturedAt, AgentIDs)` - deliberately EXCLUDING `DecisionID`,
per the mandate's own explicit recommendation: the fingerprint
describes the snapshot's CANDIDATE-SEMANTIC CONTENT, not its decision
identity. Two different decisions that happen to see an identical
eligible-candidate universe at the same captured moment correctly
produce the SAME fingerprint - this is a true fact about the candidate
universe, not an identity collision, and the records remain fully
separated by their actual persistence key, `DecisionID`
(`TestDecisionScoped_TwoDecisionsSameTaskSameSnapshotCoexist` proves
this exact case).

(`TaskClass` was added to the fingerprint's hashed fields in this phase
- a pre-existing omission noticed while reviewing the function, not
something ADR 0009D was specifically asked to fix, but corrected since
it is clearly part of "candidate-semantic content" and costs nothing to
include correctly now.)

## EvaluationRecord binding and EvaluationID

```go
type RoutingEvaluationRecord struct {
    EvaluationID string
    DecisionID   supervisor.DecisionID // first-class binding
    ...
}
```

`ComputeEvaluationID` now explicitly hashes `snapshot.DecisionID`
alongside `snapshot.fingerprint()` and both policy descriptors' string
forms - `TaskID` alone can never produce an Evaluation identity.
Because `fingerprint()` deliberately excludes `DecisionID`, two
decisions sharing an identical candidate-semantic fingerprint (the case
above) still produce DIFFERENT `EvaluationID`s, since `DecisionID`
itself is hashed as a separate, distinguishing component
(`TestNegative_EvaluationIDsDoNotCollideAcrossDecisionsWithSameChampionAndChallenger`).
`decisionFactsEqual` (the `MemoryEvaluationStore.Save` idempotency/
conflict gate) now also compares `DecisionID` first.

Multiple decisions for the same Task now correctly coexist with
independent, non-overwriting evaluation sets
(`TestDecisionScoped_EvaluationsRemainBoundToOwnSnapshot` proves two
decisions for `T1` with DIFFERENT candidate universes produce
challenger recommendations that correctly reflect EACH decision's OWN
snapshot, never the other's).

## Decision -> Execution binding (unchanged mechanism, now proven multi-decision-safe)

The ADR 0009C correlation mechanism
(`ProductionEvaluationCoordinator`'s transient `pendingDecisions`
handoff keyed by `(Tenant, TaskID)`, consumed exactly once per
execution, plus the STABLE `evaluationsByExecutionID` map keyed by the
real Core-owned `ExecutionID`) required NO structural change - it was
already correct for the multi-decision-per-task case, because:

1. The transient handoff is consumed and deleted the moment the
   matching execution's `ObserveExecution` fires, so it can never be
   read by a LATER, unrelated decision for the same task.
2. The STABLE, long-lived correlation key is the real `ExecutionID`
   (never `TaskID`), which is unique per execution ATTEMPT regardless of
   how many decisions preceded it.

`TestDecisionScoped_E1ReviewCannotChangeD2Evaluation` proves this with
two independent, concurrent decisions (D1/T1 -> E1, D2/T2 -> E2) and a
review resolving to E1 that correctly updates only D1's evaluation
record, never D2's.

## Retry vs. reselection (explicit semantics, as analyzed)

- **Reselection** (Scheduler's `selectAgent` runs again, e.g. after a
  task returns to `Pending` via `RetryTaskProposal` and is re-dispatched
  on a later Tick): ALWAYS produces a NEW `DecisionID` - this was
  already true since ADR 0009C (`captureDecision` is called fresh
  inside every `selectAgent` invocation) and required no change here.
- **Execution retry without reselection**: this repository's Scheduler
  has NO code path that re-executes an already-assigned task without
  going through `selectAgent`/`Applier.Apply` again first - every
  retry observed in this codebase's actual state machine
  (`RetryTaskProposal` -> `TaskStatusPending` -> `AssignTaskProposal` ->
  `Executor.Execute`) is architecturally a RESELECTION, even when the
  SAME agent ends up selected again. There is therefore no live
  "same DecisionID, new ExecutionID" case to support in THIS
  repository's current Scheduler semantics - documented here as a
  closed question rather than left ambiguous, per the mandate's
  explicit "analysiere aktuelle Scheduler-Semantik und dokumentiere"
  instruction.

## No execution yet (decision context survives a later TOCTOU rejection)

A `RoutingEvaluationRecord` is created at DECISION time
(`ProductionEvaluationCoordinator.ObserveRoutingDecision`), entirely
independent of whether `Executor.Execute`'s own TOCTOU re-validation
later succeeds. If Core rejects the actual dispatch (capability
revoked, tenant mismatch, authorization failure - all pre-existing,
unchanged ADR 0006 Phase 7C checks), the decision/snapshot/evaluation
context remains exactly as captured; `OutcomeAttached` simply stays
`false` until (if ever) a real execution occurs. No fake `ExecutionID`
is ever generated to paper over this - `ExecutionID` remains empty on
the record until `ObserveExecution` genuinely fires for that task.

## Coverage is decision-scoped, not task-scoped

```go
type DecisionCoverageInput struct {
    DecisionID          supervisor.DecisionID // optional, traceability only
    ExpectedChallengers int
    CapturedChallengers int
}
```

`DecisionCoverageInput` (ADR 0009C) was ALREADY one-entry-per-decision
in its arithmetic design - this phase's contribution is making that
explicit (the optional `DecisionID` field) and proving it directly:
`TestDecisionScoped_CoverageCountsDecisionsNotTasks` shows Task T1 with
two decisions (D1 fully captured, D2 missing) correctly reports
`EligibleDecisionCount = 2`, not `1` - a caller deriving
`DecisionCoverageInput`s must group `RoutingEvaluationRecord`s by
`DecisionID`, never by `TaskID`, which is now possible precisely
because `DecisionID` is a first-class, queryable field on every record.

## Canonical policy identity becomes the experiment-admission requirement

```go
func ValidateExperimentPolicyIdentity(descriptor RoutingPolicyDescriptor) error
```

Requires `FingerprintScheme == FingerprintSchemeCanonicalV2` -
i.e. the descriptor must have been built via `NewRoutingPolicyDescriptor`'s
deterministic factory (ADR 0009C), never a manual/caller-supplied
fingerprint. A `FingerprintSchemeManualV1` (or no-fingerprint-at-all)
descriptor is rejected with `*PolicyNotExperimentEligible` wrapping
`*NonCanonicalPolicyIdentity` - but remains fully usable for
HISTORICAL evaluation/shadow/replay under existing, unchanged
semantics; this validator governs admission for a FUTURE Challenger-
execution gate (Phase 10, not built here), nothing about today's
shadow/replay behavior changed.

```go
func ValidateExperimentContext(ctx, snapshotStore, evaluationStore, tenant, decisionID, challengerPolicy, challengerAgentID) error
```

is a READ-ONLY readiness check codifying the EXACT required future
invariant verbatim: `DecisionID` exists AND its decision-scoped
`CandidateSnapshot` exists AND evaluation context was captured for it
AND the challenger policy uses `CanonicalV2` AND (if an agent is named)
that agent is actually in the snapshot. It performs NO traffic
allocation, NO Challenger selection, and NO execution - calling it is
indistinguishable, side-effect-wise, from calling three `Get`/`Query`
methods and comparing results, because that is literally all it does.

## Security closure review

| Question | Finding |
|---|---|
| Can two routing decisions for the same Task overwrite each other? | No - `(Tenant, DecisionID)` is the persistence key; `TestDecisionScoped_TwoDecisionsSameTaskDifferentSnapshotsCoexist`/`...SameSnapshotCoexist` prove both the differing- and identical-content cases coexist. |
| Can a retry receive another Decision's snapshot? | No - `Get`/`Replay` require the EXACT `DecisionID`; there is no "nearest"/"latest" lookup anywhere in this package. |
| Can an Execution bind to the wrong Decision? | No - the stable correlation key is the real, unique `ExecutionID`; `TestDecisionScoped_E1ReviewCannotChangeD2Evaluation` proves two concurrent decisions' outcomes never cross. |
| Can an Evaluation record be reused across Decisions? | No - `EvaluationID` hashes `DecisionID` explicitly; `TestNegative_EvaluationIDsDoNotCollideAcrossDecisionsWithSameChampionAndChallenger`. |
| Can a ManualV1 policy pass future experiment admission? | No - `ValidateExperimentPolicyIdentity` requires `CanonicalV2`; `TestNegative_ManualV1RejectedForExperimentReadiness`. |
| Can missing Decision context be mistaken for experiment readiness? | No - `ValidateExperimentContext` explicitly checks snapshot AND evaluation existence before passing; `TestNegative_PolicyConflictRejectsExperimentContext`. |
| Can Task-level queries leak cross-tenant Decision history? | No - `ListByTaskID` takes `tenant` as a required parameter and is scoped to the secondary index built only from that tenant's own saves; `TestNegative_CrossTenantDecisionLookupFailsClosed` proves a cross-tenant `Get` by the SAME `DecisionID` string never resolves. |

No real defects were found during this phase's implementation.

## Tests / Verification

```text
gofmt -l .                  -> clean
go build ./...              -> clean
go vet ./...                -> clean
go test -race ./...         -> all packages ok, zero regressions
```

`internal/performance`: 186 tests passing under `-race` (+17 from Phase
9D). `internal/supervisor`/`internal/review` unchanged (139/81 - Phase
9D made no changes to either package; `supervisor.DecisionID` already
existed from ADR 0009C). Existing fuzz targets re-run clean
(`FuzzDecodePlanResponse`, `FuzzDecodeSynthesisResponse`,
`FuzzDetectDuplicateKeys` in `internal/supervisor`;
`FuzzDecodeArbitrationResponse`, `FuzzEvidenceValidate` in
`internal/review`) - no new fuzz target added, since Phase 9D
introduces no new untrusted-parsing/wire-decode boundary.

## Residual risks

- **No real persisted (e.g. SQLite) store exists for `CandidateSnapshot`/
  `RoutingEvaluationRecord`** - unchanged from ADR 0008/9C; this phase's
  key-schema redesign is what such a store should mirror when built,
  but building it remains out of scope.
- **`DecisionID` is trusted only within the automatic production wiring
  path** - a caller with direct, in-process access to
  `CandidateSnapshotStore`/`EvaluationStore` could still supply an
  arbitrary string as a `DecisionID` (the store has no way to verify a
  `DecisionID` was genuinely produced by `supervisor.NewDecisionID`
  inside a real `selectAgent` call) - same class of in-process trust
  boundary documented in ADR 0009C, unchanged.
- **`ValidateExperimentPolicyIdentity`/`ValidateExperimentContext` are
  admission checks only** - no Phase 10 execution gate consumes them
  yet; they exist so that gate, when built, has a ready, already-tested
  precondition to call.

## Deviations from ADR

- Added `TaskClass` to `CandidateSnapshot.fingerprint()`'s hashed
  fields - a pre-existing omission (candidate-semantic content should
  include task class, since two decisions for the same task could in
  principle occur under different task classes in a future scenario)
  noticed while reviewing the function for this phase's "snapshot
  fingerprint semantics" requirement; not a behavior the mandate
  specifically flagged, but a small, clearly-correct fix made while the
  function was already under review.
- Removed `SnapshotIdentityConflict` (ADR 0008/9C) entirely rather than
  keeping it as an unused, deprecated type - no historical persisted
  data references it (no real store ever existed), so keeping dead
  code around had no compatibility benefit.

## Final invariants (proven by the test suite)

```text
TaskID is not used as the unique identity of a routing decision.         -> CandidateSnapshotStore/EvaluationStore both keyed by DecisionID, not TaskID
Every production routing decision has its own DecisionID.                -> unchanged from ADR 0009C, re-confirmed: captureDecision generates one per selectAgent call
Every DecisionID has its own immutable CandidateSnapshot.                 -> NewCandidateSnapshot requires DecisionID; Save fails closed on content conflict under the same DecisionID
Two decisions for the same Task may coexist with different snapshots.    -> TestDecisionScoped_TwoDecisionsSameTaskDifferentSnapshotsCoexist
A later decision cannot overwrite an earlier decision's context.         -> DecisionSnapshotConflict; TestNegative_SameDecisionDifferentSnapshotConflicts
EvaluationRecords bind to DecisionID.                                     -> RoutingEvaluationRecord.DecisionID, first-class field
EvaluationIDs cannot collide merely because TaskID is shared.             -> TestNegative_EvaluationIDsDoNotCollideAcrossDecisionsWithSameChampionAndChallenger
Execution outcomes attach to the exact Decision that produced execution. -> TestDecisionScoped_E1ReviewCannotChangeD2Evaluation
Retries/reselections cannot cross-bind decision contexts.                 -> same test, plus the explicit retry-vs-reselection analysis above
Coverage counts routing decisions, not just Tasks.                       -> TestDecisionScoped_CoverageCountsDecisionsNotTasks
Cross-tenant Decision lookups fail closed.                                -> TestNegative_CrossTenantDecisionLookupFailsClosed
Historical ManualV1 policy identities remain readable but are not eligible for future Challenger execution. -> TestNegative_ManualV1RejectedForExperimentReadiness
CanonicalV2 policy identity is required for experiment readiness.        -> TestPolicyReadiness_CanonicalV2Accepted
Missing decision-scoped evaluation context fails experiment readiness.   -> TestNegative_PolicyConflictRejectsExperimentContext
Champion remains the only production routing authority.                  -> unchanged from ADR 0009; no code in this phase touches selection
Challengers still cannot reach Executor.                                 -> unchanged structural guarantee, re-confirmed by the existing reflection-based guard tests
HarnessMesh still selects agents.                                        -> unchanged from ADR 0001-0009
Switchyard still selects models.                                         -> unchanged from ADR 0001-0009
No exploration exists yet.                                               -> ValidateExperimentContext/ValidateExperimentPolicyIdentity perform readiness checks only, zero execution/allocation side effects
LLM output remains proposal, never authority.                            -> unchanged from ADR 0002-0009; Phase 9D adds no new LLM surface
```
