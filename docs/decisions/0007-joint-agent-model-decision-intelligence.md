# ADR 0007: Joint Agent-Model Decision Intelligence (ADR 0002 Phase 8)

## Status

Implemented and verified. Depends on [ADR 0001](0001-agent-control-plane-switchyard-model-routing.md)-[0006](0006-production-decision-wiring-and-explainable-agent-advice.md).

## Purpose

Make Agent, Model, and Outcome data jointly analyzable - "how did Agent
A perform for security_review," "how did Agent A + Model X compare with
Agent B + Model X," "which Agent+Model combinations had verified
outcomes" - **without** creating a joint routing authority. This phase
closes the mandatory ADR 0006 Phase 7C residual point first (production
findings did not yet reliably carry Core-owned execution provenance),
then builds read-only pair aggregation over the existing
`PerformanceObservation` model, a bounded model-outcome feedback
boundary, and an optional pair-evidence enhancement to the Phase 7B
advisor.

**HarnessMesh may observe model outcomes, but Switchyard remains the
model-selection authority.**
**Switchyard may receive outcome feedback, but this feedback is
evidence, not a routing command.**
**Joint historical performance is observational evidence, not causal
proof.**

## Non-goals (explicit)

No `JointRouter.SelectAgentAndModel(...)` (structurally guarded - see
"No Joint Router" below). No ML, no bandits, no contextual bandits, no
Thompson sampling, no UCB, no regression, no learned embeddings, no
neural routing, no automated weight learning. No new analytics database
- all aggregation is query-time over the existing `PerformanceObservation`
store. No invented Switchyard HTTP feedback API. No autonomous joint
routing of any kind - Phase 8 only observes and explains.

## Repository analysis (performed before writing code)

- `supervisor.ModelRoutingDecision{Provider, Model, RouteID, Reason}`
  carries no persisted ID; `economy.RoutingDecision.ID` is a DIFFERENT,
  unrelated concept (agent/participant selection, not model routing).
  Neither offers a "RoutingDecisionID" to correlate joint feedback by -
  `ModelUsageRef.RouteID` already serves that correlation role, so no
  new ID type was introduced (ADR 0007's own Go sketches used
  `RoutingDecisionID string` and `Tenant TenantID`; both were dropped as
  deviations - documented under "Deviations" below - since neither a
  routing-decision ID nor a `TenantID` type exists anywhere in this
  codebase, which uses a plain `string` for tenant everywhere).
- `supervisor.ExecutionResult.ModelUsages []ModelUsageRef` (ADR 0006)
  and `review.FindingObservation.SourceExecutionRef` (ADR 0006 Phase 7C)
  both existed as Core-owned PLUMBING fields, but **no production code
  anywhere populates either from a real agent/model runtime** - the only
  implementation of `supervisor.ExecutionProvider` in the entire
  repository is `FakeExecutionProvider`. This is a pre-existing gap
  (predating Phase 7/8) in how far "production wiring" actually reaches,
  not something this phase could or should close - building a real
  `ExecutionProvider` (an actual agent-runtime subprocess integration)
  is out of scope for a Decision Intelligence phase.
- `internal/collaboration/engine.go` has its OWN, separate Finding
  pipeline: `protocol.FindingPayload` findings are parsed directly from
  raw agent/LLM JSON output (`json.Unmarshal(..., &parsed)` against
  model response content). This is a DIFFERENT finding model than
  `review.FindingObservation` (the ADR 0003-0006 evidence-driven review
  system this ADR's provenance work operates on) - the two are linked
  only by ID convention (`FindingObservation.FindingID` correlates to
  `protocol.FindingPayload.ID`), and `review.ReviewState.Ingest` has
  ZERO production callers connecting them. Wiring collaboration's
  agent-text-parsing pipeline into `review.Ingest` would be new,
  large-scope integration work (effectively: teaching the collaboration
  engine to also construct authoritative `FindingObservation`s), not a
  closure fix - correctly out of scope here (see "Residual risks").
- `internal/modelrouting.SwitchyardInferenceRouter.Route` resolves a
  STAGE ("plan"/"replan"/"synthesize") to a route alias string
  (`ModelRoutingDecision.Model`/`RouteID`) and sends that alias as the
  wire-level `"model"` field to Switchyard's OpenAI-compatible endpoint;
  Switchyard's own, external configuration resolves the alias to a
  concrete provider/model, invisibly to this codebase. `Complete`'s
  response echoes back `ServedModel`/hardcoded `ServedProvider:
  "switchyard"` - this is the only trusted, transport-echoed model
  identity signal that exists today (see "Route alias semantics" below
  for the safety analysis).
- `AggregateModelUsage` (ADR 0005) already answers "which model/route
  combinations were observed for Agent A" - reused directly, not
  rebuilt.
- `ComputeObservationID`/`ObservationNotFound`/the ADR 0006 Phase 7C
  exact-correlation discipline in `ReviewFinalizationHook` is reused
  unchanged as the foundation the new Finding-provenance constructor
  feeds into.

### Gap matrix

| Concern | Existing | Target | Gap | Action |
|---|---|---|---|---|
| Finding execution provenance | `SourceExecutionRef` field exists (7C) but nothing sets it reliably in a production-realistic path | one Core-owned constructor that ALWAYS sets it from execution context | full | `review.NewFindingObservationFromExecution` + `ExecutionProvenance`/`FindingClaim` split |
| Agent attribution | `PerformanceObservation.AgentID` (ADR 0005), Core-dispatched | unchanged | none | reused as-is |
| Model attribution | `ModelUsageRef` (ADR 0006), transport-echoed only | unchanged, deduped | partial | merge duplicate `(RouteID,Provider,Model)` entries by summing `Count` in `convertModelUsages` |
| Route attribution | `ModelUsageRef.RouteID` already distinct from `Model` | documented safety analysis | partial | "Route alias semantics" section below (no code change - boundary intact) |
| Multi-model execution | `ModelUsages []ModelUsageRef` already a slice | explicit execution-level-presence vs inference-usage-count semantics | full | `PairPerformanceSummary`/`CountModelUsage`, two distinctly named fields |
| Pair metrics | `AgentPerformanceSummary` (agent-only) | `(Agent, TaskClass, Route/Model)` cohort aggregation | full | `AggregatePair` (pair.go) |
| Joint cohorts | none | tenant+task-class-scoped, sample-size-gated | full | `AggregatePair` reuses `ObservationFilter`/`DefaultMinimumObservationCount` |
| Feedback to Switchyard | none | bounded, observational, no transport exists | full (interface only) | `ModelOutcomeObserver`/`ModelOutcomeRecord`, `TelemetryModelOutcomeObserver` - no HTTP client, documented residual risk |
| Tenant isolation | `ObservationFilter.Tenant` fail-closed (ADR 0005/0006, unchanged) | unchanged | none | `AggregatePair` inherits it via `dataset.Observations` |

## Part 1: Production Finding -> Execution Attribution (mandatory 7C closure)

`review.NewFindingObservationFromExecution(provenance ExecutionProvenance, claim FindingClaim) (FindingObservation, error)`
is now the ONE constructor that ever populates a `FindingObservation`'s
provenance fields (`SourceAgentID`, `SourceExecutionID`,
`SourceExecutionRef`). The two parameter types are deliberately split so
agent-supplied data has **no field** through which it could reach a
provenance position:

```go
type ExecutionProvenance struct {
    Tenant      string
    TaskID      TaskID
    ExecutionID string             // Core-owned
    AgentID     supervisor.AgentID // Core-dispatched
}
type FindingClaim struct {
    ID, FindingID      string/TaskID
    ArtifactVersion    ArtifactVersion
    EvidenceRefs       []string
    ConsumedFindingIDs []string
    Stance             ObservationStance
    Timestamp          time.Time
    // no execution/agent-reference field exists here at all
}
```

`TestNegative_ClaimCannotSmuggleProvenance` proves this structurally via
reflection - there is no field on `FindingClaim` whose name even
resembles an execution/agent reference. Mirrors
`supervisor.ExecutionResult.ExecutionID`'s own discipline exactly: if a
provider/agent-controlled value could ever influence this, Core
overwrites or ignores it; here, agent-controlled data cannot even be
passed into the position that matters, by the type system.

**Fails closed** (`*review.MissingExecutionProvenance`) if
`ExecutionID`, `TaskID`, or `AgentID` is empty - never falls back to
guessing by TaskID, by "latest execution," by timestamp, or by agent.
`FindingObservation.HasExecutionProvenance()` is the single predicate
any joint-attribution consumer should check before trusting an
observation's binding; a legacy/external finding lacking it may remain
valid for display/review under existing semantics but must never
contribute to joint quality attribution (this mirrors
`ReviewFinalizationHook`'s own pre-existing fail-closed behavior when
resolution fails - no new enforcement code was needed there, since 7C
already made that path fail closed for exactly this reason).

### Tests (Part 1)

`finding_provenance_test.go`: Core-owned fields set from execution
context; missing-ExecutionID/TaskID/AgentID each fail closed; claim
cannot smuggle provenance (structural); `HasExecutionProvenance`
correctly distinguishes a bare `SourceExecutionID` (insufficient alone)
from a real `SourceExecutionRef`/`EvidenceRefs` (sufficient).
`joint_e2e_test.go`'s `TestNegative_ProviderSuppliedFakeExecutionIDOverwritten`,
`TestNegative_FindingCannotBindToAnotherTasksExecution`,
`TestNegative_EvidenceReferencingDifferentTaskNeverResolves` extend this
with end-to-end and cross-task/cross-source proofs.

## Part 2: Joint Observation Model

No second outcome table. `PairPerformanceSummary` (new, `pair.go`)
aggregates over the SAME `PerformanceObservation` records
`AgentPerformanceSummary` already reads:

```go
type PairPerformanceSummary struct {
    AgentID   supervisor.AgentID
    TaskClass TaskClass
    RouteAlias, Provider, Model string

    ObservationCount, FinalizedCount int
    CompletedCount, FailedCount, TimeoutCount int
    VerifiedCount, RejectedCount, ContestedCount, NeedsEvidenceCount int
    InferenceUsageCount int
    MedianDuration time.Duration
    InsufficientData bool
}
```

No composite score field, consistent with `AgentPerformanceSummary`
(ADR 0005) and `AgentRecommendation` (ADR 0006).

### Multi-model execution semantics (explicit, two distinct countings)

- **Execution-level presence** (`ObservationCount`/`FinalizedCount`/
  quality counts in `PairPerformanceSummary`): one execution whose
  `ModelUsages` contains AT LEAST ONE matching entry counts ONCE,
  regardless of how many inference calls it made.
- **Inference usage count** (`InferenceUsageCount`, and the standalone
  `CountModelUsage` helper's second return value): the SUM of
  `ModelUsageRef.Count` across every matching entry in every matching
  execution.

These are never mixed into one field. `TestPair_ExecutionLevelPresenceVsInferenceUsageCount`
and `TestNegative_MultiModelExecutionNeverCollapsedToSingleModel` prove a
3-call, 2-model execution (`Call1 R1->ModelX, Call2 R1->ModelX, Call3
R2->ModelY`) is stored as ONE observation (never three), reports
"executions involving Model X = 1, usages = 2" and "involving Model Y =
1, usages = 1" simultaneously and correctly, and counts its quality
outcome exactly once.

### Deduplication

`convertModelUsages` now merges entries sharing the same
`(RouteID, Provider, Model)` identity by summing `Count`, derived only
from existing fields - no new ID introduced. This closes the one real
duplication risk within a single execution's reported usages (a
provider reporting the same route/model twice as separate entries could
otherwise inflate `InferenceUsageCount`). `TestModelUsageDedup_DuplicateRefsMergedBySummingCount`
proves it. Cross-observation duplication (the same execution reported
twice) remains structurally prevented by `PerformanceObservation.ID`'s
existing idempotent `Save` (ADR 0005/0006, unchanged) - a joint metric
built from `Query` can never double-count the same execution twice for
an unrelated reason.

### Pair sample size

`AggregatePair` flags `InsufficientData` using the SAME
`DefaultMinimumObservationCount` (ADR 0005) every other aggregation
already uses - no second, separately-tuned threshold.
`TestPair_SampleSizeInsufficientWhenSparse` proves the exact scenario
ADR 0007 specified: Agent-level n=12 is sufficient while the SAME
agent's Model-X-specific cohort (n=4) is correctly flagged insufficient,
with both counts still honestly reported (never hidden, exactly like
ADR 0005's cold-start discipline).

### Cohort matching / no cross-task-class comparison

`AggregatePair` is always scoped to one exact `TaskClass`, identical to
`Aggregate`. `TestNegative_CrossTaskClassPairComparisonNeverHappens`
proves a `documentation`-class observation contributes nothing to a
`security_review` pair cohort.

### Tenant isolation

`AggregatePair` calls `dataset.Observations(scoped)`, which for the
shipped `StoreDataset` is `Store.Query` - the SAME fail-closed tenant
validation (`ObservationFilter.validate()`, ADR 0005/0006) every other
query already goes through. No new administrative cross-tenant mode was
introduced - `AllTenants` remains the pre-existing, explicit
operator-tooling opt-in; pair queries have no separate exception.
`TestNegative_CrossTenantPairQueryDenied` proves an unscoped pair query
fails closed exactly like an unscoped `Aggregate`/`Query` call already
did.

### Historical corrections / no stale materialized metrics

`AggregatePair`, like `Aggregate`, is **query-time aggregation over
live store state** - there is no cache, no materialized summary row, no
separate invalidation problem. If a quality outcome later changes
(`supported` -> `contested` -> `verified`), the NEXT call to
`AggregatePair` simply re-reads the current `PerformanceObservation`
rows and reflects it - there is nothing to invalidate.

### Failed/timeout/pending handling (no survivorship bias)

`AggregatePair` counts `CompletedCount`/`FailedCount`/`TimeoutCount`
unconditionally, exactly mirroring `Aggregate`'s existing discipline.
`TestNegative_FailedAndTimedOutExecutionsCountedInPairMetrics` and
`TestNegative_RejectedExecutionsNotOmittedFromPairMetrics` prove
non-successful outcomes are counted, not quietly excluded.
`TestNegative_PendingObservationNotCountedAsVerifiedInPairMetrics`
proves a never-finalized observation is never counted as verified -
and, in writing this test, independently re-confirmed ADR 0005's
anti-revert `Save` guard is still active (a direct attempt to write a
`Finalized`-then-reverted-to-`Pending` record was correctly rejected by
the store, exactly as designed - not a defect, a working protection
this test had to route around rather than through).

## Route alias semantics (analysis, no architecture change)

Analyzed exactly what a route alias currently means, per the mandate's
"safe abstraction" vs. "dangerous abstraction collapse" distinction:

**Finding: safe abstraction, confirmed by code inspection.**
`SwitchyardInferenceRouter.Route` resolves a Stage to a route alias
string (e.g. `"supervisor"`, or whatever `Routes`/`DefaultRoute` map a
stage to); `Complete` sends that alias as the wire-level `"model"` field
to Switchyard's own OpenAI-compatible endpoint. The alias-to-concrete-
model mapping is resolved entirely INSIDE Switchyard's own, external
configuration - nothing in this repository reads, caches, or hardcodes
that mapping. `HarnessMesh supplies a route alias; Switchyard selects
the model` holds exactly as documented in `supervisor/modelrouting.go`'s
own existing comments. There is no code path anywhere that resolves
`route "foo" -> always model X` inside this codebase - if such a
collapse exists, it would live entirely in Switchyard's own deployment
configuration, outside this repository's authority and visibility.

This phase made no architecture change here, per the mandate's explicit
instruction: "Ändere die Architektur nur, wenn die Authority-Grenze real
verletzt wird" - no real violation was found.

## Agent Advisor enhancement (pair evidence, still agent-only)

`DeterministicAdvisor.IncludePairEvidence bool` (new, default `false` -
an explicit opt-in, not a default behavior change to the Phase 7B
advisor). When true, for each sufficient-data candidate, `pairEvidenceReason`
finds the agent's historically most-used route (via the existing
`AggregateModelUsage`) for this task class and, ONLY if that specific
route's pair cohort itself meets the advisor's configured minimum
observation count, appends one bounded, observational sentence, e.g.:

> "observed evidence: 15 of 15 finalized runs involved route
> "strong-reasoning" (15 verified, 0 rejected) - observational, not a
> model selection"

Never "select Model X." `TestAdvisor_PairEvidenceIncludedWhenSufficientAndRequested`
proves the sentence appears and never contains selection language;
`TestAdvisor_PairEvidenceOmittedWhenSparse` and
`TestAdvisor_PairEvidenceDisabledByDefault` prove the negative cases.

### Hard model-selection prohibition (structural, re-confirmed)

`AgentRecommendation`/`AgentAdvice` (ADR 0006, unchanged by this phase)
still have no model/provider/route field at all -
`TestNegative_AgentRecommendationCannotExpressModelSelection` re-runs
the exact reflection-based structural proof ADR 0006 Phase 7B already
established, confirming Phase 8's pair-evidence addition did not
weaken it (the enhancement only ever appends to `Reasons []string`, a
field that already existed and was already documented as bounded text,
never a new typed field).

## Model outcome feedback (evidence, never a routing command)

```go
type ModelOutcomeRecord struct {
    Tenant string
    ExecutionID string
    TaskClass TaskClass
    AgentID supervisor.AgentID
    ModelUsage ModelUsageRef
    ExecutionOutcome ExecutionOutcome
    QualityOutcome QualityOutcome
}
type ModelOutcomeObserver interface {
    ObserveModelOutcome(ctx context.Context, outcome ModelOutcomeRecord) error
}
```

Emitted from TWO points, mirroring the dual-lifecycle pattern already
established for `ExecutionObserver`/`ReviewFinalizationObserver`:
once per `ModelUsages` entry at execution time (via
`SupervisorExecutionObserver.ModelOutcome`, `QualityOutcome` still
`QualityUnreviewed`), and again at finalization time (via
`ReviewFinalizationHook.ModelOutcome`, with the now-authoritative
quality outcome). Both are optional (nil-safe) fields, failure-isolated
exactly like every other observer in this package: a feedback-sink
error or panic is recorded via `WiringTelemetry`
(`RecordModelOutcomeFeedback`/`RecordModelOutcomeFeedbackFailure`) and
never affects execution truth, review truth, or routing -
`TestModelOutcome_FeedbackSinkFailureDoesNotAffectExecutionResult` and
`TestModelOutcome_FeedbackSinkPanicDoesNotCrashExecution` prove this.

**No Switchyard transport was built.** `internal/modelrouting` has no
outcome/feedback endpoint of any kind today - `SwitchyardInferenceRouter`
only ever sends chat-completion requests. Per the mandate's explicit
instruction not to fabricate a transport that doesn't exist, this phase
ships only the HarnessMesh-side interface/persistence boundary
(`ModelOutcomeObserver`) plus a telemetry-only implementation
(`TelemetryModelOutcomeObserver`). **Feedback transport to Switchyard
itself is a documented residual risk / future integration point**, not
built here.

`TestNegative_ModelOutcomeRecordCannotCarryAgentRecommendation` is the
structural counterpart to the advisor's own guard: `ModelOutcomeRecord`
has no field through which feedback could ever express "select this
agent" - Switchyard-side feedback cannot influence agent routing through
this type, by construction.

## No Joint Router

`TestNegative_NoJointRouterExistsAnywhere` (`joint_router_guard_test.go`)
is a repository-wide structural guard: it walks every `.go` file and
fails if the literal identifiers `JointRouter` or `SelectAgentAndModel`
appear anywhere outside the guard test itself. This is deliberately a
repo-scan rather than a type-level check, since the property being
guarded is "this identifier must never be introduced," which a Go
compile-time check cannot express for something that doesn't exist yet.

## Selection bias / causal language (documented, not solved)

Repeated here verbatim, as both ADR 0005 and ADR 0006 already required
for their own scopes, now extended explicitly to the joint case:

**Switchyard's observed model outcomes are conditional on Switchyard
having selected those models. Agent outcome history is conditional on
HarnessMesh having selected those agents.** Neither pair comparison is
therefore an unbiased, randomized trial - `AggregatePair` reports raw,
honest counts over whatever was actually assigned, never a corrected or
debiased estimate. **Joint outcome history is observational and
selection-biased. It does not establish causal superiority of an agent,
model, route, or pair.** The pair-evidence advisor sentence (above) uses
strictly "observed evidence" / "historically ... involved" phrasing,
never "would have succeeded" or "is intrinsically better" - no
counterfactual claim is ever constructed anywhere in this phase's code.

## Feedback-loop observability

Every advisor-influenced `Tick` already records a distinct
`ExecutionEvent` (`EventAdvisorySelection` vs. none for a fallback-only
tick - ADR 0006 Phase 7B, unchanged). This phase adds no NEW
feedback-loop-specific persistence beyond what 7B already provides,
since Phase 8 introduces no new routing decision point (the advisor's
pair-evidence addition only changes `Reasons` text, never `Ranked`
order or `Basis`) - "advisor policy version" provenance is unchanged
from `AdviceBasis`/`DefaultAdvisorPolicyVersion`.

## Security closure review

Checked explicitly, no code change required except where noted:

| Question | Finding |
|---|---|
| Can HarnessMesh effectively select a model despite the stated boundary? | No - confirmed by code inspection of `SwitchyardInferenceRouter`; the alias-to-model mapping lives entirely in Switchyard's own external config, never read or cached here. |
| Can Switchyard affect agent authority? | No - `ModelOutcomeRecord` has no agent-recommendation field (`TestNegative_ModelOutcomeRecordCannotCarryAgentRecommendation`); nothing calls back from a `ModelOutcomeObserver` into `Scheduler`. |
| Can a Finding without Core provenance poison Pair metrics? | No - `ReviewFinalizationHook`'s exact-resolution-or-fail-closed discipline (ADR 0006 Phase 7C, reused unchanged) means an unattributable finding is simply never finalized into any observation, pair or otherwise. |
| Can route aliases become hidden hardcoded model selectors? | Not within this codebase - see "Route alias semantics"; the risk, if any, lives entirely in Switchyard's own deployment config, outside this repository's authority. |
| Can duplicate model usages inflate verified outcome counts? | No - `convertModelUsages` merges same-identity entries (summing `Count`, not inflating execution-level presence); execution-level quality counting is presence-based, immune to call-count inflation regardless. |
| Can pair-level data leak between tenants? | No - `AggregatePair` inherits the existing fail-closed tenant validation via `dataset.Observations`. |
| Can sparse historical data become strong advice? | No - pair-level `InsufficientData`/`minObs` gating (both in `AggregatePair` and in `pairEvidenceReason`'s own, advisor-configured threshold check) suppresses the sentence entirely rather than presenting a weak signal as strong. |

No real defects were found during this phase's implementation beyond
what is already covered above (the anti-revert `Save` guard behaving
correctly when a test tried to route around it - confirmed working as
designed, not a defect).

## Tests / Verification

```text
gofmt -l .                  -> clean
go build ./...              -> clean
go vet ./...                -> clean
go test -race ./...         -> all packages ok, zero regressions
```

`internal/performance`: 103 tests passing under `-race` (+27 from Phase
8: finding-attribution E2E, pair aggregation, multi-model semantics,
model-outcome feedback, advisor pair-evidence, the no-Joint-Router
guard). `internal/review`: 81 tests passing under `-race` (+6, the
Core-owned Finding-provenance constructor suite). `internal/supervisor`:
132 tests passing, unchanged (Phase 8 made no `internal/supervisor`
changes). Existing fuzz targets re-run clean
(`FuzzDecodePlanResponse`, `FuzzDecodeSynthesisResponse`,
`FuzzDetectDuplicateKeys` in `internal/supervisor`;
`FuzzDecodeArbitrationResponse`, `FuzzEvidenceValidate` in
`internal/review`) - no new fuzz target added, since Phase 8 introduces
no new untrusted-parsing/wire-decode boundary (every new field is
Core-derived or caller-supplied typed Go data, aggregated query-time
over already-authoritative store state).

## Residual risks

- **No real `supervisor.ExecutionProvider` implementation exists
  anywhere in the repository** (only `FakeExecutionProvider`) - a
  pre-existing gap, not introduced or closed by this phase. Every test
  in this phase (like every test in ADR 0005/0006/7A/7B/7C before it)
  exercises realistic SIMULATED call sequences; the real agent-runtime
  integration that would populate `ModelUsages`/`Findings` from an
  actual subprocess remains future work.
- **`internal/collaboration`'s agent-text-parsed Finding pipeline
  (`protocol.FindingPayload`) is still not connected to
  `review.ReviewState.Ingest`** - `NewFindingObservationFromExecution`
  is ready to be the Core-owned construction point once that connection
  is built, but building the connection itself is new, large-scope
  integration work, correctly left out of this closure phase's scope.
- **Model-outcome feedback has no Switchyard-side transport** -
  `ModelOutcomeObserver` is a HarnessMesh-side interface/boundary only;
  wiring it to an actual Switchyard endpoint requires that endpoint to
  exist first, which it currently does not.
- **`ServedProvider` is hardcoded to `"switchyard"`** in
  `SwitchyardInferenceRouter.Complete` - the actual underlying provider
  (gpt/claude/etc.) is not separately, authoritatively knowable from
  trusted transport today beyond whatever model-name string Switchyard's
  response echoes back. Joint queries filtering by `Provider` are
  therefore less reliable than filtering by `Model`/`RouteID` until
  Switchyard's response shape carries real provider identity.
- **No full cross-product enumeration/reporting engine** - `AggregatePair`
  is a per-cohort query primitive; answering "list every observed
  Agent+Model combination" requires a caller to enumerate agents/models
  itself (e.g. via existing agent listing + `AggregateModelUsage`) and
  call `AggregatePair` per candidate, by design, to avoid building the
  "analytics database" explicitly ruled out.

## Deviations from ADR

- Dropped `Tenant TenantID` from `PairPerformanceSummary` (no such type
  exists in this codebase; tenant is always a plain `string` filter,
  consistent with `AgentPerformanceSummary`'s own, pre-existing shape).
- Dropped `RoutingDecisionID` from `ModelOutcomeRecord` (no persisted
  model-routing-decision ID exists anywhere to reference;
  `ModelUsage.RouteID` already serves the correlation role).
- Did not introduce a separate `JointDecisionDataset` interface
  (`SummarizeAgent`/`SummarizeModel`/`SummarizePair`) - the mandate
  itself said "only if sensible"; plain functions (`Aggregate`,
  `AggregatePair`, pre-existing `AggregateModelUsage`) already match
  this package's established style and avoid an unnecessary wrapper.

## Final invariants (proven by the test suite)

```text
Every production finding used for quality attribution has authoritative execution provenance. -> NewFindingObservationFromExecution is the only constructor; fails closed otherwise
Missing finding provenance fails closed for Joint Quality Attribution.   -> MissingExecutionProvenance / HasExecutionProvenance / ReviewFinalizationHook's unchanged fail-closed resolution
Agent identity comes from Core dispatch.                                 -> ExecutionProvenance.AgentID, unchanged since ADR 0005/0006
Execution identity comes from Core.                                      -> supervisor.NewExecutionID (ADR 0006 Phase 7C), reused unchanged; TestNegative_ProviderSuppliedFakeExecutionIDOverwritten
Model identity comes from trusted routing/transport metadata.            -> ModelUsageRef populated only from ExecutionProvider/transport context, never agent/LLM text
HarnessMesh selects agents.                                              -> Scheduler unchanged; no code path in this phase touches agent selection
Switchyard selects models.                                                -> SwitchyardInferenceRouter unchanged; route alias resolution confirmed external to this codebase
HarnessMesh cannot force a concrete model through Joint Intelligence.    -> AgentRecommendation/AgentAdvice/ModelOutcomeRecord all structurally lack a model-selection field
Switchyard outcome feedback cannot select an agent.                       -> ModelOutcomeRecord has no agent-recommendation field; TestNegative_ModelOutcomeRecordCannotCarryAgentRecommendation
Multi-model executions are not collapsed into a false single-model identity. -> ModelUsages remains a slice; TestNegative_MultiModelExecutionNeverCollapsedToSingleModel
Execution-level quality is counted once regardless of model-call count.  -> PairPerformanceSummary's execution-level-presence semantics, distinct from InferenceUsageCount
Cross-tenant joint history cannot influence advice.                      -> AggregatePair inherits ObservationFilter's fail-closed tenant validation; DeterministicAdvisor.Tenant unchanged from 7B
Sparse pair data cannot create strong recommendations.                   -> InsufficientData gating in AggregatePair and pairEvidenceReason's own minObs check
Joint history is observational evidence, not causal proof.               -> documented verbatim in this ADR; pair-evidence sentences never use counterfactual language
No unified Agent+Model router exists.                                    -> TestNegative_NoJointRouterExistsAnywhere (repository-wide structural guard)
Decision Intelligence observes and advises.                              -> unchanged from ADR 0006
Core and Switchyard retain their separate authorities.                   -> unchanged from ADR 0001-0006; no authority-crossing code introduced
LLM output remains proposal, never authority.                            -> unchanged from ADR 0002-0006; Phase 8 adds no new LLM surface
```
