# ADR 0004: Authoritative Evidence and Review State Closure (ADR 0002 Phase 5)

## Status
Implemented and verified. Depends on [ADR 0001](0001-agent-control-plane-switchyard-model-routing.md),
[ADR 0002](0002-supervisor-agent-with-deterministic-control-plane.md) (Phases 1-3), and
[ADR 0003](0003-evidence-driven-arbitration-and-independent-review.md) (Phase 4). This
phase closes five foundation gaps ADR 0003 explicitly left open as
residual risk: evidence-reference validation, real review-limit
enforcement, Finding-status projection, full provenance, and reliable
rejection of stale/tampered/cross-tenant/cross-artifact evidence.

## Context

ADR 0003 built the correlation/independence/arbitration layer but
deliberately treated `EvidenceRefs` as opaque strings, left three of five
declared `ReviewLimits` unenforced, and left `ReviewGroup.Status` and
`protocol.FindingPayload.Status` as two disconnected values. Phase 5's
central instruction: **"Evidence reference ≠ Evidence truth."** A string
like `E123` must be proven - exists, correct tenant, correct
artifact/version, correct execution, valid provenance, current, not
tampered - before it can ever influence an authoritative review
transition.

## Repository analysis (performed before writing code)

- `protocol.EvidencePayload` (types.go:385) already exists, persisted via
  `store.Store.SaveEvidence`/`GetEvidence`/`GetEvidenceByFinding`
  (SQLite-backed). It has `ID`, `FindingID`, `SourceAgent`, `Type`
  (reusing `protocol.EvidenceType` - exactly the Evidence Kinds this
  phase needed, already covering test_result/static_analysis/
  code_location/etc.), and a `Metadata map[string]any` JSON column - but
  **no** tenant, artifact-version, execution, or digest/integrity field.
- `store.SQLiteStore.UpdateFindingStatus(ctx, sessionID, findingID, status)`
  already exists and already fails closed (returns an error, checked via
  `RowsAffected`) if the finding doesn't exist - exactly the primitive
  Finding-status projection needed, with no new store method required.
- No `internal/assurance`/`internal/evidence` package, no
  attestation/PKI mechanism, no existing content-digest/integrity
  infrastructure beyond MeshCommit's tree-hash (a different, change-
  transaction-scoped concept this phase does not need to reuse directly).
- `economy.RoutingDecision` records agent selection, not evidence/
  provenance lineage - nothing reusable there for "did reviewer B consume
  artifact or merely consume agent A's conclusion" (already solved by
  Phase 4's `ConsumedFindingIDs`, which this phase composes with rather
  than replaces).

### Gap Matrix

| Concern | Existing | Target | Gap | Action |
|---|---|---|---|---|
| Evidence identity | `EvidencePayload.ID` (string only) | ID + content digest + bindings | no digest, no bindings | new `EvidenceRecord` (internal/review), persisted via existing `EvidencePayload.Metadata` |
| Evidence persistence | `SaveEvidence`/`GetEvidence` (SQLite) | authoritative `EvidenceStore` | no binding/integrity columns | reuse existing table + `Metadata` JSON round-trip; no schema migration, no parallel store |
| Artifact binding | none on `EvidencePayload` | per-record `ArtifactVersion` | full | new, round-tripped via `Metadata` |
| Execution binding | none on `EvidencePayload` | per-record `TaskID`/`ExecutionID` | full | new, round-tripped via `Metadata` |
| Tenant binding | none on `EvidencePayload` | per-record `Tenant` | full | new, round-tripped via `Metadata` |
| Integrity | none | content digest, fail-closed on mismatch | full | new, `EvidenceIdentityConflict` on tampered replay |
| Review limits | 2 of 5 enforced (Phase 4) | all 5 enforced | `MaxIndependentReviews`/`MaxReviewDepth`/`MaxReviewCost` unenforced | enforced in `Ingest`/`IngestSuccessor`/`ValidateActions` |
| Finding status sync | two disconnected values | one-way projection | full | `FindingProjector` + `EngineFindingProjector` (thin wrapper over `UpdateFindingStatus`) |
| Provenance | `SourceAgentID`/`ConsumedFindingIDs` (Phase 4, claim-level) | + evidence-level producer/execution/kind | partial | `EvidenceRecord.ProducerAgentID`/`TaskID`/`ExecutionID`/`Kind` |

## Authoritative evidence model

`internal/review.EvidenceRecord{ID, Tenant, Subject, Artifact,
ArtifactVersion, TaskID, ExecutionID, ProducerAgentID, Kind, Digest,
Status, SupersededBy, CreatedAt}` (evidence.go). Every binding field is
set only by Core/execution-context code - there is **no wire decoder
anywhere in this package that constructs an `EvidenceRecord` from
untrusted JSON**. `WireAction` (the Arbiter's only output schema) has no
evidence-creation field at all; `TestSecurity_ArbiterCannotFabricateEvidenceID`
proves an attempted `"evidence_id"` field is rejected as unknown at
decode time, structurally, not by a runtime check.

`EvidenceKind` is `protocol.EvidenceType` reused directly - no parallel
kind vocabulary.

## Evidence identity and integrity

Identity is `ID + Digest + bindings`, never a bare filename/label.
`MemoryEvidenceStore.Put` is idempotent on an exact duplicate (same ID,
digest, bindings - a safe replay no-op) and fails closed
(`EvidenceIdentityConflict`) on the same ID with a different digest -
`TestSecurity_TamperedEvidenceContentRejected` proves the *original*
record survives a rejected tamper attempt untouched. `EvidenceRecordStatus`
(`current`/`superseded`/`revoked`) gives immutable-vs-replaceable
semantics explicitly, with `Revoke`/`Supersede` as Core-only operations
(no `ArbiterAction` can reach them).

## Evidence store

`EvidenceStore` interface (`Put`/`Get`/`Exists`, matching the request's
example exactly). `MemoryEvidenceStore` is the deterministic test/proof
double (Phase 4/5's `FakeArbiter`/`FakeSupervisor` pattern). The real
implementation, `collaboration.EngineEvidenceStore`
(`internal/collaboration/review_adapters.go`), integrates with the
*existing* persistent store rather than inventing a parallel one: it
calls the real `store.Store.SaveEvidence`/`GetEvidence` and round-trips
the new binding/integrity fields through `EvidencePayload.Metadata`
(already a persisted JSON column) - no schema migration, proven against
a real SQLite-backed `Engine` in
`TestEngineEvidenceStore_PutGetRoundTripsBindingMetadata`. The one-way
dependency direction established in Phase 2A/3 continues: `collaboration`
imports `review`'s interfaces to build the adapter; `review` imports
nothing from `collaboration`.

## Evidence validation

`EvidenceValidator.Validate(ctx, id, expected EvidenceExpectation) (EvidenceRecord, error)`.
`CoreEvidenceValidator` always re-fetches from the store - never trusts a
cached/snapshotted record - checking, in order: exists
(`EvidenceNotFound`) → tenant (`EvidenceTenantMismatch`) → artifact
version (`EvidenceVersionMismatch`) → task/execution
(`EvidenceExecutionMismatch`) → acceptable kind/missing digest
(`EvidenceIntegrityFailure`) → revoked (`EvidenceRevokedError`) →
superseded/stale (`EvidenceStaleError`). All nine typed errors from the
request are implemented. Wired into `ReviewAuthorizer` via
`NewReviewAuthorizerWithEvidence` - `nil` (the Phase 4 default) preserves
exact prior behavior for callers/tests that never populate
`EvidenceRefs`, so nothing broke by introducing this.

**Fail-closed scope, deliberately whole-batch, not per-observation**: if
*any* `EvidenceRef` among the observations being counted for a
verification/rejection attempt fails validation, the entire attempt fails
closed (`TestReviewAuthorizer_EvidenceValidation_UnknownRefFailsVerification`) -
evidence is never silently dropped from the count while the decision
proceeds on what's left.

## TOCTOU

`checkEvidence` runs inside `ReviewApplier.Apply`'s existing lock,
against state read fresh that same call - and critically, it calls
`EvidenceValidator.Validate`, which itself re-fetches from the store
every time, never a snapshot. `TestSecurity_EvidenceRevokedAfterArbitrationBeforeApply`
proves the exact scenario the request names: an Arbiter "saw" valid
evidence, the evidence is revoked before `Apply` runs, and the
revocation is honored - closing the same class of window ADR 0002's
Phase 2A/2B closure review found and fixed for task execution.

## Artifact-version binding (stale evidence)

Unchanged invariant from ADR 0003 (`ReviewGroup` scoped to one
`ArtifactVersion`), now also enforced at the *evidence* level:
`EvidenceVersionMismatch` fires if an observation's evidence belongs to a
different version than the group it's being counted in.
`TestSecurity_StaleEvidenceAfterRemediationRejected` proves the explicit
"v2 remediates v1, but v2 evidence != v1 evidence" exception: even a
successor group created specifically because it remediates a prior
group's claim still cannot use the *predecessor's* evidence to authorize
its own verdict.

## Review-limit enforcement (now real)

All five `ReviewLimits` are enforced, each with the requested typed
error: `MaxEvidenceRequests`/`MaxArbitrationRounds` (Phase 4, unchanged)
plus three new in Phase 5: `MaxIndependentReviews`
(`IndependentReviewLimitExceeded`, enforced in `Ingest` - a new distinct
agent beyond the cap is rejected, observation not appended),
`MaxReviewDepth` (`ReviewDepthLimitExceeded`, enforced in the new
`IngestSuccessor` - bounds a remediation chain's length), `MaxReviewCost`
(`ReviewCostLimitExceeded`, enforced in `ValidateActions` against
`DefaultActionCost`, a **deterministic, documented, non-monetary** cost
unit per action type - explicitly not claiming fake provider-cost
precision, per the request's own allowance for this when real synchronous
costs aren't available). None are readable or settable from any
`ArbiterAction` - all are `ReviewLimits`/`ReviewState` construction-time
configuration only.

## Finding-status projection

**Single owner, one-way, explicitly chosen per the request's own
guidance**: `ReviewGroup`/`ReviewState` remain authoritative;
`FindingPayload.Status` is a *projection*. `FindingProjector` interface
(`Project(ctx, sessionID, findingID, status)`); status mapping is the
**identity function** - no translation table needed, because ADR 0003
already made `ReviewGroup.Status` a `protocol.FindingStatus` value. The
real path, `collaboration.EngineFindingProjector`, is a thin,
additive wrapper over the already-existing, already-fail-closed
`store.UpdateFindingStatus` - it does not touch `SubmitFinding`/
`Challenge`/`Resolve`'s existing conversational-lifecycle logic at all.

### Consistency model (honest, not pseudo-atomic)

`ReviewState` (in-memory, Phase 1-5) and `store.Store`'s finding table
(SQLite, `collaboration`) are two independent stores with no shared
transaction. **This phase does not claim true cross-store atomicity**,
per the request's explicit demand not to assert a guarantee that isn't
real. The actual model: the review-state transition commits first
(atomically, under `ReviewState`'s own lock, exactly as already proven);
projection is attempted immediately after, *outside* that lock; if
projection fails, `FindingProjectionFailure` is returned but **the
review transition is never rolled back** - `ReviewState` remains
authoritative regardless of projection outcome. `Project` is idempotent
(`UpdateFindingStatus` simply re-sets the same value), so a failed
projection is always safely retryable - this is the honestly-disclosed
"eventual consistency with idempotent, retryable projection," not a fake
two-phase commit. Proven by `TestProjection_FailureDoesNotRollBackReviewState`.

## Cross-tenant isolation / Snapshot scoping

Unchanged core invariant (`FindGroup` requires exact tenant match) plus
the evidence layer's own tenant check
(`TestSecurity_ArbiterReferencesEvidenceFromAnotherTenant`). ADR 0003's
flagged residual risk - `Snapshot()` has no tenant filter - is closed
additively: `ReviewState.SnapshotForTenant(tenant)` is now the
recommended tenant-scoped primitive; `Snapshot()` (unscoped) remains for
genuinely cross-tenant callers (operator tooling, tests) rather than
being removed and breaking them.

## Replay / idempotency

Evidence: identical replay (same ID+digest+bindings) is a safe no-op;
same ID with a different digest is `EvidenceIdentityConflict`, never a
silent overwrite. Observations: unchanged from Phase 4
(`FindingCorrelationFailure` on a replayed observation ID, never
inflating the independent count).

## Concurrency

`ReviewApplier.Apply` and `ReviewState.Ingest`/`IngestSuccessor` all hold
`ReviewState`'s single lock across their full validate/authorize/mutate
or correlate/append sequence - the same pattern already proven safe in
Phase 4, now re-verified under the new evidence/limit/projection logic
added this phase. New tests:
`TestConcurrency_VerifyRejectRaceOnSameClaim` (20 goroutines racing
verify vs. reject on the same claim - exactly one terminal status wins,
no goroutine observes a conflicting one). All existing Phase 4
concurrency tests re-run unchanged and still pass (two were updated only
to raise `MaxIndependentReviews` for their own stress-test agent counts,
not because of a behavior regression).

## Security findings discovered during implementation

No new defects found this phase (unlike Phases 2A/2B/3/4, each of which
found at least one real bug) - the evidence/limit/projection logic was
built directly against this phase's own test suite from the start,
including the TOCTOU and tenant/version-mismatch scenarios, before any
test ran green. The closest analogue to a "finding" is a design
clarification made explicit during implementation: an already-terminal
group's `RejectClaimAction`/`RecommendVerificationAction` early-return
returns the group's *actual* current status (e.g. `verified`) as a
successful no-op even when the caller asked for the opposite transition
(`reject`) - this is intentional (Core does not re-litigate a settled
claim), not a bug, and `TestConcurrency_VerifyRejectRaceOnSameClaim`
explicitly validates this behavior rather than treating it as a defect.

## Fuzzing

New: `FuzzEvidenceValidate` (arbitrary `EvidenceID` strings, including
path-traversal-shaped, null-byte, and SQL-injection-shaped inputs,
against a store seeded with exactly one real record) - zero panics,
zero false-positive validations, across ~3.4M executions. Existing
`FuzzDecodeArbitrationResponse` and the three `internal/supervisor` fuzz
targets re-run clean. `EvidenceRecord` itself has no fuzz target because
it has no decoder - there is nothing to fuzz for a type that can only be
constructed by trusted Core code, which is itself the security property
requested ("No LLM can create authoritative evidence").

## Verification

```text
gofmt -l .                  → clean
go build ./...              → clean
go vet ./...                → clean
go test -race ./...         → all 20 packages ok, zero regressions
```

`internal/review`: 73 tests passing under `-race`, 75.5% coverage (up
from 42/74.1% after Phase 4 - net addition of 31 tests; coverage held
roughly flat because this phase added substantial new branches alongside
new tests). `internal/collaboration`: 3 new tests for the real
`EngineFindingProjector`/`EngineEvidenceStore` adapters, all passing
against a real SQLite-backed `Engine`.

## Residual risks

- `EvidenceRecord`'s binding fields are *round-tripped* through
  `EvidencePayload.Metadata`, not first-class SQLite columns. Correct and
  queryable via the existing JSON column, but not indexable/queryable by
  SQL directly (e.g. "find all evidence for artifact version v2" requires
  loading and filtering in Go, not a `WHERE` clause). A future phase
  wanting that could migrate these fields into real columns without
  changing `EngineEvidenceStore`'s external behavior.
- `EngineEvidenceStore.Get`/`Exists` load *all* evidence for a session
  and filter in Go (no `GetEvidenceByID` exists in the current `Store`
  interface) - correct but O(n) in session evidence count. Acceptable at
  current scale; worth a dedicated store method if evidence volume grows
  large.
- `DefaultActionCost` is an explicitly deterministic, non-monetary cost
  model (documented in code and here) - it does not reflect real
  provider/token spend. A future integration with `internal/economy`'s
  cost tracking would replace it, not extend it, since the concerns are
  different (agent-selection cost vs. review-action cost).
- No digest *computation* helper is provided - callers (execution-result
  producers) are responsible for computing `Digest` themselves (e.g.
  `sha256` over canonical content/metadata) before calling `Put`. Adding
  a canonical-digest helper was judged unnecessary speculative
  infrastructure until a real `ExecutionProvider` integration needs one.

## Final invariants (proven by the test suite)

```text
Evidence references are never trusted by name alone.      -> CoreEvidenceValidator always re-fetches
Evidence must exist in the authoritative store.            -> EvidenceNotFound, fail closed
Evidence is bound to tenant/artifact/version/execution.     -> Evidence*Mismatch errors, all tested
Stale or revoked evidence cannot authorize a transition.    -> EvidenceStaleError/EvidenceRevokedError, TOCTOU-safe
Arbiter output remains untrusted.                           -> WireAction has no evidence-creation field at all
Review limits are Core-enforced.                            -> all 5 ReviewLimits now enforced, typed errors
Finding status is a projection of authoritative review state. -> FindingProjector, one-way, identity mapping
No LLM can create authoritative evidence.                   -> no decoder path exists for EvidenceRecord
No LLM can directly verify or reject a finding.              -> ReviewAuthorizer.verifyClaim/rejectClaim, unchanged
HarnessMesh Core owns evidence and review authority.        -> ReviewState/ReviewApplier are the only mutators
LLM output remains proposal, never authority.                -> every ArbiterAction still passes Validator+Authorizer
```
