# ADR 0005: Agent Performance and Decision Intelligence Foundation (ADR 0002 Phase 6)

## Status
Implemented and verified. Depends on [ADR 0001](0001-agent-control-plane-switchyard-model-routing.md)-[0004](0004-authoritative-evidence-and-review-state-closure.md).
This phase introduces `internal/performance`: a **read-only observation
plane** over agent/model/outcome data. It makes no routing decision and
changes no routing behavior.

## Goals

Systematically record, for each task execution: which task (and task
class), which agent executed it, which model(s) actually served it,
what Core/Scheduler observed about the execution, what independent
review/evidence established, and bounded performance metrics - all
derived from existing authoritative state, never from an agent's or
LLM's own claim.

## Non-goals (explicit)

This phase does **not** implement: automatic routing-weight learning,
automatic agent ranking for production routing, automatic routing-policy
changes, reinforcement learning, or bandit exploration in production.
The existing deterministic, capability-based `Scheduler`
(`internal/supervisor`) is unchanged. No `BestAgent(task)` function
exists anywhere in this package, deliberately. A speculative
`AgentDecisionAdvisor` interface was considered and **deliberately left
out entirely** - the request itself said to omit it if it would be
purely speculative, and nothing in this phase needs it yet.

## Repository analysis (performed before writing code)

- `supervisor.TaskID`/`AgentID`, `supervisor.ExecutionResult`,
  `supervisor.ModelRoutingDecision` (Phase 2B/3) already exist and are
  reused directly - no new ID types invented where these suffice.
- `economy.RoutingDecision` (`internal/economy/economy.go`) already
  records agent-selection provenance (candidates, selected, reasons,
  timestamp), persisted via `store.RoutingDecisionRecord`. This phase
  **references** it by ID (`PerformanceObservation.SelectionRecordRef`)
  rather than duplicating agent-selection provenance - exactly the
  "if already present, only reference" instruction.
- `review.ReviewGroup`, `IndependentConfirmingObservations`/
  `IndependentRefutingObservations` (Phase 4/5) already implement
  independence-aware review counting correctly - reused directly in
  `FinalizeWithReview` rather than re-implemented.
- `protocol.FindingStatus`'s six Phase-4 verdict values are reused
  verbatim as `QualityOutcome`'s base vocabulary (`QualityFromFindingStatus`
  is the identity function), plus one new sentinel, `QualityUnreviewed`,
  for "no review has happened yet" - a state `FindingStatus` itself has
  no value for.
- `internal/telemetry.Registry` (dependency-free counters/latencies,
  already used by `internal/supervisor`) is the existing metrics
  abstraction this phase's own observability hooks into - no second
  telemetry system.
- `store.Store`'s existing, versioned SQLite migration system (12
  migrations already) is extended with **migration 13**
  (`performance_observations` table) rather than a new custom database
  layer - the explicit instruction to prefer the existing store
  abstraction.
- No existing cost-tracking abstraction beyond `economy.RelativeCost`
  (agent-selection-time cost class, a different concern) and
  `review.DefaultActionCost` (review-action cost units, Phase 5) exists.
  This phase's `PerformanceMetrics.CostUnits` is populated by the caller
  from whatever real cost signal it has (if any); this package invents
  no new cost computation.

### Gap Matrix

| Concern | Existing | Target | Gap | Action |
|---|---|---|---|---|
| Task taxonomy | none (free text only, e.g. `Task.Type` in supervisor) | stable, typed, extensible taxonomy | full | new `TaskClass` (internal/performance), extensible via `RegisterTaskClass` |
| Agent outcome attribution | `supervisor.ExecutionResult` (per-task) | per-observation, Core-derived attribution | partial | `NewObservation` requires `AgentID` from caller-supplied authoritative context, never self-reported |
| Model attribution | `supervisor.ModelRoutingDecision` (per-call) | per-observation, multi-usage attribution | partial | new `ModelUsageRef[]`, sourced only from routing/transport context |
| Verified outcomes | `review.ReviewGroup.Status` (per-claim) | per-observation quality dimension | partial | `FinalizeWithReview`, reuses `review` package's independence logic unchanged |
| Cost | `economy.RelativeCost`, `review.DefaultActionCost` | per-observation, honest, non-fake-precision | partial | `PerformanceMetrics.CostUnits`, caller-supplied, no new computation invented |
| Latency | none, per-execution | per-observation duration + aggregated mean/median | full | new, from caller-supplied `StartedAt`/`FinishedAt` |
| Rework | none | per-observation `ReworkCount` | full | new field, populated by caller |
| Review quality | `review.ReviewGroup` (raw) | derived rates (verification/rejection/contestation) | partial | `AgentPerformanceSummary`'s counts - deliberately NOT a single rate/score (see "Statistical Safety") |
| Aggregation | none | deterministic, sample-size-aware | full | `Aggregate`/`AggregateModelUsage`, `InsufficientData` flag, no ranking |
| Persistence | 12 existing migrations, no performance table | integrate with `store.Store` | full | migration 13, `StoreBackedPerformanceStore` |

## Core principle: observed outcome != claimed success

`PerformanceObservation.ExecutionOutcome` is set only from
`NewObservationInput.ExecutionOutcome`, a pre-typed enum the **caller**
derives from `supervisor.ExecutionResult`/Scheduler event classification
- there is no text field anywhere in `NewObservationInput` an agent's own
claim could populate (`TestSecurity_AgentCannotSelfReportSuccess` proves
this structurally via reflection, then proves an invented outcome string
is rejected, not defaulted to success). `QualityOutcome` is set only via
`FinalizeWithReview`, which takes a `review.ReviewGroup` - the same
authoritative, evidence-checked record ADR 0003/0004 already made
impossible for an Arbiter to forge.

## Agent attribution vs. model attribution vs. review attribution

Three, deliberately separate attributions on one record:
`AgentID` (who executed), `ReviewerAgentIDs` (who independently reviewed
- excludes the implementer, `TestFinalizeWithReview_VerifiedFinalizes`),
`ModelUsages` (which route/provider/model actually served inference
calls, as a slice, never a false single-model simplification for an
execution that used several). `AggregateModelUsage` is a separate
function from `Aggregate` (agent performance) specifically so "Agent
Performance != Model Performance" is structural, not just documented -
a caller wanting a joint Agent+Model view runs both over the same
filtered observation set rather than this package building a merged
router component (explicitly out of scope, and no such component
exists).

Model identity (`ModelUsageRef.Model`) is populated only from
`InferenceResult.ServedModel`-shaped data the caller already has from
`supervisor`/`modelrouting` - there is no decoder anywhere in this
package that could parse a model identity out of LLM text
(`TestSecurity_ModelCannotSelfReportIdentity`).

## Outcome model: two independent dimensions

`ExecutionOutcome` (`completed`/`failed`/`timed_out`/`policy_rejected`/
`authorization_rejected`/`blocked`/`cancelled`/`partially_completed`)
and `QualityOutcome` (`unreviewed`/`unconfirmed`/`supported`/`contested`/
`needs_evidence`/`verified`/`rejected`) are never conflated - a record
can and often does have `ExecutionOutcome=completed` with
`QualityOutcome=unreviewed` or `contested`.

## Finalization lifecycle

`pending` (execution observed, quality not yet settled) ->
`finalized` (quality reached a Core-authoritative TERMINAL verdict:
`verified` or `rejected` only). Critically, `supported` does **not**
finalize (`TestFinalizeWithReview_SupportedStaysPending`) - agreement is
not authority, so an observation whose claim is merely "supported" stays
pending, exactly like the underlying `review.ReviewGroup` itself never
auto-promotes "supported" to "verified."

## Statistical safety / no ranking / cold start

`AgentPerformanceSummary` has **no composite score field at all** - only
independently-inspectable counts (`VerifiedCount`, `RejectedCount`,
`ContestedCount`, `PendingCount`, `FinalizedCount`, mean/median duration,
etc.), per the request's explicit preference ("Besser Phase 6 ohne
Composite Score"). `DefaultMinimumObservationCount` (10) marks
`InsufficientData=true` below threshold, but the honest counts are
**still returned** - cold start is never translated into an artificially
bad score (`TestAggregate_InsufficientDataFlaggedForColdStartAgent`
proves 2 real verified observations are still reported as 2, with the
flag merely noting the sample is small). `Aggregate` never errors or
refuses on zero samples - it returns a zero-valued, flagged summary
(`TestNegative_ZeroSampleAggregationReportsZeroNotError`).

## Bias limitations (documented, not solved)

This ADR explicitly documents, per the request, limitations this phase
does **not** attempt to correct:

- **Selection bias**: agents only ever receive tasks they were selected
  for (by the existing, unchanged `Scheduler`). Historical performance
  is therefore never an unbiased A/B comparison between agents - it
  reflects what each agent was *given*, not a controlled trial.
- **Difficulty bias**: if Agent A only receives easy tasks and Agent B
  only hard ones, raw counts favor A. `TaskClass` + `Risk` are recorded
  specifically so a *future* consumer could condition on them, but this
  phase performs no automatic normalization - there is no code anywhere
  that adjusts a count for difficulty.
- **Survivorship bias**: `Aggregate` explicitly counts
  failed/timed-out/cancelled/blocked executions alongside completed ones
  (`TestNegative_FailedAndCancelledExecutionsNotExcludedFromMetrics`) -
  a summary built only from successes would be meaningless.
- **No recall metric**: HarnessMesh has no ground truth for "how many
  real issues were missed." This phase deliberately implements **no**
  recall/miss-rate metric of any kind - not even a proxy - per the
  request's explicit conservatism on this point. A future phase wanting
  a *proxy* signal (e.g. "finding later discovered by an independent
  review") would need to label it as a proxy explicitly, not as recall.

**Historical performance is observational evidence, not causal proof
that an agent/model is superior.** This sentence is repeated here
verbatim because the request required it to be documented explicitly,
not merely implied.

## Idempotency / replay / correction semantics

`ObservationID = f(Tenant, TaskID, ExecutionID, Kind)`, deterministic,
never random. `MemoryPerformanceStore.Save`/`StoreBackedPerformanceStore.Save`
both: (a) treat an identical replay as a safe no-op, (b) treat a
same-ID-different-immutable-identity write as `DuplicateObservation`
(fail closed - two unrelated executions must never silently merge), (c)
refuse to let a stale `Pending` write revert an already-`Finalized`
record (`TestSecurity_HistoricRecordNotSilentlyRewrittenToPending`).
**Chosen pattern**: mutable projection (one record per observation,
updated in place as it moves pending -> finalized) rather than
append-only event history. Rationale, since the request asked this to
be justified rather than assumed: `ReviewGroup`/`FindingPayload` (the
authoritative sources this package reflects) are themselves mutable
projections, not event logs (ADR 0003/0004); mirroring that pattern here
keeps one consistency model instead of two, and the *authoritative*
audit trail of how a verdict was reached already lives in
`review.ReviewGroup`'s own observation history - this package's record
is a performance-analytics projection of that, not a second copy of the
audit trail.

## Tenant isolation

`ObservationFilter.Tenant` is **required** unless `AllTenants` is
explicitly set - `CrossTenantQueryDenied`, fail closed, mirroring ADR
0002 Phase 2B's and ADR 0004's tenant discipline exactly
(`TestNegative_CrossTenantQueryDenied`,
`TestSecurity_CrossTenantStatisticsLeakagePrevented`, and the real-DB
equivalent `TestStoreBackedPerformanceStore_TenantIsolation`).

## Decision provenance (observed, not altered)

`SelectionRecordRef` references an existing `economy.RoutingDecision`/
`store.RoutingDecisionRecord` by ID - this phase does not duplicate
agent-selection provenance, and critically, it never writes to or
influences that record. `ModelUsages` is the equivalent reference for
Switchyard's model-routing decision. Both are purely observational.

## Privacy

`PerformanceObservation` carries IDs, enums, counts, and durations -
never prompts, source code, secrets, raw evidence content, or raw
transcripts. Confirmed by inspection: no field anywhere in `types.go`
accepts free-form text beyond bounded identifiers.

## Security findings discovered during implementation

One test-authoring correction, not a product defect: two new security
tests initially asserted that a remediator's/copier's own observation
should contribute *zero* toward the independent-confirmation count.
Re-deriving from `review.IndependentConfirmingObservations`'s actual,
already-proven (ADR 0003/0004) semantics showed this was the wrong
expectation - a lone or copied observation legitimately contributes
*one* toward the count (there's nothing else in the set for it to be
"non-independent" from), and the real security property is that **one
alone, or one-plus-a-copy, is insufficient to reach the verification
threshold of two** - never that it counts as nothing. The tests were
corrected to assert the actual, correct property
(`TestSecurity_RemediatorSelfVerificationNotCountedAsIndependent`,
`TestSecurity_CopiedReviewNotCountedAsIndependent`); no change was made
to `review` or `performance` production code, since the original
Phase 4/5 logic was already correct.

## Observability

`internal/telemetry.Registry`-based counters for this subsystem are the
natural next integration point (`observations_created`,
`observations_finalized`, `duplicate_observations`, `attribution_failures`,
`aggregation_queries`) - following the exact `RegistryTelemetry` pattern
`internal/supervisor` already established. Not wired into a live call
site this phase (there is no production caller of `NewObservation` yet -
see Residual risks), so no counters would currently increment; the
integration point is documented rather than built against nothing.

## Tests / Verification

```text
gofmt -l .                  → clean
go build ./...              → clean
go vet ./...                → clean
go test -race ./...         → all 21 packages ok, zero regressions
```

`internal/performance`: 36 tests passing under `-race`, 76.8% coverage,
including a full end-to-end scenario
(`TestIntegration_FullDecisionIntelligenceFlow`: Scheduler-selects-A ->
Switchyard-chooses-X -> execution completes -> Finding+Evidence ->
independent Agent B reviews -> verified -> finalized observation ->
query returns exactly one finalized record) and 5 tests proving the real
`StoreBackedPerformanceStore` against an actual on-disk SQLite database
(migration 13). No new fuzz target was added - confirmed, by
inspection and by this phase's own security tests, that there is no
untrusted wire/decode boundary anywhere in `internal/performance` (every
field is caller-supplied, typed Go data from authoritative state); the
three existing fuzz targets (`internal/supervisor`,
`internal/review` x2) were re-run clean.

## Residual risks

- **No production caller wires `NewObservation`/`FinalizeWithReview`
  into the real `Scheduler`/`Executor`/`ReviewApplier` execution path
  yet.** This phase builds and proves the data model, store, and
  aggregation logic end-to-end against realistic *simulated* call
  sequences; actually calling `NewObservation` from
  `supervisor.Executor.Execute` and `FinalizeWithReview` from
  `review.ReviewApplier.Apply` (so real production executions start
  generating real observations) is deliberately left to a follow-up -
  wiring it now, without dedicated regression coverage of THAT
  integration seam specifically, risked rushing exactly the kind of
  under-tested production wiring this project's prior phases have
  consistently avoided.
- **`QueryPerformanceObservations` loads full rows and filters
  `FinalizedOnly` in Go**, not in SQL - correct but not indexed on that
  column. Acceptable at current scale; a dedicated index/column would be
  a one-line migration addition if volume grows.
- **`DefaultMinimumObservationCount` (10) is a single, hardcoded
  constant**, not yet configurable per task class or per deployment -
  reasonable for a foundation phase; a real production rollout may want
  this tunable.
- **No digest/content-integrity concept for `PerformanceObservation`
  itself** (unlike `review.EvidenceRecord`) - judged unnecessary, since
  this package's records are Core-constructed-only (no untrusted input
  path exists to tamper with), unlike evidence, which genuinely needed
  integrity checking against a replay/tamper threat model.

## Final invariants (proven by the test suite)

```text
Agents cannot assign themselves successful outcomes.        -> no agent-claim field exists in NewObservationInput
Models cannot self-report authoritative model identity.     -> no model-identity decoder exists anywhere in this package
Only Core/Evidence/Review state determines authoritative outcomes. -> ExecutionOutcome/QualityOutcome both caller-supplied from authoritative sources only
Unreviewed work is not equivalent to verified work.          -> QualityUnreviewed is the default; only verified/rejected finalize
Duplicate events cannot improve performance metrics.          -> idempotent Save, both Memory and real-SQLite-backed
Historical observations do not alter routing in Phase 6.     -> no routing code anywhere imports internal/performance
HarnessMesh continues to select agents.                       -> Scheduler (internal/supervisor) unchanged
Switchyard continues to select models.                        -> modelrouting/SwitchyardInferenceRouter unchanged
Decision Intelligence observes both decisions without becoming an authority over either. -> PerformanceObservation only ever references SelectionRecordRef/ModelUsages, never mutates them
Historical performance is evidence, not causal proof.        -> documented above, repeated verbatim as required
LLM output remains proposal, never authority.                -> unchanged from ADR 0002/0003/0004; this phase adds no new LLM surface at all
```
