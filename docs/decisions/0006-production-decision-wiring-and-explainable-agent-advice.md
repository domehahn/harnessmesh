# ADR 0006: Production Decision Wiring and Explainable Agent Advice (ADR 0002 Phase 7)

## Status
Implemented and verified. Depends on [ADR 0001](0001-agent-control-plane-switchyard-model-routing.md)-[0005](0005-agent-performance-and-decision-intelligence-foundation.md).

This phase closes ADR 0005's explicitly documented residual risk (no
production caller of `NewObservation`/`FinalizeWithReview`) and, only
after that wiring is proven correct, introduces the first consumer of
historical performance data in agent routing: an advisory, non-authoritative
`AgentDecisionAdvisor`.

**Performance data becomes routing input in Phase 7, but never overrides
eligibility, policy, authorization or tenant boundaries.**
**Advisor recommendations are observationally informed, not causal
guarantees.**

## Phase 7A: production wiring and decision data integrity

### Goal

Wire real production executions into `PerformanceObservation` creation,
and real production review finalization into `FinalizeWithReview`,
without weakening ADR 0005's data-integrity invariants and without ever
letting a performance-system failure affect execution truth.

### Repository analysis / gap matrix

| Concern | Existing | Target | Gap | Action |
|---|---|---|---|---|
| Execution->observation call site | none | Core-owned hook inside `Executor.Execute` | full | new `supervisor.ExecutionObserver` interface + `Executor.Observer`/`WithObserver` |
| Review->finalization call site | none | Core-owned hook inside `ReviewApplier.Apply` | full | new `review.ReviewFinalizationObserver` interface + `ReviewApplier.Observer`/`WithObserver` |
| Execution outcome source | agent-claimed text (forbidden) | Core/Executor typed state only | full | `ClassifyExecutionOutcome(supervisor.ExecutionResult, error)` - pure function over typed Core errors, no text parsing |
| Model attribution | none wired | routing/transport context only | full | `supervisor.ModelUsageRef`, populated only by `ExecutionProvider`, converted in `wiring.go` |
| Observation<->task correlation for finalization | `review.FindingObservation.SourceExecutionID` (bare `TaskID`) | find the right pending `PerformanceObservation` | partial | `ObservationFilter.TaskID` + `mostRecentPending` lookup |
| Idempotency key for a production execution | `ObservationID` (ADR 0005) exists but nothing derives `ExecutionID` from a real dispatch | deterministic, retry-distinguishing | full | `ExecutionID = fmt.Sprintf("%s@%d", task.ID, startedAt.UnixNano())` |
| Failure isolation | none wired | a performance write failure must never roll back or block execution | full | `defer` + `recover()`-wrapped observer call, nil-safe, return value never touched |

### Design: optional, nil-safe, crash-proof hooks

`Executor.Observer ExecutionObserver` and `ReviewApplier.Observer
ReviewFinalizationObserver` are optional fields (nil by default - every
existing caller of `NewExecutor`/`NewApplier` is unaffected). Both
interfaces are defined in the lower-level package that calls them
(`internal/supervisor`, `internal/review`), never in `internal/performance`
- the implementation lives in `internal/performance` and is injected via
`With*`, exactly mirroring the established `AgentDirectory`/
`FindingProjector` one-way dependency pattern from earlier phases.

`Executor.Execute` captures `startedAt`/`finishedAt` around dispatch and
invokes the observer from a `defer func() { ... recover() ... }()` that
reads the already-finalized local `result`/`execErr` values - the
observer runs strictly *after* the authoritative outcome is fixed and
can neither see nor affect it. The same pattern applies to
`ReviewApplier.Apply`'s observer call, placed after the existing
`Projector` call site and guarded by `work.Status != current.Status` (no
duplicate notification for same-status writes).

### Execution outcome: Core-derived, never agent-claimed

`ClassifyExecutionOutcome` maps `supervisor.ExecutionResult`/`error`
(`*ExecutionTimeout`, `*AuthorizationFailure`, `*PolicyRejection`,
`*AgentRoutingFailure`, `*StateTransitionFailure`, `context.Canceled`,
or a plain completed `Status`) onto the 8 `ExecutionOutcome` values. It
takes no string from agent output; there is no code path by which an
agent claiming `"success": true` in its own response text could reach
this function at all - `ExecutionResult`/`error` are Core/Executor's own
typed return values, fixed before the observer is ever invoked.

### Idempotency and retry semantics

`ExecutionID = task.ID + "@" + startedAt.UnixNano()`. An exact replay of
the same completed-execution event (identical `startedAt`/`finishedAt`,
e.g. at-least-once redelivery of a notification) reproduces the same
`ExecutionID` and is deduplicated by `PerformanceObservation.Save`'s
existing idempotency logic (ADR 0005) - no double count
(`TestNegative_DuplicateExecutionCallbackIdempotent`). A genuine retry
(new dispatch, new `startedAt`) produces a distinct `ExecutionID` and
therefore a distinct observation (`TestNegative_RetryCreatesDistinctObservation`)
- the two failure modes ("same event redelivered" vs. "new attempt")
are structurally distinguished by the timestamp that is already part of
a real retry's semantics, not by a second, separately-maintained flag.

### Finalization: most-recent-pending lookup, not reconstructed ID

`review.FindingObservation.SourceExecutionID` is a bare `supervisor.TaskID`
(no per-attempt suffix), so `ReviewFinalizationHook` cannot reconstruct
the exact `ObservationID` a finalization event should target. Instead it
queries `ObservationFilter{Tenant, TaskID}` and picks the most recently
started still-`Pending` record
(`TestNegative_StaleReviewFinalizationDoesNotCorruptLatestObservation`
proves an older pending record for the same task is not incorrectly
finalized when a newer one exists). A claim supported by multiple
agents' separate executions correctly finalizes each contributing
execution's own observation independently with the same review
outcome.

### Failure isolation

Every observer call is wrapped in `recover()`; every store error inside
the observer is routed to `WiringTelemetry`, never returned, never
logged as an execution failure. `TestNegative_MetricsStoreFailureDoesNotAffectExecutionResult`
proves a permanently-failing `PerformanceStore` does not change
`Executor.Execute`'s return value, task state, or error type in any way
- execution truth is structurally protected by the interface boundary
(`supervisor.ExecutionObserver.ObserveExecution` returns nothing),
not merely by convention.

### Explicit backfill decision: no backfill

Existing (pre-Phase-7) execution/review history has no recorded
`startedAt`/`finishedAt`/per-attempt attribution captured anywhere
reconstructible to Phase 7A's data model. Backfilling would require
either fabricating timestamps (violating "no fake implementations") or
leaving fields honestly empty/zero (which would then be
indistinguishable from a real, very-fast, zero-duration execution).
**Decision: no backfill.** Performance observations begin accumulating
from the moment Phase 7A's wiring is enabled going forward; this is
documented here rather than silently decided in code.

### Tests (Phase 7A)

13 tests in `internal/performance/wiring_test.go` plus 1 concurrency
test: real-Scheduler-creates-pending-observation, failed/timeout/
cancelled-observed, review-verified/rejected-finalizes, duplicate-event
idempotent, retry-distinct, metrics-store-failure-does-not-roll-back,
wrong-tenant-isolated, missing-task-class/attribution rejected (recorded
as attribution failure, never fabricated), stale-review-finalization
safe, plus `TestConcurrency_ExecutionAndFinalizationSafe` (20 concurrent
executions + 20 concurrent finalizations against one shared store, exact
counts, zero lost updates, clean under `-race`).

## Phase 7B: explainable agent decision advisor

### Goal

Let historical performance data advise agent selection for the first
time - strictly advisory, never authoritative, with an explicit,
observable, default-off rollout path (`disabled` -> `shadow` ->
`advisory`).

### Gap matrix

| Concern | Existing | Target | Gap | Action |
|---|---|---|---|---|
| Eligible-candidate computation | inline in `routeAgent` | shared by fallback and advisor | partial | new `eligibleCandidates` helper, called by both |
| Advisor interface | none (deliberately omitted in ADR 0005) | ranks already-eligible candidates only | full | `AgentDecisionAdvisor.Advise` in `internal/supervisor` |
| Ranking policy | none | deterministic, lexicographic, versioned | full | `DeterministicAdvisor` (`internal/performance`) |
| Mode gating | none | `disabled`/`shadow`/`advisory`, Core-configured only | full | `Scheduler.AdvisorMode`/`AdvisorPolicyVersion`, `WithAdvisor` |
| TOCTOU re-validation of advice | established pattern (Phase 2A/2B/4/5) | extend to advisor recommendations | partial | `isStillEligible` + unmodified Applier/Executor re-checks |
| Routing provenance | `ExecutionEvent` (Phase 3) | advisor-specific event kinds | partial | `EventAdvisorShadowRecommendation`/`EventAdvisorySelection`/`EventAdvisorFallback`/`EventAdvisorFailure` |

### Interface shape (as specified)

```go
type AgentDecisionAdvisor interface {
    Advise(ctx context.Context, task Task, candidates []AgentCandidate) (AgentAdvice, error)
}
type AgentAdvice struct {
    Ranked []AgentRecommendation
    Basis  AdviceBasis
}
type AgentRecommendation struct {
    AgentID    AgentID
    Confidence ConfidenceBand // insufficient/low/moderate/strong - never a float score
    Reasons    []string       // bounded, structured, auditable - never raw chain-of-thought
}
```

No composite score field exists anywhere in this shape - deliberately.
`DeterministicAdvisor.reasonsFor` returns exactly 4 bounded strings
(finalized/total count, verified/rejected/contested/needs_evidence
counts, failed/timed-out counts, median duration + rework) -
independently inspectable, never collapsed into `agent score = 87.231`.

### Eligibility: the advisor never sees an ineligible agent

`eligibleCandidates` (capability-filtered against the live
`AgentCatalog`) is computed exactly once per `Tick`, and both the
deterministic fallback and `Advisor.Advise` are given the *same* slice.
This is a structural guarantee, not a policy convention: there is no
code path in `selectAgent` that calls `Advise` with any list other than
the one `eligibleCandidates` just returned, so the advisor cannot
recommend, and by extension cannot cause the Scheduler to select, an
agent that did not already pass capability eligibility for that exact
`Tick`. `isStillEligible` re-checks the advisor's top pick against that
same list before `AgentDecisionAdvisory` mode will use it, which matters
only if a future `AgentDecisionAdvisor` implementation ever violated the
input contract - the shipped `DeterministicAdvisor` cannot violate it
by construction.

Whichever `AgentID` `selectAgent` ultimately returns - advisor-selected
or deterministic-fallback - is proposed through the **same, entirely
unmodified** `AssignTaskProposal` -> `Applier.Apply` (Validator +
Authorizer) -> `Executor.Execute` (capability/tenant/authorization
TOCTOU re-checks) path every assignment already went through in Phases
1-6. No new enforcement code was written to keep the advisor from
bypassing policy/authorization/tenant boundaries, because none was
needed: the advisor's output is just a different candidate for the same
unchanged gate.

### Ranking policy: deterministic, documented, versioned

`DeterministicAdvisor` (package `internal/performance`) implements the
interface using only `Aggregate` (ADR 0005) - no ML, no bandits, no
learned weights. Lexicographic order, applied only within the
sufficient-data bucket:

1. higher verified/finalized ratio
2. lower rejected/finalized ratio
3. lower (failed+timed_out)/observed ratio
4. lower total rework count
5. lower median duration
6. deterministic `AgentID` tie-break

`AdviceBasis` carries `PolicyVersion` (default `"performance-v1"`,
`DefaultAdvisorPolicyVersion`) so a historical selection remains
explainable even after a future rule-order change - no silent rule
changes.

### Sample size and cold start

`DefaultMinimumObservationCount` (10, ADR 0005, reused unchanged) gates
whether a candidate is "sufficient" or "cold start" for *this task
class*. **Cold-start policy:**
- all candidates cold-start -> `AgentAdvice{Ranked: nil, Basis:
  "all_cold_start_fallback"}` - `selectAgent` treats an empty `Ranked`
  identically to "no advisor configured," i.e. the existing
  deterministic router is used, full stop.
- some sufficient, some cold-start -> sufficient candidates are ranked
  normally; cold-start candidates are appended, unranked relative to the
  sufficient ones, `Confidence: ConfidenceInsufficient`, **never**
  assigned a low/bad score (`TestAdvisor_ColdStart_MixedNotPenalized`).

No exploration (no bandit, no forced trial of a cold-start agent) is
implemented in Phase 7, as instructed.

### Confidence: qualitative bands, no fake probability

`confidenceFor` returns `ConfidenceLow` below the finalized-count
threshold, `ConfidenceStrong` only when `FinalizedCount >= 2x` the
minimum *and* the dominant quality outcome's share is `>= 0.8`,
`ConfidenceModerate` otherwise - a stated rule over two observable
quantities (sample size, outcome consistency), never a fabricated
statistical probability.

### Difficulty matching and bias disclaimers

`Aggregate`/`DeterministicAdvisor` are always scoped to one exact
`TaskClass`; there is no code path that compares or combines counts
across different task classes
(`TestNegative_CrossTaskClassComparisonNeverHappens` proves a perfect
`documentation` history for an agent contributes nothing toward a
`security_review` ranking - the agent is correctly treated as cold-start
for that class). As ADR 0005 already states and this phase repeats:
**historical performance is observational evidence, not causal proof**
- under observed historical assignments for this task class, one agent
had stronger verified outcomes than another; this is never reported or
implied as "Agent A is intrinsically better than Agent B."

### Tenant isolation

`DeterministicAdvisor.Tenant` is a required field; `Advise` fails closed
if unset, and fails closed if `task.Tenant` doesn't match it
(`TestNegative_CrossTenantAdviceRejected`). Every `Aggregate` call inside
`Advise` is scoped to `a.Tenant` only - an agent with strong history in
another tenant is correctly treated as cold-start when advising within
this tenant, never silently credited with foreign-tenant history
(`TestNegative_CrossTenantHistoryNotLeakedIntoAdvice`). There is no "all
tenants" mode for advice, unlike `ObservationFilter.AllTenants` (an
explicit operator-tooling opt-in elsewhere in this package) - advisor
ranking has no such exception.

### Model neutrality

`AgentRecommendation`/`AgentAdvice` have no model, provider, or route
field at all - verified structurally via reflection
(`TestNegative_AdvisorCannotSelectModelOrProvider`), not merely by
convention. The advisor selects agents; Switchyard
(`modelrouting`/`SwitchyardInferenceRouter`, unchanged) remains the sole
model router. Observing "Agent A + Model X historically strong" (via
`AggregateModelUsage`, already a separate function from `Aggregate` per
ADR 0005) never becomes "select Agent A and force Model X" - joint
agent+model routing remains explicitly deferred.

### Modes: disabled / shadow / advisory (no autonomous)

```go
const (
    AgentDecisionDisabled AgentDecisionMode = "disabled" // advisor never called
    AgentDecisionShadow   AgentDecisionMode = "shadow"   // recorded only, zero production effect
    AgentDecisionAdvisory AgentDecisionMode = "advisory" // may be used after full re-validation
)
```

There is no `autonomous` mode anywhere in the type. The zero value of
`AgentDecisionMode` is `""`, which `selectAgent` treats identically to
`AgentDecisionDisabled` - so `Scheduler`'s default (before `WithAdvisor`
is ever called) is conservatively disabled, never auto-activated into
`advisory`. `TestAdvisor_DisabledModeNeverInvokesAdvisor` proves zero
calls in disabled mode; `TestAdvisor_ShadowModeNeverAltersSelection` and
the real-advisor equivalent
`TestEndToEnd_ShadowMode_RealAdvisorRecommendsButActualSelectionUnchanged`
prove the advisor is called (and its recommendation differs from the
fallback, using the exact 20/12/6-vs-20/18/1 history specified) but the
actual selection is still the deterministic fallback.

### Fallback semantics: always explicit, never silent

Every path that results in using the deterministic fallback instead of
the advisor's top recommendation records a distinct, typed
`ExecutionEvent`:

- advisor error -> `EventAdvisorFailure` (`TestAdvisor_FailureFallsBackDeterministically`)
- `shadow` mode -> `EventAdvisorShadowRecommendation` (always, by mode definition)
- `advisory` mode, recommendation fails `isStillEligible` -> `EventAdvisorFallback`
  (`TestAdvisor_AdvisoryModeFallsBackWhenRecommendedAgentIneligible`)
- `advisory` mode, recommendation passes -> `EventAdvisorySelection`

There is no code path that falls back to the deterministic router
without recording one of these events (except the uninteresting
"advisor not configured at all" / "advice was empty" cases, which are
behaviorally identical to `disabled` and need no event, since nothing
advisory was attempted).

### Routing provenance

`ExecutionEvent.Detail` on the advisor-related kinds records
`shadow recommendation=<id> actual=<id> basis=<policy> policy=<version>`
or `selected=<id> basis=<policy> policy=<version>` or an explicit
fallback reason - bounded, structured, never a raw metrics dump and
never the advisor's internal reasoning text.

### Feedback-loop / selection-bias awareness (not solved)

Every advisor-influenced `Tick` produces a distinct, typed event
(`EventAdvisorySelection` vs. no advisor event at all for a
fallback-only tick), so "was this selection advisor-influenced" is
always reconstructible from the event stream for future bias analysis.
**No automatic correction is implemented** - Phase 7 does not attempt to
detect or correct a feedback loop where advisor-driven selections change
the future training distribution; this is documented as a residual risk
below, per the request's explicit instruction not to solve it yet.

### Observability

`internal/supervisor.AdvisorTelemetry` (`advisor_calls`,
`advisor_failures`, `advisor_shadow_recommendations`,
`advisor_advisory_selections`, `advisor_fallbacks`) is recorded from
`Scheduler.selectAgent`, mirroring the existing `RegistryTelemetry`
pattern. `internal/performance.AdvisorQueryTelemetry`
(`advisor_insufficient_data`, `advisor_query_duration`) is recorded from
`DeterministicAdvisor.Advise` itself, kept as a separate interface from
the Scheduler-level one because it measures advisor-internal query
behavior, not routing-decision outcomes - both default to no-ops and
both reuse `internal/telemetry.Registry`, no second telemetry system.

### Tests (Phase 7B)

`internal/supervisor/scheduler_advisor_test.go` (5 tests, fake advisor
double): disabled-never-invokes, shadow-never-alters, advisory-selects-
when-eligible, advisory-falls-back-when-ineligible,
failure-falls-back-with-explicit-event.

`internal/performance/advisor_test.go` (12 tests, real
`DeterministicAdvisor`): the exact specified shadow scenario (ranks B
over A given 20/12/6 vs 20/18/1), all-cold-start fallback, mixed
cold-start not penalized, insufficient-sample not fabricated strong,
cross-tenant advice rejected, cross-tenant history not leaked,
cross-task-class never compared, unknown task class rejected, policy
version defaults correctly, advisor cannot select model/provider
(reflection-based structural proof), empty candidate list produces no
advice, recommendation reasons bounded.

`internal/performance/advisor_scheduler_integration_test.go` (3 tests,
real `Scheduler` + real `DeterministicAdvisor`, not the fake double):
the exact end-to-end shadow scenario (legacy selects agent-A, shadow
advisor recommends agent-B, actual execution stays agent-A, both facts
recorded in the event stream), advisory mode selecting the
re-validated recommendation (agent-B) end-to-end, and a scenario proving
the advisor structurally cannot even consider an unavailable
higher-history agent (agent-B, absent from the directory) because it is
never shown as a candidate in the first place - no TOCTOU window exists
here for the real advisor to fall through.

## Phase 7C: decision attribution and tenant closure

Phase 7C is a **closure phase**: it adds no new routing intelligence
(no ranking-policy change, no ML, no bandits, no exploration, no
Agent+Model joint routing). It closes four integrity gaps left open by
7A/7B: (1) Review -> Execution Attribution, (2) Tenant Filtering Before
Advice, (3) Observer Failure Visibility, (4) Core-owned Execution
Identity.

### Repository analysis (performed before writing code)

- `review.EvidenceRecord.ExecutionID string` already existed (ADR 0004,
  Phase 5) as a genuine Core-owned binding point - but no production
  code populated it (`EngineEvidenceStore.Put`/`Get` is pure pass-through
  persistence). Reused, not replaced, as the preferred secondary
  resolution path (see below).
- `review.FindingObservation.SourceExecutionID` (typed `TaskID`) existed
  but was read by no logic anywhere - safe to extend without breaking
  any consumer.
- No production call site anywhere in the repository constructs a
  `FindingObservation`/`ReviewGroup` from a real agent submission
  (`review.ReviewState.Ingest` has zero production callers) - this
  predates Phase 7 and remains out of scope; building that integration
  would be new feature work, not closure.
- `ExecutionID` was NOT Core-owned before this phase: it was invented
  ad-hoc inside `internal/performance`'s observer
  (`task.ID+"@"+startedAt.UnixNano()`), after the fact, not generated by
  `Executor` at the actual execution boundary.
- `eligibleCandidates` (`internal/supervisor/scheduler.go`) filtered by
  capability only, never tenant, confirmed by direct code inspection.

### Gap matrix

| Concern | Existing | Target | Gap | Action |
|---|---|---|---|---|
| Execution identity | Derived ad-hoc in `internal/performance` from `task.ID+startedAt` | Core-owned, generated by `Executor` at the dispatch boundary | full | new `supervisor.ExecutionID` + `NewExecutionID`, set on `ExecutionResult`, passed explicitly to `ExecutionObserver` |
| Finding->Execution link | `SourceExecutionID TaskID` (coarse, unused by logic) | exact per-attempt binding | partial | new `FindingObservation.SourceExecutionRef string` (Core-owned, optional) |
| Evidence->Execution link | `EvidenceRecord.ExecutionID string` (Phase 5, unpopulated in practice) | reused as-is | none (type-level) | no new field; documented as the secondary resolution path |
| Review->Execution link | heuristic "most-recently-started pending for TaskID" | exact deterministic lookup | full | `ReviewFinalizationHook` rewritten to resolve `(Tenant, TaskID, ExecutionID)` exactly, `Store.Get` by `ComputeObservationID`, fail closed when unresolvable |
| Tenant candidate filtering | `eligibleCandidates` ignores tenant; `Executor` re-checks at dispatch | pre-filtered before the advisor sees candidates | full | `eligibleCandidates` extended with the same `TenantDirectory` fail-closed pattern `Executor.Execute` already used |
| Observer failure telemetry | store errors recorded; panics silently dropped | panics observable, no secrets logged | partial | new `ExecutionObserverFailureSink`/`ReviewObserverFailureSink` (parameterless methods - no payload can ever be passed through), auto-wired by `WithObserver` |

### 1. Review -> Execution Attribution

`ReviewFinalizationHook.ObserveReviewTransition` no longer queries for
"the most recently started pending observation for this TaskID." For
each `FindingObservation`, it resolves the EXACT execution it
originated from, in order:

1. `FindingObservation.SourceExecutionRef`, if set - the Core-owned,
   per-attempt execution identity (a string form of
   `supervisor.ExecutionID`), set only from real execution context.
2. Otherwise, `FindingObservation.EvidenceRefs` resolved against an
   `EvidenceStore` (if configured): each referenced `EvidenceRecord`'s
   own `ExecutionID` is read, scoped to the same tenant and task. If
   more than one DISTINCT `ExecutionID` is found this way for the SAME
   observation, that is ambiguous provenance and fails closed (not
   finalized, recorded as an attribution failure) - never resolved by
   guessing.

An observation whose execution cannot be resolved by either path is
skipped (recorded as an attribution failure), never finalized against
some unrelated execution. The resolved `(Tenant, TaskID, ExecutionID)`
triple is used to compute the EXACT `PerformanceObservation.ID`
(`ComputeObservationID`) and fetch it directly (`Store.Get`) - there is
no query-and-sort-by-timestamp step anywhere in this path anymore.

**Quality attribution semantics (decided, not assumed):** a review claim
can be supported by multiple agents' executions (the claimant's own,
plus every independent reviewer's). Each contributing execution's OWN
`PerformanceObservation` is finalized independently with the SAME review
outcome - unchanged from 7A's documented rule, now implemented via exact
resolution instead of a heuristic. There is no "pick one execution" step
- every resolvable, distinct `(TaskID, ExecutionID)` pair found across
the group's observations is finalized.

**Stale review protection**: a late-arriving review of an older
execution's finding resolves to that execution's OWN exact
`ExecutionID` and can therefore never touch a newer execution's
observation, regardless of arrival order - proven by
`TestAttribution_LateReviewOfOlderExecutionNeverTouchesNewer` and
`TestAttribution_OutOfOrderReviewsEachFinalizeTheirOwnExecution`.

### 2. Core-owned Execution Identity

`supervisor.ExecutionID` (new type) is generated by `NewExecutionID`
using `crypto/rand` (mirroring the existing random-ID pattern in
`internal/provider/chatgpt_siwc.go` rather than adding a UUID
dependency) - uniqueness never depends on wall-clock time alone.
`Executor.Execute` generates it immediately at the execution boundary,
before the provider is ever called, and sets it on `ExecutionResult`
UNCONDITIONALLY after the provider returns - overwriting anything a
provider may have populated, never trusted from agent/provider output.
It is passed explicitly to `ExecutionObserver.ObserveExecution` as its
own parameter (not re-derived from timestamps inside the observer, as
7A originally did). `internal/performance`'s `SupervisorExecutionObserver`
now uses this Core-owned value directly - the ad-hoc
`task.ID+"@"+startedAt.UnixNano()` derivation inside `internal/performance`
is gone.

### 3. Tenant Filtering Before Advice

`eligibleCandidates` now filters by tenant, not just capability, for a
tenant-scoped task - using the exact `TenantDirectory` fail-closed
pattern `Executor.Execute` already used (type-assert the catalog to
`TenantDirectory`; if a tenant-scoped task has no such authority, return
zero candidates via a typed `*AuthorizationFailure`, never "include all
and let Executor decide later"). Since both `routeAgent`'s deterministic
fallback and `Scheduler.selectAgent`'s advisor consultation share this
one computation, a cross-tenant agent is now removed from BOTH paths at
the same point - before the advisor, the fallback, or any provenance
event could ever reference it. An unscoped task (`Task.Tenant == ""`) is
completely unaffected (`TestTenantFiltering_UnscopedTaskUnaffected`).

**Defense in depth preserved, not replaced**: `Executor.Execute`'s own
live TOCTOU tenant re-check (Phase 2B) is unchanged and still runs
immediately before dispatch - `TestTenantFiltering_TenantChangeAfterAdviceRejectedAtExecution`
proves that even if an agent's tenant membership changes between advice
and dispatch (simulating the live directory mutating mid-`Tick`,
inside the advisor's own call, before `Executor` re-checks), execution
is still rejected at the dispatch boundary.

**A real defect was found and fixed during this work**: `selectAgent`'s
`AgentDecisionShadow` branch recorded the advisor's raw recommendation
into `ExecutionEvent.Detail` verbatim, with no eligibility check at all
- a buggy or malicious advisor naming a tenant-filtered (or otherwise
ineligible) agent would have leaked that agent's ID into shadow
telemetry/provenance, even though shadow mode has zero effect on actual
selection. Found by
`TestTenantFiltering_ForeignAgentAbsentFromShadowProvenance` failing on
first run. Fixed: the shadow branch now calls the same `isStillEligible`
check the advisory branch already used, and discards (does not log) a
recommendation naming an ineligible candidate, recording
`EventAdvisorFailure` with a generic detail instead of ever naming the
filtered agent.

### 4. Observer Failure Visibility

`supervisor.ExecutionObserverFailureSink` and
`review.ReviewObserverFailureSink` are new, narrow, optional interfaces
(`RecordExecutionObserverPanic()` / `RecordReviewObserverPanic()` - both
take **zero parameters**, a structural guarantee, not a convention, that
a recovered panic's raw value can never be passed through to telemetry;
proven by `TestObserverFailure_SinkInterfaceCarriesNoPayload` via
reflection). `Executor.FailureSink` / `ReviewApplier.FailureSink` are
optional fields, nil-safe, auto-wired by `WithObserver` whenever the
attached observer also happens to implement the sink interface (one call
configures both roles) - `internal/performance`'s
`SupervisorExecutionObserver` and `ReviewFinalizationHook` both implement
their respective sink interfaces, delegating to the SAME `WiringTelemetry`
used for store-error failures, extended with
`RecordExecutionObserverPanic`/`RecordReviewObserverPanic`.

The existing `recover()` wrapper around each observer call (already
present since 7A, to guarantee a panicking observer can never affect
`Execute`'s/`Apply`'s own return value) now additionally calls the
`FailureSink` from inside its own `recover()`, itself wrapped in a
SECOND `recover()` - so a `FailureSink` that itself panics can never
crash `Execute`/`Apply` either
(`TestObserverFailure_FailureSinkItselfPanickingDoesNotCrashExecute`,
`TestObserverFailure_ReviewFailureSinkPanickingDoesNotCrashApply`).

**No retry infrastructure was added.** The mandate explicitly asked
whether performance writes should be retryable; this repository has no
existing retry/event-redelivery infrastructure that could be reused
cleanly for this without building new, unaudited infrastructure
(explicitly out of scope for a closure phase - "kein versteckter
Goroutine Retry Loop"). **Decision: no retry.** Data loss from a
failed write remains fully observable (every failure path already
increments a `WiringTelemetry` counter), which satisfies the mandate's
minimum bar ("mindestens muss Data Loss observierbar sein") without
adding unaudited retry machinery.

### Concurrency review

`TestConcurrency_TwoReviewsFinalizeTheirOwnExecutionsIndependently` runs
two reviews (each naming a different execution of the same `TaskID`)
concurrently against one shared store under `-race`: no data race, no
cross-execution attribution, no lost or duplicated finalization. The
pre-existing `TestConcurrency_ExecutionAndFinalizationSafe` (20 concurrent
executions + 20 concurrent finalizations, ADR 0006 Phase 7A) continues
to pass unchanged with the new Core-owned `ExecutionID` plumbed through.
No deadlocks: the attribution-resolution logic added in this phase
performs no locking of its own beyond what `PerformanceStore`/
`EvidenceStore` already provide internally.

### TOCTOU (extended)

The pre-existing TOCTOU chain (advisor sees eligible candidates -> time
passes -> Core selects -> Executor revalidates live state) is unchanged.
Phase 7C adds one more instance of the same discipline: review
finalization resolves the EXACT `ExecutionID` first, then fetches the
corresponding `PerformanceObservation` fresh via `Store.Get` immediately
before finalizing - there is no cached/snapshotted observation carried
across any window; `FinalizeWithReview` always operates on what `Get`
just returned.

## Security closure review (all of Phase 7)

Checked explicitly, no further code change required except where noted:

| Question | Finding |
|---|---|
| Can a performance write failure affect execution truth? | No - `recover()`-wrapped, nil-safe observer interface returns nothing; proven by `TestNegative_MetricsStoreFailureDoesNotAffectExecutionResult`. |
| Can stale review data finalize the wrong observation? | No (strengthened in 7C) - exact `(Tenant, TaskID, ExecutionID)` resolution and direct `Store.Get`, never a "most recent pending" guess; proven by the new `TestAttribution_*` suite. |
| Forged `ExecutionID` in model/agent output? | Structurally impossible - `ExecutionID` is generated by `Executor` itself via `crypto/rand`, set unconditionally on `ExecutionResult` AFTER the provider returns (overwriting anything the provider set), and never read back from agent/LLM text anywhere in `internal/performance` or `internal/review`. |
| Evidence references another execution / Finding references unrelated evidence? | `resolveExecutionID` only accepts an `EvidenceRecord` whose `Tenant` AND `TaskID` match the observation being resolved; a mismatched record is simply ignored, never used (`TestNegative_EvidenceFromWrongTenantNeverResolves`). |
| Review tries to finalize an unrelated task's execution? | No - resolution is keyed by the SAME `TaskID` the observation declares; an evidence record for a different task never matches. |
| Duplicate replay of a review transition? | Idempotent - re-finalizing with the same verdict is a safe no-op (`TestAttribution_FinalizedTwiceIsIdempotent`, `TestAttribution_E1ReviewReplayCannotMutateE2`). |
| Can the advisor bypass eligibility? | No - structurally cannot see an ineligible candidate at all (unchanged from 7B, now additionally tenant-filtered at the source). |
| Can the advisor recommend another tenant's agent? | No (closed in 7C) - `eligibleCandidates` now filters by tenant before the advisor ever sees the candidate list; `Executor.Execute`'s existing fail-closed tenant re-check remains as defense-in-depth. |
| Can a cross-tenant agent sneak into candidates or provenance? | No - `TestTenantFiltering_ForeignTenantAgentNeverPassedToAdvisor` / `TestTenantFiltering_ForeignAgentAbsentFromShadowProvenance` (the latter caught and fixed a real defect - see "3. Tenant Filtering" above). |
| Can historical data leak cross-tenant into the advisor's own ranking? | No - `DeterministicAdvisor.Tenant` required, fails closed on mismatch, every query scoped to it (unchanged from 7B). |
| Can an agent manipulate its own metrics? | No - unchanged from ADR 0005/7A (`TestSecurity_AgentCannotSelfReportSuccess`, `TestSecurity_ModelCannotSelfReportIdentity`, `TestSecurity_CopiedReviewNotCountedAsIndependent`, all still passing). |
| Can a recommendation survive capability revocation? | No - `eligibleCandidates` is recomputed fresh every `Tick`. |
| Can shadow mode accidentally influence routing? | No - `AgentDecisionShadow`'s branch in `selectAgent` always returns `fallback`, never `top`; structurally, not conditionally. |
| Can advisor failure silently change behavior? | No - every failure/fallback path records a distinct typed event. |
| Observer panic / malformed panic value / sensitive error content? | Never reaches telemetry - `RecordExecutionObserverPanic`/`RecordReviewObserverPanic` take zero parameters, a structural guarantee proven via reflection (`TestObserverFailure_SinkInterfaceCarriesNoPayload`). |
| Recursive telemetry failure (the failure sink itself panics)? | Contained by a second, nested `recover()` - proven by `TestObserverFailure_FailureSinkItselfPanickingDoesNotCrashExecute` / `..._ReviewFailureSinkPanickingDoesNotCrashApply`. |

## Tests / Verification

```text
gofmt -l .                  -> clean
go build ./...              -> clean
go vet ./...                -> clean
go test -race ./...         -> all packages ok, zero regressions
```

`internal/performance`: 76 tests passing under `-race` (`go test -v`
count; +11 from Phase 7C's attribution suite). `internal/supervisor`:
132 tests passing under `-race` (+11 from Phase 7C's tenant-filtering
and observer-failure suites). `internal/review`: 75 tests passing under
`-race` (+2 from Phase 7C's review-observer-failure suite). Zero
regressions anywhere in the repository, with one exception requiring a
deliberate test update (not a regression in behavior): `TestNegative_CrossTenantTaskAssignment`
(`internal/supervisor/workflow_test.go`) previously asserted a
cross-tenant agent reached the downstream Authorizer-rejection path;
Phase 7C's tenant pre-filter now blocks it one step earlier (an
`EventAgentUnavailable`/"no eligible agent" outcome), so the test was
updated to assert the stronger, earlier-blocking behavior while still
proving the same underlying security property (cross-tenant assignment
never commits). Existing fuzz targets re-run clean at this checkpoint
(`FuzzDecodePlanResponse`, `FuzzDecodeSynthesisResponse`,
`FuzzDetectDuplicateKeys` in `internal/supervisor`;
`FuzzDecodeArbitrationResponse`, `FuzzEvidenceValidate` in
`internal/review`) - no new fuzz target added, since Phase 7C introduces
no new untrusted-parsing/wire-decode boundary (every new field -
`ExecutionID`, `SourceExecutionRef` - is Core-generated or caller-supplied
typed Go data, never decoded from untrusted bytes).

## Residual risks

- **No production caller constructs `FindingObservation`/`ReviewGroup`
  from a real agent submission anywhere in the repository**
  (`review.ReviewState.Ingest` has zero production callers). Phase 7C's
  attribution fix is fully exercised and correct against realistic
  simulated call sequences (exactly as 7A/7B were), but the deeper gap of
  "nothing in `internal/collaboration`/`internal/mcp` yet constructs a
  real `FindingObservation` with `SourceExecutionRef` set from a live
  `ExecutionResult`" predates Phase 7 and remains open - building that
  integration is new feature work, not closure, and was correctly left
  out of scope.
- **Feedback-loop bias is recorded, not corrected** (unchanged from 7B).
- **`DefaultMinimumObservationCount` remains a single, hardcoded
  constant** (unchanged from ADR 0005).
- **No backfill of pre-Phase-7A execution/review history** (unchanged
  decision from 7A).
- **No retry for failed performance writes** - a deliberate decision
  (see "4. Observer Failure Visibility" above), not an oversight; data
  loss remains observable via telemetry counters, never silent.
- **Shadow mode still requires an explicit second rollout step
  (advisory) with no automatic promotion** - intentional, unchanged from
  7B.

## Final invariants (proven by the test suite)

```text
A review finalizes exactly the execution it actually pertains to.         -> ReviewFinalizationHook resolves the EXACT (Tenant, TaskID, ExecutionID) and fetches by ComputeObservationID - no heuristic
A retry cannot steal an older execution's review.                        -> each attempt gets its own Core-generated ExecutionID (NewExecutionID); TestAttribution_LateReviewOfOlderExecutionNeverTouchesNewer
A stale review cannot finalize a newer execution.                        -> TestAttribution_OutOfOrderReviewsEachFinalizeTheirOwnExecution / TestAttribution_E1ReviewReplayCannotMutateE2
Execution identity is Core-owned.                                        -> supervisor.NewExecutionID, generated by Executor before the provider is called, set unconditionally on ExecutionResult afterward
Agents/models cannot forge authoritative ExecutionID.                    -> ExecutionID is never read from agent/provider/LLM output anywhere
Evidence preserves execution provenance.                                 -> EvidenceRecord.ExecutionID remains the Core-owned secondary resolution path, tenant- and task-scoped before use
Cross-tenant agents are removed before Advice.                           -> eligibleCandidates tenant-filters before both routeAgent's fallback and Advisor.Advise see the candidate list
Cross-tenant performance data cannot influence Advice.                   -> DeterministicAdvisor.Tenant required, fails closed (unchanged from 7B)
Executor still performs live tenant revalidation before dispatch.        -> Phase 2B's TOCTOU tenant re-check is unchanged; TestTenantFiltering_TenantChangeAfterAdviceRejectedAtExecution
Performance-system failure cannot alter execution truth.                 -> recover()-wrapped, nil-returning observer interfaces; TestObserverFailure_PanicDoesNotAffectExecutionResult
Performance-system failure cannot disappear silently.                    -> WiringTelemetry + ExecutionObserverFailureSink/ReviewObserverFailureSink record every failure/panic path
Decision Intelligence advises; Core decides.                             -> unchanged from 7B
HarnessMesh selects agents.                                              -> Scheduler remains the sole caller of Applier/Executor
Switchyard selects models.                                               -> modelrouting/SwitchyardInferenceRouter unchanged
LLM output remains proposal, never authority.                            -> unchanged from ADR 0002/0003/0004/0005; Phase 7 adds no new LLM surface
```
