# ADR 0009: Production Evaluation Wiring and Policy Identity Closure (ADR 0002 Phase 9C)

## Status

Implemented and verified. Depends on [ADR 0001](0001-agent-control-plane-switchyard-model-routing.md)-[0008](0008-controlled-exploration-and-routing-evaluation.md).

## Purpose

Phase 9C closes four residual points ADR 0008 left documented and open,
before any production exploration may begin:

1. Production decisions were not automatically captured.
2. `CandidateSnapshot` capture was manual.
3. `RoutingPolicyDescriptor.Fingerprint` was caller-supplied, with no
   conflict detection.
4. `InvalidRecommendationCount` had no typed reason granularity.

**Every evaluation-eligible production decision automatically captures
the exact authoritative decision context that existed when the
production Agent was selected.**

```text
CandidateSnapshots are captured from the exact Core-authorized
production candidate set.
Evaluation-eligible production decisions do not depend on callers
to manually create evaluation context.
Policy identity is captured when the production decision occurs.
Policy ID and version cannot silently refer to changed routing
semantics.
Evaluation failures cannot change production truth, but missing
evaluation coverage remains visible.
```

## Non-goals (explicit)

No challenger execution, no traffic splitting, no exploration
probability, no canary routing, no budgeted exploration, no bandits, no
reinforcement learning, no automatic promotion, no automatic rollback,
no model-side experimentation. Phase 10 has not begun.

## Repository analysis (performed before writing code)

- The real production path - `Scheduler.selectAgent` -> `eligibleCandidates`
  (tenant/capability/availability-filtered) -> deterministic fallback or
  Advisor -> `Executor.Execute` -> Core-owned `ExecutionID` (ADR 0006
  Phase 7C) -> `PerformanceObservation` -> `ReviewFinalizationHook` - was
  traced end to end and found UNCHANGED by Phase 9 (ADR 0008 deliberately
  built its evaluation plane entirely in `internal/performance`, never
  touching `internal/supervisor`). This confirmed ADR 0008's own stated
  residual risk precisely: nothing automatically captures a
  `CandidateSnapshot` or invokes `ShadowEvaluator` from a real decision.
- No stable "decision identity" existed anywhere - `selectAgent` runs
  once per Tick-dispatch per task, computes a result, and returns; there
  was no Core-owned ID naming that specific selection EVENT (distinct
  from `TaskID`, which spans every attempt/retry, and from
  `ExecutionID`, which identifies the subsequent execution). New:
  `supervisor.DecisionID`/`NewDecisionID`, generated once per
  `selectAgent` call, mirroring `NewExecutionID`'s `crypto/rand` pattern
  exactly.
- Historical candidate sets were confirmed NOT reconstructible before
  this phase (`eligibleCandidates`' output is ephemeral, computed fresh
  every Tick, never persisted) - this phase's `CandidateSnapshot`
  capture closes that gap going forward; it does NOT retroactively
  reconstruct anything from before this wiring existed.
- Historical Advisor data had no "as of" time-filtering capability
  before ADR 0008 Phase 9 itself introduced `TimeFaithfulDataset` -
  reused unchanged here; Phase 9C adds no new time-filtering concept.

### Gap matrix

| Concern | Current | Required | Gap | Planned change |
|---|---|---|---|---|
| Production decision capture | None - `Scheduler` had no evaluation hook at all | Automatic, Core-owned, at the exact point the final candidate set is authoritative | full | new `supervisor.RoutingDecisionObserver`/`WithEvaluation`, called once per `selectAgent` |
| Snapshot ownership | Manually constructed by test/caller code (ADR 0008) | Core-owned, built only from `eligibleCandidates`' final output | full | `ProductionEvaluationCoordinator.ObserveRoutingDecision` builds and persists it automatically |
| Champion identity | Not captured at all for production decisions | Captured at selection time, including advisor-attempted-but-failed provenance | full | `supervisor.PolicyProvenance`, `LegacyDeterministicPolicyID/-Version`, `PolicyIdentifiable` |
| Policy fingerprint | Caller-supplied, no conflict detection (ADR 0008) | Deterministic, canonical, conflict-detected | full | `NewRoutingPolicyDescriptor`/`computeCanonicalFingerprint`, `ShadowEvaluator.checkPolicyIdentity` |
| Challenger invocation | Manual `ShadowEvaluator.Evaluate` call (ADR 0008) | Automatic, from the captured decision | full | `ProductionEvaluationCoordinator.ObserveRoutingDecision` |
| Outcome attachment | Manual `AttachChampionOutcome` call (ADR 0008) | Automatic at execution AND finalization time | full | `ProductionEvaluationCoordinator.ObserveExecution`/`ObserveReviewTransition`, new `AttachChampionQuality` |
| Evaluation coverage | None | Decision-level and record-level accounting, gating promotion | full | `EvaluationCoverage`/`ComputeEvaluationCoverage`/`EvaluatePromotionGateWithCoverage` |
| Invalid reason taxonomy | Single hard-blocker count only (ADR 0008) | Typed, provable reasons, same hard-blocker semantics | full | `InvalidRecommendationReason`, `RoutingEvaluationRecord.InvalidReason` |
| Failure visibility | 7A/7C-style telemetry pattern, not yet applied to the new wiring | Same pattern, extended | full | `ProductionWiringTelemetry` |

## 1-2. Production decision capture / Core-owned CandidateSnapshot

`Scheduler.selectAgent` now routes EVERY return path through one choke
point, `captureDecision`, which fires (only if `EvaluationMode ==
EvaluationShadow` and an observer is configured - default
`EvaluationDisabled`, the conservative default the mandate requires)
with the FINAL candidate set (`ids`, unchanged after this point) and
whichever `(agent, policy)` that call is about to return:

```go
capture := func(agent AgentID, policy PolicyProvenance) {
    s.captureDecision(ctx, task, ids, agent, policy)
}
```

`ProductionEvaluationCoordinator.ObserveRoutingDecision` (implementing
the new `supervisor.RoutingDecisionObserver`) builds a `CandidateSnapshot`
from EXACTLY `decision.CandidateAgentIDs` - never re-queries a live
registry, never widens or substitutes the set - and persists it via
`CandidateSnapshotStore.Save` before evaluating any Challenger. No
external caller can substitute a different candidate universe for
production evaluation: there is no other code path in the production
wiring that calls `Save` at all.

`TestProductionWiring_AutomaticSnapshotNoManualCall` proves a real
`Scheduler.Tick` call alone produces a persisted snapshot, with zero
manual snapshot construction anywhere in the test.
`TestProductionWiring_ForeignTenantCandidateAbsentFromSnapshot` proves a
cross-tenant agent (present in the directory, under a different tenant)
never appears in the captured snapshot - it was already excluded by
`eligibleCandidates` (ADR 0006/0007) before `captureDecision` ever runs.
`TestProductionWiring_SnapshotStableDespiteLaterRegistryChange` proves
a live registry mutation AFTER the decision never alters the
already-persisted snapshot.

## 3. Snapshot fingerprint / canonicalization

`CandidateSnapshot.fingerprint()` (unchanged from ADR 0008) already
sorts `AgentIDs` before hashing - `NewCandidateSnapshot` deduplicates
and sorts deterministically, so map/slice iteration order from
`eligibleCandidates`' own construction never leaks into snapshot
identity. No change was needed here; this phase's repository analysis
confirmed ADR 0008's existing snapshot identity already satisfied this
requirement.

## 4. Decision identity

```go
type DecisionID string
func NewDecisionID(taskID TaskID) DecisionID
```

Generated once per `selectAgent` call via `crypto/rand`, mirroring
`NewExecutionID` exactly - never derived from a timestamp alone.
`TestDecisionCapture_RetryProducesNewDecisionID` proves two calls for
the same `TaskID` never collide.

## 5. Consolidating existing shadow paths

Phase 7B's Advisor-shadow mechanism (`Scheduler.AgentDecisionMode`/
`EventAdvisorShadowRecommendation`) and Phase 9's `ShadowEvaluator` were
reviewed together, per the mandate's explicit "inspect for duplicate
shadow concepts" instruction. They serve genuinely distinct purposes
(7B: live routing-decision telemetry inside `selectAgent` itself; 9C:
replayable, persisted, multi-challenger evaluation records) and are NOT
merged into one mechanism - instead, **one authoritative production
shadow-evaluation path** is established by having a SINGLE
`ProductionEvaluationCoordinator` object implement all three relevant
hook interfaces (`supervisor.RoutingDecisionObserver`,
`supervisor.ExecutionObserver`, `review.ReviewFinalizationObserver`),
wired via the SAME existing `WithObserver`/`WithEvaluation` methods
Executor/ReviewApplier/Scheduler already had. No parallel orchestration
framework was introduced - Executor/ReviewApplier/Scheduler still only
ever call the single-observer hook points they always had; this
coordinator is simply what gets plugged into all three now, delegating
each hook's pre-existing job UNCHANGED before doing its new work.

## 6. Champion policy provenance

```go
type PolicyProvenance struct {
    PolicyID, PolicyVersion string
    AttemptedPolicyID, AttemptedPolicyVersion string
    FallbackUsed bool
}
const LegacyDeterministicPolicyID = "legacy-deterministic"
const LegacyDeterministicPolicyVersion = "v1"
```

`ChampionPolicy` is NEVER empty for an evaluation-eligible decision:

- No advisor configured / disabled / shadow mode (never selects the
  advisor's pick) -> `legacy-deterministic/v1`.
- Advisor configured, succeeds, TOCTOU-eligible, advisory mode -> the
  advisor's OWN identity (`PolicyIdentifiable.PolicyID()/PolicyVersion()`
  if implemented - `DeterministicAdvisor` now implements this directly;
  else the single combined `AdviceBasis` string as `PolicyID` with an
  empty `Version`, strictly better than nothing).
- Advisor fails, returns insufficient data, or its pick fails TOCTOU
  re-validation -> `legacy-deterministic/v1` with `FallbackUsed=true`
  and `AttemptedPolicyID/-Version` preserving what was tried - "advisor
  attempted, advisor failed, legacy fallback selected Agent A" is
  exactly what gets captured, never the failed advisor's identity as
  Champion.

`TestDecisionCapture_ShadowModeCapturesLegacyChampion`,
`TestDecisionCapture_AdvisoryModeCapturesAdvisorChampion`,
`TestDecisionCapture_AdvisorFailureCapturesFallbackAsChampionNotAdvisor`,
and `TestDecisionCapture_ShadowRecommendationNeverBecomesChampionPolicy`
prove all four cases. Captured at selection time, inside `selectAgent`
itself - never reconstructed later from current configuration.

## 7-8. Deterministic policy fingerprinting / identity conflict

```go
func NewRoutingPolicyDescriptor(id RoutingPolicyID, version, semanticsVersion string, config map[string]string) RoutingPolicyDescriptor
```

is the TRUSTED factory: it computes `Fingerprint` internally via
`computeCanonicalFingerprint` (SHA-256 over id, version,
semanticsVersion, and sorted `config` keys/values - map iteration order
never affects the result, no timestamp, no runtime pointer, no random
value participates) and tags the result `FingerprintScheme =
"canonical-v2"`. There is no parameter through which a caller can
inject an arbitrary pre-computed fingerprint via this factory
(`TestNegative_ArbitraryCallerFingerprintCannotOverrideComputed`
confirms exactly 4 parameters, none a fingerprint override).

Historical ADR 0008 records built via direct struct literals (manual,
empty-or-caller-supplied `Fingerprint`, implicitly `FingerprintScheme =
"manual-v1"` by the absence of the canonical tag) are NEVER rewritten -
a record's `FingerprintScheme` is itself a fact about how it was
produced, kept forever as written.

`ShadowEvaluator.checkPolicyIdentity` is the smallest-design conflict
enforcement point the mandate asks for (no separate policy registry):
the evaluator's own `fingerprint map[string]string` (keyed by
`"ID:Version"`) remembers the first fingerprint seen for each identity;
a LATER, DIFFERENT non-empty fingerprint under the same identity is a
`*PolicyIdentityConflict`, returned from `Evaluate` (for the Champion,
the entire batch fails closed; for one Challenger, only that
Challenger's record is skipped, others proceed) - this NEVER touches
Champion execution, which has either already happened or happens
entirely outside `ShadowEvaluator` regardless. Only enforced when BOTH
fingerprints are non-empty, so historical/manual/simple-test
descriptors (empty `Fingerprint`) never spuriously conflict.

`TestFingerprint_*` (6 tests) prove determinism, map-order independence,
semantic-change sensitivity, `SemanticsVersion` sensitivity, and no
timestamp parameter exists structurally.
`TestNegative_SameIDVersionDifferentFingerprintIsConflict` proves the
conflict detection end to end.

## 9-10. Automatic Challenger wiring / automatic outcome attachment

`ProductionEvaluationCoordinator` maintains two in-memory maps:

- `pendingDecisions map[string]pendingDecision` (keyed by
  `Tenant\x00TaskID`) - a TRANSIENT handoff from `ObserveRoutingDecision`
  to the immediately-following `ObserveExecution` call for the SAME
  task. Correctness relies on Scheduler's existing, unchanged dispatch
  discipline: a task is selected and then immediately dispatched in the
  SAME control-flow sequence, with no other decision for the same task
  interleaved - confirmed by tracing `Tick`'s dispatch loop (`selectAgent`
  -> `Applier.Apply` -> `Executor.Execute`, per task, in order).
- `evaluationsByExecutionID map[string][]string` - the STABLE
  correlation key (Core-owned `ExecutionID`, ADR 0006 Phase 7C), set once
  at execution time and used again at finalization time. This is what
  makes retry attribution correct: a genuine retry generates a NEW
  `ExecutionID` (unchanged ADR 0006 Phase 7C behavior), so a review
  resolving to the OLD `ExecutionID` can never find/alter the NEW
  attempt's entry - there is no shared mutable state between them.

`ObserveExecution` delegates to the embedded `SupervisorExecutionObserver`
UNCHANGED, then (if a pending decision exists for this task) calls
`AttachChampionOutcome` for every `EvaluationID` that decision produced,
with `QualityUnreviewed` (quality is not yet known at execution time).
`ObserveReviewTransition` delegates to the embedded
`ReviewFinalizationHook` UNCHANGED, then calls the NEW
`AttachChampionQuality` (which updates ONLY the quality dimension,
preserving the already-attached `ExecutionID`/`ActualExecutionOutcome`
untouched) for every `EvaluationID` resolved via
`ReviewFinalizationHook.ResolvedExecutionTargets` - the SAME exact-
resolution logic (ADR 0006 Phase 7C, exported for reuse rather than
duplicated) the embedded hook itself already uses to finalize
`PerformanceObservation`s, so the two can never diverge.

`TestProductionWiring_ChampionAndChallengerSeeIdenticalSnapshot` proves
Champion and Challenger are evaluated against the exact same persisted
snapshot. `TestProductionWiring_AutomaticOutcomeAttachment_ExecutionAndReview`
proves the full execution-then-review lifecycle attaches automatically,
with the execution outcome preserved across the later quality update.

## 11. Retry / out-of-order attribution

`TestProductionWiring_RetryBindsOwnExecutionNeverCross` drives a REAL
failed execution (D1 -> E1), a real `RetryTaskProposal`/re-assignment,
a real second execution (D2 -> E2 via direct `Executor.Execute`), and a
STALE review resolving to E1 - proving the stale review never alters
E2's (the retry's) evaluation record, since `AttachChampionQuality`
only ever updates the record `evaluationsByExecutionID[E1]` points to,
which is disjoint from E2's entry.

## 12-13. Evaluation failure isolation / observability

`ProductionWiringTelemetry` (`RecordCandidateSnapshotCaptureFailure`,
`RecordShadowEvaluationFailure`, `RecordEvaluationOutcomeAttachmentFailure`)
mirrors the ADR 0006 Phase 7A/7C pattern exactly. Every coordinator
method that could fail (snapshot save, `Evaluate`, `AttachChampionOutcome`/
`AttachChampionQuality`) records a counter and returns/continues without
ever touching Champion selection, `ExecutionResult`, or `ReviewState` -
none of those three authoritative outputs are mutated by anything in
this file. `RecordRoutingDecisionObserverPanic`/`RecordExecutionObserverPanic`/
`RecordReviewObserverPanic` delegate to the SAME panic-isolation
machinery `Executor`/`ReviewApplier`/`Scheduler` already established
(ADR 0006 Phase 7C) - no new recover() call sites were scattered through
domain logic; the ONE recovery boundary per hook (already present in
`Executor.Execute`/`ReviewApplier.Apply`/`Scheduler.captureDecision`)
is reused as-is. `TestDecisionCapture_ObserverPanicDoesNotAffectSelection`
proves a panicking `RoutingDecisionObserver` never affects `selectAgent`'s
return value.

## 14. Evaluation coverage accounting

```go
type EvaluationCoverage struct {
    EligibleDecisionCount, FullyCapturedDecisionCount,
    PartiallyCapturedDecisionCount, MissingDecisionCount int
    ExpectedChallengerEvaluations, CapturedChallengerEvaluations int
}
```

Decision-level coverage (how many decisions got SOME/ALL/NO challenger
records) is kept explicitly SEPARATE from record-level coverage ("1
decision x 3 challengers = 3 expected records") - the mandate's own
instruction not to conflate the two. `EvaluatePromotionGateWithCoverage`
checks `coverage.Complete()` BEFORE even considering
`EvaluatePromotionGate`'s result, returning the new
`PromotionCoverageIncomplete` result otherwise -
`TestCoverage_MissingEvaluationsBlockPromotion` proves the EXACT
mandated 100/95/5 scenario; `TestNegative_CoverageCannotBeGamedByHidingFailures`
proves a challenger causing 9 of 10 decisions to vanish cannot look
clean via its 1 perfectly-captured record, since the 9 missing
decisions remain visible in the coverage counters regardless.

## 15. Typed invalid recommendation reasons

```go
type InvalidRecommendationReason string
const (
    InvalidReasonUnknownCandidate, InvalidReasonNotInSnapshot,
    InvalidReasonTenantBoundary, InvalidReasonCapabilityMismatch,
    InvalidReasonAuthorizationViolation, InvalidReasonInvalidPolicyOutput
    InvalidRecommendationReason = ...
)
```

`RoutingEvaluationRecord.InvalidReason` is set ONLY when `Status ==
StatusChallengerInvalidRecommendation`, and only to a value Core can
actually PROVE: an empty `AgentID` -> `invalid_policy_output`; any
non-empty `AgentID` not in the snapshot -> `candidate_not_in_snapshot`
(this correctly subsumes the cross-tenant case too, since a
cross-tenant agent could never have been in the snapshot to begin with
- the snapshot itself retains no separate exclusion-reason provenance
to prove the MORE specific `tenant_boundary`/`capability_mismatch`
values, so those are never manufactured just for granularity, per the
mandate's explicit "do not manufacture exclusion reason just for test
coverage" instruction). `InvalidRecommendationCount > 0` remains the
UNCHANGED hard promotion blocker regardless of which `InvalidReason` is
recorded - the typed reason is for audit/security analysis/debugging
only, never a weakening of the gate.

## 16. Tenant/capability isolation, resource limits, idempotency

Unchanged from ADR 0006/0007/0008 at every layer this phase touches:
`eligibleCandidates`' tenant filter (ADR 0007), `CandidateSnapshot`'s
bounded size (`ErrSnapshotTooLarge`), `ShadowEvaluator`'s
`MaxChallengersPerDecision`/`boundReasons` (ADR 0008). `ComputeEvaluationID`
(ADR 0008, unchanged) remains the idempotency key; `TestProductionWiring_IdempotentRepeatedHooks`
proves a second `Tick` over an already-completed task produces no
duplicate record.

## Concurrency

`TestConcurrency_ProductionEvaluationCoordinator` drains 15 concurrent
decisions/executions through the REAL `Scheduler`/`Executor`/coordinator
chain (bounded by `MaxConcurrency`, requiring several `Tick` calls),
with concurrent `EvaluationStore.Query` calls racing against it, under
`-race`: exactly 15 distinct `EvaluationID`s, no duplicates, no lost
records, no deadlock.

## Security closure review

| Question | Finding |
|---|---|
| Can a caller inject an alternate CandidateSnapshot for production evaluation? | Not through the production wiring path - `ProductionEvaluationCoordinator.ObserveRoutingDecision` is the ONLY caller of `Save` in that path, fed exclusively from Core's own `eligibleCandidates` output. A caller with direct code-level access to the same `CandidateSnapshotStore` could still call `Save` itself - this is an in-process trust boundary, not a network/external attack surface, and is documented as a residual risk (same class as "a caller could construct a `PerformanceObservation` directly" in every prior phase). |
| Can a caller claim "performance-v1" while executing different routing config? | Detected if BOTH descriptors use the canonical fingerprint scheme (`*PolicyIdentityConflict`); NOT detected if either side uses a manual/empty fingerprint - documented residual risk, mitigated by preferring `NewRoutingPolicyDescriptor` for any real policy. |
| Can a Challenger recommendation reach production selection? | No - unchanged structural guarantee from ADR 0008, re-confirmed (`ShadowEvaluator`/`RoutingPolicy` still have no `Executor` reference anywhere). |
| Can failed/missing evaluations simply disappear? | No - `EvaluationCoverage`'s counters remain visible regardless of how clean a captured subset looks. |
| Can a Challenger be assigned the Champion's actual outcome, or vice versa? | No - `AttachChampionOutcome`/`AttachChampionQuality` write only Champion-outcome fields; no field exists for a Challenger outcome (ADR 0008, unchanged). |
| Can a filtered foreign Agent enter the snapshot/evaluation/shadow telemetry? | No - proven at the production level with a real mixed-tenant directory (`TestProductionWiring_ForeignTenantCandidateAbsentFromSnapshot`). |
| Can E1/E2 (retry) evaluation or reviews cross-bind? | No - proven end to end with real failed-then-retried execution (`TestProductionWiring_RetryBindsOwnExecutionNeverCross`). |
| Can huge Challenger sets/candidate sets/reasons bypass existing limits? | No - ADR 0008's limits are unchanged and still enforced; this phase added no new unbounded surface. |

No real defects were found during this phase's implementation.

## Tests / Verification

```text
gofmt -l .                  -> clean
go build ./...              -> clean
go vet ./...                -> clean
go test -race ./...         -> all packages ok, zero regressions
```

`internal/performance`: 169 tests passing under `-race` (+22 from Phase
9C). `internal/supervisor`: 139 tests passing under `-race` (+7 from
Phase 9C's decision-capture suite). Existing fuzz targets re-run clean
(`FuzzDecodePlanResponse`, `FuzzDecodeSynthesisResponse`,
`FuzzDetectDuplicateKeys` in `internal/supervisor`;
`FuzzDecodeArbitrationResponse`, `FuzzEvidenceValidate` in
`internal/review`) - no new fuzz target added, since Phase 9C introduces
no new untrusted-parsing/wire-decode boundary (every new type is
Core-generated or caller-supplied typed Go data).

## Residual risks

- **A privileged in-process caller with direct access to
  `CandidateSnapshotStore`/`EvaluationStore` could still bypass the
  automatic production wiring** - this is an in-process trust boundary
  (the same class of residual risk every prior phase's "a caller could
  construct X directly" already documents), not a network-reachable
  attack surface.
- **Policy identity conflict detection only works for descriptors using
  the canonical fingerprint scheme** - a manual/empty-fingerprint
  descriptor (e.g. simple test doubles, or a future caller that doesn't
  use `NewRoutingPolicyDescriptor`) never trips the conflict check.
- **`CandidateSnapshotStore` remains keyed by `(Tenant, TaskID)` only**
  (unchanged from ADR 0008) - a genuine retry whose candidate set
  actually DIFFERS from the first decision's hits `SnapshotIdentityConflict`
  and that retry's evaluation capture is skipped (observable via
  `RecordCandidateSnapshotCaptureFailure`, never silent) rather than
  being separately persisted. The common case (same/idempotent candidate
  set across retries) is handled correctly and automatically; the
  divergent-candidate-set retry case is a documented gap, not silently
  mishandled.
- **No Model-side experimentation** - unchanged scope boundary from ADR
  0008, re-confirmed.

## Deviations from ADR

- `PolicyProvenance` (the Go sketch suggested) was implemented with
  `PolicyID`/`PolicyVersion` as plain strings rather than reusing
  `RoutingPolicyDescriptor` directly in `internal/supervisor` - this
  package has zero dependency on `internal/performance` (the one-way
  dependency direction established since ADR 0002 Phase 2A), so a
  plain-string DTO is the correct boundary type; `internal/performance`'s
  `ProductionEvaluationCoordinator` converts it into a real
  `RoutingPolicyDescriptor` on its own side.
- `DeterministicAdvisor.PolicyVersion` (the field) was renamed to
  `ConfiguredPolicyVersion` to free the `PolicyVersion()` METHOD name
  required by the new `supervisor.PolicyIdentifiable` interface - no
  literal struct-field usages existed anywhere in the repository, so
  this was a safe, zero-impact rename.
- Did not extend `CandidateSnapshotStore`'s key schema to include
  `DecisionID` (which would allow multiple distinct snapshots per
  TaskID) - judged out of scope for this closure phase; documented
  above as a residual risk instead, per "use the smallest correct
  design."

## Final invariants (proven by the test suite)

```text
Every evaluation-eligible production decision automatically captures an authoritative evaluation context. -> TestProductionWiring_AutomaticSnapshotNoManualCall
CandidateSnapshot is Core-owned.                                          -> ObserveRoutingDecision is the only Save call site in the production path
CandidateSnapshot represents the exact eligible production set.          -> built only from decision.CandidateAgentIDs, never re-derived
Callers cannot replace the production CandidateSnapshot.                 -> no other code path calls Save in the automatic wiring
Champion and Challengers see the same decision-time candidate universe.  -> TestProductionWiring_ChampionAndChallengerSeeIdenticalSnapshot
Champion policy identity comes from the policy that actually produced the production decision. -> TestDecisionCapture_AdvisoryModeCapturesAdvisorChampion / ...AdvisorFailureCapturesFallbackAsChampionNotAdvisor
Policy ID/version cannot silently represent changed routing semantics.   -> NewRoutingPolicyDescriptor + checkPolicyIdentity (for the canonical scheme)
Policy fingerprinting is deterministic.                                  -> TestFingerprint_* (6 tests)
Challenger recommendations still cannot reach Executor.                  -> unchanged ADR 0008 structural guarantee, re-confirmed
Evaluation failure cannot change the Champion decision.                  -> TestDecisionCapture_ObserverPanicDoesNotAffectSelection
Evaluation failure cannot change execution truth.                        -> ProductionWiringTelemetry counters only; no mutation of ExecutionResult/ReviewState anywhere
Missing evaluation records remain visible as incomplete coverage.        -> TestCoverage_MissingEvaluationsBlockPromotion
Missing evaluations cannot make a Challenger appear cleaner.             -> TestNegative_CoverageCannotBeGamedByHidingFailures
Actual outcomes attach only to the executed Champion decision through exact Core-owned provenance. -> TestProductionWiring_AutomaticOutcomeAttachment_ExecutionAndReview
Retries and stale reviews cannot cross-bind EvaluationRecords.           -> TestProductionWiring_RetryBindsOwnExecutionNeverCross
Cross-tenant agents cannot enter the production CandidateSnapshot.       -> TestProductionWiring_ForeignTenantCandidateAbsentFromSnapshot
Invalid recommendations remain hard Promotion blockers.                  -> EvaluatePromotionGate unchanged; InvalidReason never weakens it
Promotion Gates remain advisory only.                                    -> no Promote/ActivateChallenger/SetChampion method exists anywhere (ADR 0008, re-confirmed unchanged)
HarnessMesh still selects agents.                                        -> Scheduler's own selection logic is unmodified; only an observer hook was added
Switchyard still selects models.                                         -> unchanged from ADR 0001-0008
No exploration or Challenger execution exists yet.                       -> TestNegative_NoPhase10IdentifiersIntroduced (repository-wide structural guard)
LLM output remains proposal, never authority.                            -> unchanged from ADR 0002-0008; Phase 9C adds no new LLM surface
```
