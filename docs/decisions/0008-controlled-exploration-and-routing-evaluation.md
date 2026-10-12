# ADR 0008: Controlled Exploration and Routing Evaluation (ADR 0002 Phase 9)

## Status

Implemented and verified. Depends on [ADR 0001](0001-agent-control-plane-switchyard-model-routing.md)-[0007](0007-joint-agent-model-decision-intelligence.md).

## Purpose

Build a Controlled Routing Evaluation Plane: Champion/Challenger
comparison, offline replay, and manual-promotion gates, so routing
policies can be evaluated safely against each other **without** ever
gaining production authority.

**Evaluation != Authority.**

```text
Shadow recommendations never execute.
Offline replay simulates routing decisions, not execution outcomes.
Promotion gates provide evidence for manual policy promotion but
cannot modify the active routing policy.
Time-faithful replay must not consume information that was
unavailable at the historical decision time.
Historical routing evaluation remains observational evidence,
not causal proof.
```

## Non-goals (explicit)

No automatic policy promotion. No automatic alteration of active
routing. No Challenger decision is ever executed. No autonomous
exploration, no contextual bandits, no reinforcement learning, no
routing-model training. No `JointRouter`/unified Agent+Model router
(ADR 0007's guard, unchanged and re-confirmed). No Phase 10
functionality (e.g. no actual promotion workflow, no automated
rollback, no live A/B traffic splitting).

## Repository analysis (performed before writing code)

- `supervisor.AgentDecisionAdvisor.Advise(ctx, task, candidates) (AgentAdvice, error)`
  (ADR 0006 Phase 7B) is already shaped exactly like a policy's
  recommendation function. Per the mandate's own instruction to adapt
  it rather than invent a parallel abstraction, `AdvisorRoutingPolicy`
  wraps it directly - `RoutingPolicy` is a thin interface, not a new
  framework layer.
- `supervisor.AdviceBasis`/`DefaultAdvisorPolicyVersion` (ADR 0006)
  already carry a policy-version string, but no DEDICATED, explicit
  policy-identity type existed - `RoutingPolicyDescriptor{ID, Version,
  Fingerprint}` is new.
- `eligibleCandidates` (`internal/supervisor/scheduler.go`, ADR 0006/7C)
  computes the tenant+capability+availability-filtered candidate set
  EVERY Tick, but **nothing persists it** - there was no way to
  reconstruct "what was eligible at decision time T" before this phase.
  This confirmed the mandate's own suspicion ("Prüfe, ob historische
  Candidate Sets heute rekonstruierbar sind" - answer: no) and is why
  `CandidateSnapshot`/`CandidateSnapshotStore` had to be built new.
- `PerformanceObservation.CreatedAt`/`UpdatedAt` (ADR 0005/0006) exist
  and are set correctly (`CreatedAt` at execution-observation time,
  `UpdatedAt` bumped by every `FinalizeWithReview` call, including
  non-terminal transitions) - but `ObservationFilter` had **no
  time-cutoff filtering at all**. Rather than extend `ObservationFilter`/
  `PerformanceStore` (a broader, more invasive change touching the
  persisted store interface), this phase adds `TimeFaithfulDataset`, a
  `DecisionDataset` DECORATOR that filters/rolls back observations
  client-side - every existing aggregation function (`Aggregate`,
  `AggregatePair`, `AggregateModelUsage`, `DeterministicAdvisor.Advise`)
  works against it UNCHANGED, since they only depend on the
  `DecisionDataset` interface, never the concrete store.
- No existing "evaluation record" or "replay" concept exists anywhere
  in the repository - `RoutingEvaluationRecord`, `ShadowEvaluator`,
  `Replayer` are entirely new, built in `internal/performance`
  (reusing `supervisor`/`review` TYPES, importing neither
  `supervisor.Executor` nor `supervisor.ExecutionProvider` anywhere -
  see "Structural safety" below).
- ADR 0006 Phase 7B's EXISTING shadow mechanism
  (`Scheduler.AgentDecisionMode`/`EventAdvisorShadowRecommendation`)
  was reviewed per the mandate's explicit "don't build two competing
  shadow systems" instruction. **Decision**: Phase 9's `ShadowEvaluator`
  is a separate, explicit, side-by-side evaluation call - it is NOT
  auto-wired into `Scheduler.Tick`, and `internal/supervisor` was not
  modified at all in this phase. This is a deliberate scope boundary,
  not an oversight: auto-wiring would conflate "live routing
  telemetry" (7B's job) with "controlled, replayable evaluation" (9's
  job), which is exactly the kind of system-doubling the mandate warned
  against avoiding by MERGING, not by building a second live path.
  Phase 7B's shadow events remain the production routing telemetry;
  Phase 9's `ShadowEvaluator`/`RoutingEvaluationRecord` is the separate,
  explicitly-invoked evaluation plane that REUSES the same
  `AgentDecisionAdvisor`/`AgentCandidate`/`AgentAdvice` vocabulary. See
  "Residual risks" for the concrete, documented gap this leaves (no
  automatic capture of production decisions into the evaluation plane
  yet).

### Gap matrix

| Concern | Existing | Target | Gap | Action |
|---|---|---|---|---|
| Policy identity | `AdviceBasis` (version string only, ADR 0006) | immutable `{ID, Version}` | partial | `RoutingPolicyDescriptor` |
| Champion policy | Scheduler's live deterministic/advisor path (ADR 0006/7C) | reusable as evaluation input | full | `StaticRoutingPolicy`/direct `championAgentID` parameter - Champion's REAL decision is taken as ground truth, never re-derived |
| Challenger policy | `AgentDecisionAdvisor` (ADR 0006) | recommendation-only, no execution authority | full | `AdvisorRoutingPolicy` + `RoutingPolicy` interface |
| Candidate snapshot | none (ephemeral per-Tick only) | persisted, immutable, bounded | full | `CandidateSnapshot`/`CandidateSnapshotStore` |
| Shadow evaluation | 7B's live shadow mode (different purpose) | side-by-side, replayable, persisted comparison | full | `ShadowEvaluator`/`RoutingEvaluationRecord` |
| Offline replay | none | pure, Executor-free simulation | full | `Replayer`/`ReplayResult` |
| Historical cutoff | none (`ObservationFilter` has no time dimension) | time-faithful vs retrospective | full | `TimeFaithfulDataset`/`ObservationAsOf` |
| Promotion gates | none | evidence-only eligibility signal, no mutation | full | `EvaluatePromotionGate`/`PromotionGateResult` |
| Tenant isolation | `ObservationFilter`/`DeterministicAdvisor.Tenant` (ADR 0005-0007) | extended to evaluation/replay | full | `EvaluationFilter` (same fail-closed pattern), snapshot-sourced tenant boundary |
| Evaluation persistence | none | decision-facts-immutable / outcome-mutable split | full | `MemoryEvaluationStore.Save`'s `decisionFactsEqual` gate |

## Part 1-2: Policy identity, Champion, Challenger

```go
type RoutingPolicyDescriptor struct { ID RoutingPolicyID; Version string; Fingerprint string }
type RoutingPolicy interface {
    Descriptor() RoutingPolicyDescriptor
    Recommend(ctx context.Context, input RoutingEvaluationInput) (RoutingRecommendation, error)
}
```

`AdvisorRoutingPolicy` adapts any `supervisor.AgentDecisionAdvisor`.
There is no separate `ChampionPolicy`/`ChallengerPolicy` Go type - the
roles are assigned entirely by the CALLER
(`ShadowEvaluator.Evaluate`'s parameter positions). Champion's
`championAgentID` is passed in as the agent Core ALREADY, really
selected - `Evaluate` never re-derives or second-guesses it, so the
record's `ChampionAgentID` is always ground truth, never a
recomputation that could drift from what actually executed.

### Structural proof: Challenger cannot reach Executor

`TestNegative_ShadowEvaluatorHasNoExecutorReference` walks
`ShadowEvaluator`'s fields and every method's signature via reflection
and fails if any type name contains "Executor"/"ExecutionProvider" -
there is none. The SAME is true of `Replayer`
(`TestNegative_ReplayerHasNoExecutorReference`). This is not a
convention kept by code review; it is a fact about the type graph that
a test re-verifies every run.

## Part 3: Immutable Candidate Snapshot

```go
type CandidateSnapshot struct { Tenant string; TaskID supervisor.TaskID; TaskClass TaskClass; CapturedAt time.Time; AgentIDs []string }
```

Built via `NewCandidateSnapshot(tenant, taskID, taskClass, candidates []supervisor.AgentCandidate, capturedAt, limits)`
from an ALREADY eligibility-filtered candidate list (e.g. whatever
`eligibleCandidates` already produced) - it never re-derives or widens
eligibility itself, and it is bounded
(`DefaultMaxCandidatesPerSnapshot`, `ErrSnapshotTooLarge`). `Contains`
is the one check that enforces "recommended AgentID ∈ snapshot" -
anything else is `StatusChallengerInvalidRecommendation`, never
executed, never trusted.

`CandidateSnapshotStore` persists by `(Tenant, TaskID)`. Since the
snapshot's SOURCE is always an already-tenant-filtered candidate list,
a cross-tenant agent can never be IN a snapshot to begin with - this is
what makes "Challenger recommends a cross-tenant agent" collapse
structurally into "Challenger recommends an agent outside the
snapshot," the SAME code path, proven by
`TestNegative_ChallengerReturnsCrossTenantAgent`.

## Part 4-5: Shadow Evaluation and Outcome Attachment

```go
func (e *ShadowEvaluator) Evaluate(ctx, snapshot, championPolicy, championAgentID, decisionTime, challengers []RoutingPolicy) ([]RoutingEvaluationRecord, error)
func (e *ShadowEvaluator) AttachChampionOutcome(ctx, evaluationID, executionID string, outcome ExecutionOutcome, quality QualityOutcome) error
```

`EvaluationStatus` is exactly the mandated vocabulary
(`same_recommendation`/`different_recommendation`/
`challenger_insufficient_data`/`challenger_error`/
`challenger_invalid_recommendation`/`evaluation_incomplete`) -
`TestNegative_NoStatusImpliesChallengerWonOrLost` confirms none of them
resemble "won"/"lost".

`AttachChampionOutcome` correlates via the Champion's REAL, Core-owned
`ExecutionID` (ADR 0006 Phase 7C), supplied by the caller who already
has it from the real `Executor.Execute` return value - never a
TaskID-latest or timestamp guess. It writes ONLY the Champion-outcome
fields; `RoutingEvaluationRecord` has no field resembling a
"ChallengerOutcome" at all (`TestNegative_RoutingEvaluationRecordCannotFakeChallengerOutcome`),
so there is no position through which an outcome could even be
misattached to the Challenger, structurally, not by discipline.
`TestNegative_OutcomeOnlyAttachesToChampion` and
`TestNegative_AttachOutcomeToUnknownEvaluationFailsClosed` prove the
behavioral side.

Challenger failures are isolated the same way every other optional hook
in this package is: `recommendSafely` wraps `Recommend` in `recover()`,
so a panicking Challenger is recorded as `StatusChallengerError`, never
crashes `Evaluate` (`TestNegative_ChallengerPanicRecordedAsError`).

## Part 6-8: Offline Replay, Time-Faithful vs. Retrospective, Historical Candidate Reconstruction

`Replayer.Replay`/`ReplayBatch` never substitute today's live registry
for a missing historical snapshot - `ReplayIncomplete` is correct in
that case (`TestNegative_NoHistoricalCandidateSnapshotIsIncomplete`).
`ReplayResult` has no field resembling a predicted/hypothetical outcome
(`TestNegative_ReplayResultCannotFakeOutcome`) - replay answers "what
would Policy P have recommended," never "what would have happened."

**Time-faithful semantics, made executable, not just documented:**
`ObservationAsOf(obs, asOf)` defines exactly what counts as "available
at DecisionTime":

- An observation `CreatedAt` AFTER `asOf` did not exist yet - excluded
  entirely.
- An observation that existed (`CreatedAt <= asOf`) but was finalized
  (`UpdatedAt`) AFTER `asOf` is returned with its quality dimension
  rolled back to `QualityUnreviewed`/`ObservationPending` - at
  `DecisionTime`, only the pending execution was known, not whatever
  verdict review later reached. This is a conservative
  under-approximation (it can only ever claim to know LESS than full
  history shows): there is no event-sourced history of every
  intermediate quality state to reconstruct exactly, and erring toward
  "unknown" is the safe direction, never the other way.

`TimeFaithfulDataset` wraps any `DecisionDataset` with this rule,
letting EVERY existing aggregation function work unchanged in
time-faithful mode - no aggregation logic was duplicated.
`ReplayModeTimeFaithful`/`ReplayModeRetrospective` are explicit,
mutually exclusive, and never inferred; there is no default mode.
`TestE2E_TimeFaithfulReplay_CannotUseFutureData` proves the exact
mandated scenario: a decision at T1 with an Agent B observation only
becoming knowable at T2 > T1 is invisible to time-faithful replay but
visible to retrospective replay.

## Part 9: Comparable Historical Evidence (no fake challenger outcome)

Reuses ADR 0007's `Aggregate`/`AggregatePair` directly for cohort
support numbers (`HistoricalSupportCount` on `ChallengerSummary`) -
strictly observational. `ReplayResult`/`RoutingEvaluationRecord` both
have no `PredictedOutcome`/`ExpectedSuccess`/`WouldHaveVerified`-style
field (`TestNegative_ReplayResultCannotFakeOutcome`,
`TestNegative_RoutingEvaluationRecordCannotFakeChallengerOutcome`) -
the mandate's explicit "if such fields already exist, critically
review them" found none, since this is all new code.

## Part 10-11: Evaluation Cohorts, Bias Controls

`EvaluationFilter` is Tenant+TaskClass scoped only (optionally
`ChallengerPolicyID`), mirroring `ObservationFilter` exactly - no
cross-tenant, no cross-task-class cohort. Bias documented and tested:

- **Selection bias**: only the Champion's selected agent has a real,
  observed outcome - the Challenger's recommendation never does, by
  construction (no execution occurs).
- **Survivorship bias**: `AttachChampionOutcome` honestly records
  failed/timed-out/cancelled outcomes, never omits them
  (`TestNegative_FailedTimedOutCancelledOutcomesAttachHonestly`).
- **Look-ahead bias**: `TimeFaithfulDataset`/`ObservationAsOf`, above.
- **Policy-induced feedback loop**: documented, not corrected (ADR
  0006 Phase 7B already made this same call for its own advisor
  influence tracking; Phase 9 adds no automatic correction either -
  this remains a human-in-the-loop decision by design).

## Part 12-14: ChallengerSummary, Promotion Gates, Manual Promotion Boundary

```go
type ChallengerSummary struct {
    Policy RoutingPolicyDescriptor
    EvaluationCount, SameRecommendationCount, DifferentRecommendationCount int
    InvalidRecommendationCount, ErrorCount, InsufficientDataCount int
    HistoricalSupportCount int
}
```

No "accuracy" field anywhere - there is no ground truth for a
Challenger's hypothetical choice to be accurate against.

`EvaluatePromotionGate(summary, finalizedOutcomeSupportCount, cfg) PromotionGateResult`
checks hard blockers FIRST: `InvalidRecommendationCount > 0` ->
`policy_violation`-tier `invalid_recommendations` (this single counter
structurally subsumes any cross-tenant recommendation attempt AND any
policy/authorization-bypass attempt, since a Challenger has no other
observable channel to express either - see "Hard security gates"
below) - then minimum-evidence gates
(`MinimumEvaluations`/`MinimumFinalizedOutcomes`, both defaulting to
the EXISTING `DefaultMinimumObservationCount`, ADR 0005, reused rather
than inventing new magic numbers). `CompareHistoricalCohortReliability`
is a SEPARATE, explicitly bounded signal (not folded into the hard-gate
logic) comparing the Champion's real historical reliability against
the historical reliability of whichever agent the Challenger most
favored - conservative, threshold-gated (>2x worse, both cohorts
sufficient), and never claims the Challenger "would" underperform;
real outcome superiority remains unproven, exactly as the mandate
requires.

**No `Promote`/`ActivateChallenger`/`SetChampion` method exists
anywhere in `ShadowEvaluator` or `Replayer`**
(`TestNegative_PromotionGateHasNoMutationMethod`, reflection-based).
`TestNegative_PromotionGateDoesNotMutateActivePolicy` behaviorally
confirms a gate returning eligible has no observable effect on
anything - there is no "active champion" state in this package to
mutate.

## Part 15: Idempotency

`ComputeEvaluationID(snapshot, championPolicy, challengerPolicy)`
derives from the snapshot's OWN fingerprint (which encodes Tenant,
TaskID, CapturedAt, and the full AgentIDs list) plus both policy
descriptors' `String()` forms - never a timestamp alone. Replaying the
EXACT same historical shadow event (same snapshot, same policies)
always produces the same ID and is therefore a safe no-op
(`TestNegative_DuplicateEvaluationEventIdempotent`); a genuinely new
decision (even for the same TaskID, e.g. a reassignment) captures a NEW
snapshot with a new `CapturedAt`, producing a distinct ID.

**Policy version immutability**: `RoutingPolicyDescriptor.Fingerprint`
is an OPTIONAL, caller-supplied field for detecting a same-ID-same-
Version-different-semantics collision. This package computes no
fingerprint automatically - there is no existing deterministic
config-hashing utility anywhere in this repository for policy configs
specifically to reuse, and building one generically was judged out of
scope for this phase (documented deviation below). What IS guaranteed:
different `Version` strings always produce different `EvaluationID`s
(`TestNegative_PolicyVersionCollisionDetectedViaDistinctIDs`,
`TestNegative_MissingPolicyVersionDistinctFromSet`) - a silent
behavior change under the SAME ID+Version string is only detectable if
the caller supplies a `Fingerprint` and compares it; this package does
not enforce that comparison itself.

## Part 16: Evaluation Persistence

`MemoryEvaluationStore.Save` splits "decision facts" (Tenant, TaskID,
TaskClass, both policy descriptors, CandidateAgentIDs,
ChampionAgentID, ChallengerAgentID, Status) from "outcome projection"
(ExecutionID, ActualExecutionOutcome, ActualQualityOutcome,
OutcomeAttached, OutcomeUpdatedAt) via `decisionFactsEqual`: a Save
whose decision facts match an existing record is accepted (the only
fields allowed to change are the outcome-projection ones, exactly what
`AttachChampionOutcome` does); a Save that would CHANGE decision facts
under an existing `EvaluationID` is rejected
(`*EvaluationIdentityConflict`) - "what did the Challenger recommend at
the time" remains auditable forever, never silently rewritten.

## Part 17-18: Security Boundaries, Shadow Mode Integration

Covered above (Part 1-2's structural proof, and "Repository analysis"'s
explicit "existing shadow mechanism reviewed, not duplicated"
decision). Model-side scope: Phase 9 is Agent-routing evaluation only -
`RoutingRecommendation`/`RoutingPolicy` have no model/provider field
(mirrors ADR 0007's own guard), and no Model Challenger evaluation was
built - **deferred, documented as a residual risk**, per the mandate's
explicit permission to defer it.

## Part 19-20: Explanation Bounds, Security Threat Model

`boundReasons` truncates to `MaxReasonCount`/`MaxReasonLength`
(`TestNegative_HugeReasonsBounded`); `NewCandidateSnapshot` rejects
over-sized candidate lists (`ErrSnapshotTooLarge`,
`TestNegative_HugeCandidateListRejected`); `Evaluate` rejects
over-sized challenger counts (`TooManyChallengers`,
`TestNegative_TooManyChallengers`); `ReplayBatch` rejects over-sized
batches (`ReplayBatchTooLarge`, `TestNegative_ReplayBatchTooLarge`).

## Security closure review

| Question | Finding |
|---|---|
| Can Challenger reach Executor? | No - structurally (reflection-proven); no code path exists. |
| Can Replay invoke a real agent/provider? | No - structurally (reflection-proven) and behaviorally confirmed. |
| Can a Challenger inject a foreign AgentID? | No - `snapshot.Contains` rejects anything not in the (already tenant/capability-filtered) snapshot; always `challenger_invalid_recommendation`. |
| Can actual outcome be attached to a non-executed recommendation? | No - `AttachChampionOutcome` writes only Champion-outcome fields; there is no field to attach a Challenger outcome to at all. |
| Can future observations enter a time-faithful replay? | No - `ObservationAsOf`/`TimeFaithfulDataset`, proven with the exact mandated T1/T2 scenario. |
| Can failed/time-out/cancelled runs disappear? | No - honestly recorded, proven directly. |
| Can cross-tenant data influence evaluation? | No - `EvaluationFilter`'s fail-closed tenant validation; `DeterministicAdvisor.Tenant`'s existing (ADR 0007) structural scoping, unchanged. |
| Can a PromotionGate modify Champion state? | No - no mutation method exists anywhere in the evaluation layer. |
| Can PolicyVersion be silently reused for new behavior? | Partially mitigated - distinct Version strings always produce distinct EvaluationIDs; TRUE same-string-different-semantics collision detection requires a caller-supplied Fingerprint, not enforced automatically (documented deviation). |

No real defects were found during this phase's implementation - every
check above was satisfied by the design as built, verified by a
dedicated test.

## Tests / Verification

```text
gofmt -l .                  -> clean
go build ./...              -> clean
go vet ./...                -> clean
go test -race ./...         -> all packages ok, zero regressions
```

`internal/performance`: 147 tests passing under `-race` (+44 from
Phase 9: structural guards, shadow evaluation E2E and negatives, offline
replay E2E and negatives, time-faithful vs. retrospective, promotion
gates, concurrency). `internal/supervisor`/`internal/review`: unchanged
(132/81 respectively - Phase 9 touched neither package). Existing fuzz
targets re-run clean (`FuzzDecodePlanResponse`,
`FuzzDecodeSynthesisResponse`, `FuzzDetectDuplicateKeys` in
`internal/supervisor`; `FuzzDecodeArbitrationResponse`,
`FuzzEvidenceValidate` in `internal/review`) - no new fuzz target added,
since Phase 9 introduces no new untrusted-parsing/wire-decode boundary
(every new type is Core-constructed or caller-supplied typed Go data,
never decoded from untrusted bytes).

## Residual risks

- **No automatic capture of production decisions into the evaluation
  plane.** `internal/supervisor` was not modified - a real integration
  (Scheduler capturing a `CandidateSnapshot` and calling
  `ShadowEvaluator.Evaluate` automatically on every Tick) remains
  future work, deliberately out of this phase's scope (see "Repository
  analysis"'s shadow-mode-integration decision). Every test in this
  phase exercises realistic simulated call sequences, exactly like
  every prior phase's own residual-risk pattern.
- **`RoutingPolicyDescriptor.Fingerprint` is caller-computed, not
  automatically derived** - no generic deterministic policy-config
  hashing utility exists in this repository to build one from
  generically; a real same-ID-same-Version semantics collision is only
  caught if a caller supplies and compares fingerprints itself.
- **Model-side Challenger evaluation is deferred entirely** - Phase 9
  is Agent-routing evaluation only, per the mandate's explicit
  permission to defer this.
- **`CandidateSnapshot` capture is manual** - nothing automatically
  snapshots eligible candidates at decision time; a caller (e.g. a
  future Scheduler integration) must call `NewCandidateSnapshot`
  itself and persist it before a later replay can reconstruct that
  decision. Decisions made before this capture point exists have no
  snapshot and correctly replay as `ReplayIncomplete`.

## Deviations from ADR

- Did not extend `ObservationFilter`/`PerformanceStore` with a
  time-cutoff parameter - built `TimeFaithfulDataset` as a
  `DecisionDataset` decorator instead, a smaller, less invasive change
  that reuses every existing aggregation function unchanged.
- Folded "cross-tenant recommendation" and "policy/authorization
  bypass attempt" into the single `InvalidRecommendationCount` hard
  blocker rather than tracking them as separate `ChallengerSummary`
  fields, since a Challenger has no other observable channel through
  which either could manifest (both collapse to "recommended an agent
  outside the snapshot," the same code path) - documented explicitly
  rather than adding counters that would always read zero through a
  different path and nonzero through this one.
- Did not build a `JointDecisionDataset`-style wrapper interface for
  the evaluation plane (mirroring ADR 0007's own "only if sensible"
  deviation) - `ShadowEvaluator`/`Replayer`/`EvaluatePromotionGate` are
  plain functions/types consistent with this package's established
  style.

## Final invariants (proven by the test suite)

```text
Champion remains the only production Agent routing policy.          -> internal/supervisor unmodified; Scheduler's own selection logic untouched by this phase
Challenger recommendations cannot reach Executor.                    -> TestNegative_ShadowEvaluatorHasNoExecutorReference (structural)
Offline Replay cannot invoke ExecutionProvider.                      -> TestNegative_ReplayerHasNoExecutorReference (structural) + TestNegative_ReplayCannotInvokeExecutionProvider
Actual outcomes belong only to executed decisions.                   -> AttachChampionOutcome writes only Champion fields; TestNegative_OutcomeOnlyAttachesToChampion
Challenger outcomes are never fabricated.                            -> TestNegative_RoutingEvaluationRecordCannotFakeChallengerOutcome / TestNegative_ReplayResultCannotFakeOutcome
Historical CandidateSnapshots are not reconstructed from today's registry. -> TestCandidateSnapshot_HistoricalNotReconstructedFromTodaysRegistry / TestNegative_NoHistoricalCandidateSnapshotIsIncomplete
Time-faithful Replay cannot use future information.                  -> TestE2E_TimeFaithfulReplay_CannotUseFutureData / ObservationAsOf
Cross-tenant data cannot influence evaluation.                        -> EvaluationFilter fail-closed tenant validation; TestNegative_CrossTenantReplayQueryDenied
Failed, timed-out and cancelled executions remain visible.           -> TestNegative_FailedTimedOutCancelledOutcomesAttachHonestly
Promotion Gates cannot mutate active policy.                         -> TestNegative_PromotionGateHasNoMutationMethod / TestNegative_PromotionGateDoesNotMutateActivePolicy
Policy versions are auditable and immutable in meaning.               -> ComputeEvaluationID keyed on policy descriptor String(); distinct versions never collide
Shadow disagreement is evidence, not authority.                       -> StatusDifferentRecommendation never alters ChampionAgentID or triggers any execution
Historical evaluation is observational, not causal proof.            -> documented verbatim in this ADR; CompareHistoricalCohortReliability never claims causal superiority
HarnessMesh still selects agents.                                     -> unchanged from ADR 0001-0007
Switchyard still selects models.                                      -> unchanged from ADR 0001-0007
No unified Agent+Model experiment authority exists.                  -> RoutingPolicy/RoutingRecommendation have no model/provider field; ADR 0007's JointRouter guard re-confirmed unmodified
LLM output remains proposal, never authority.                        -> unchanged from ADR 0002-0007; Phase 9 adds no new LLM surface
```
