# ADR 0003: Evidence-Driven Arbitration and Independent Review (ADR 0002 Phase 4)

## Status
Implemented and verified. Depends on [ADR 0001](0001-agent-control-plane-switchyard-model-routing.md)
(Switchyard/agent-selection separation) and [ADR 0002](0002-supervisor-agent-with-deterministic-control-plane.md)
(Supervisor/Scheduler Phases 1-3). Given this phase's size - a new package,
a new untrusted-output decode boundary, a new authorization surface, and a
genuinely new concern (multi-agent independence) - it gets its own ADR
rather than overloading ADR 0002 further, per that document's own
instruction to split when a phase warrants it.

This ADR's residual risks (evidence-reference validation, real
review-limit enforcement, Finding-status projection, full provenance,
stale/tampered/cross-tenant evidence rejection) are closed by Phase 5 -
see [ADR 0004](0004-authoritative-evidence-and-review-state-closure.md).

## Context

Phases 1-3 proved: an LLM-backed Supervisor can plan, replan, and
coordinate dependency-aware multi-agent execution, while HarnessMesh Core
remains the sole authority over task state, agent assignment, and
workflow completion. What Phases 1-3 never addressed: what happens when
**multiple agents independently report on the same underlying claim** -
possibly agreeing, possibly conflicting - and how HarnessMesh decides
whether that claim is actually true, without ever treating agreement
itself as proof.

## Problem

A naive implementation would count confirmations and call a majority
"verified." ADR 0002 Phase 4 explicitly forbids this: *"2 von 3 Agents
sagen HIGH -> Finding = verified"* is not sufficient assurance, because
agents are not automatically independent of each other (one may simply
be reading and echoing another's conclusion), and even genuinely
independent agreement is not the same thing as evidence-backed proof.

## Decision

A new package, `internal/review`, implements a deterministic-first
arbitration layer: `ReviewGroup`/`FindingObservation` correlate reports
on the same claim (never by free-text similarity alone), `DeriveStatus`
computes agreement/conflict/insufficient-evidence purely from counting
**mutually independent** observations (never a majority vote), and an
`Arbiter` (deterministic `FakeArbiter` first, `LLMArbiter` second,
exactly mirroring Phase 2A/2B's Supervisor-before-LLMSupervisor
sequencing) only ever *proposes* - `ReviewValidator` -> `ReviewAuthorizer`
-> `ReviewApplier`'s atomic apply remain the sole path to a claim
actually becoming `protocol.FindingVerified` or `protocol.FindingRejected`.

### Existing assurance/evidence architecture reused, not duplicated

Repository analysis (performed before writing any code) found:

- `protocol.FindingPayload`/`ChallengePayload`/`ResolutionPayload` already
  exist (`internal/protocol/types.go`) with a six-value `FindingStatus`
  enum (open/acknowledged/disputed/resolved/dismissed/superseded) owning
  the *conversational* lifecycle of a finding - unmodified by this phase.
- `collaboration.DeduplicateFindings` already does heuristic (file+line
  proximity + category/claim-text) correlation - but no fingerprinting,
  no artifact-version binding, and it operates on raw claim text.
  `internal/review` does not touch or replace it; it solves a different
  problem (claim-identity correlation for arbitration, not
  finding-list deduplication for display).
- `ChangeEvidence.TreeHash`/`ProofObligation.FreshnessPolicy`
  (MeshCommit subsystem) already establish the "tree-hash-scoped
  evidence" pattern this phase's `ArtifactVersion` concept follows - but
  `FindingPayload` itself has **no** artifact-version binding at all
  today, and MeshCommit's `FreshnessPolicy` field is set but never
  branched on beyond the hardcoded `"exact_tree"` value. `internal/review`
  does not import or extend MeshCommit; it defines its own minimal
  `ArtifactVersion` (an opaque string a caller supplies, e.g. a tree
  hash) scoped to `ReviewGroup`, deliberately not coupling to
  MeshCommit's specific change-transaction machinery.
- No `internal/assurance` or `internal/evidence` package exists; there
  was nothing to extend for the correlation/independence/arbitration
  concerns specifically, confirming this needed to be new, additive code
  rather than a missed extension point.
- `protocol.FindingStatus` is extended (additively: six new values -
  `unconfirmed`/`supported`/`contested`/`needs_evidence`/`verified`/
  `rejected`) rather than replaced, per the explicit instruction to reuse
  existing status semantics. The two existing, already-mutually-
  inconsistent counting switches in `collaboration/engine.go` are
  unaffected: neither is ever passed one of the six new values, because
  `ReviewGroup.Status` is **not** currently written back onto a live
  `FindingPayload.Status` (see "Deviations" below).

### Gap Matrix

| Concern | Existing | Target | Gap | Action |
|---|---|---|---|---|
| Finding identity | free-text claim string | deterministic `ClaimFingerprint` | full | new `ComputeClaimFingerprint` (subject+category+rule+location-bucket, never free text) |
| Evidence binding | `ChangeEvidence.TreeHash` (MeshCommit only) | `ArtifactVersion`-scoped `ReviewGroup` | `FindingPayload` itself has none | new, minimal, not coupled to MeshCommit |
| Review status | 6-value conversational `FindingStatus` | +6 verdict values | additive | extended enum, reused, no parallel machine |
| Correlation | `DeduplicateFindings` heuristic (file+line+text) | deterministic fingerprint + artifact version | existing is display-only, no identity concept | new `FindGroup`, independent of `DeduplicateFindings` |
| Arbiter | none | `Arbiter` interface, Fake+LLM impl | full | new, mirrors `Supervisor`'s proven pattern |
| Independent review | none | `IsIndependentObservation` + derived aggregates | full | new; never majority voting |
| Conflict handling | none | deterministic `contested` via `DeriveStatus` | full | new |

## Finding/claim identity

`ComputeClaimFingerprint(ClaimInput{Subject, Category, FindingType, Line})`
combines normalized subject (repo+path), category, optional rule/analyzer
ID, and a 5-line location bucket - via SHA-256, never free-text
similarity. "SQL injection in auth.go:42" and "Unsanitized query
construction in auth.go:42" correlate (same subject/category/bucket,
different wording); "auth.go:42" and "auth.go:87" never blindly merge
(different bucket). `FindGroup` additionally requires an exact
`ArtifactVersion` and `Tenant` match - two claims that would otherwise
fingerprint identically never join if they're about different code
versions or different tenants.

## Review Groups

`ReviewGroup{ID, Claim, Subject, ArtifactVersion, Tenant, Status,
Observations, ArbitrationRounds, EvidenceRequests}`. Each
`FindingObservation` carries `SourceAgentID`, `SourceExecutionID`,
`ArtifactVersion`, `EvidenceRefs`, `ConsumedFindingIDs`, and `Stance` -
provenance fields that are never derived from an Arbiter's own output
(the wire schema for Arbiter actions has no field that could set any of
these; observations only ever enter the system via `ReviewState.Ingest`,
called by Core/an authenticated caller, exactly as
`internal/mcp/server.go`'s existing `peer.challenge` handler already
guards against challenger-identity spoofing for the conversational
finding flow).

## Independence semantics

`IsIndependentObservation` is the single function everything else
builds on: an observation counts as independent of a group's existing
observations only if (a) its `SourceAgentID` is new to the group, and
(b) its `ConsumedFindingIDs` does not reference any observation already
in the group. `IndependentConfirmingObservations`/
`IndependentRefutingObservations` compute the maximal mutually-
independent subset per stance - this is what `DeriveStatus` and
`ReviewAuthorizer.verifyClaim`/`rejectClaim` actually count, never a raw
observation count. A reviewer that "consumed another agent's conclusion"
(read its finding and merely agreed) is structurally excluded from
counting toward `supported`/`verified`, even with a different agent
identity - this is how "reviewer consumed original artifact vs. merely
consumed another agent's conclusion" is modeled, per the request's
explicit ask, without adding a full input/evidence-lineage graph (judged
unnecessary complexity for what `ConsumedFindingIDs` already captures).

## Review states

Reused, additive `protocol.FindingStatus` values: `unconfirmed` (default,
<2 independent confirms, no conflict), `supported` (deterministic
agreement: >=2 independent confirms, 0 independent refutes - `DeriveStatus`,
no LLM call), `contested` (deterministic conflict: independent confirms
AND independent refutes both present), `needs_evidence` (set when a
verification/rejection attempt is authorized to run but evidence is
insufficient), `verified`/`rejected` (terminal, Core-authorized only -
`DeriveStatus` can never return either; only
`ReviewAuthorizer.verifyClaim`/`rejectClaim`, invoked from
`ReviewApplier.Apply`, can).

## Arbiter architecture

`Arbiter.Review(ctx, ArbitrationRequest) (ArbitrationProposal, error)`.
`FakeArbiter` (deterministic, test/proof double) implemented and proven
first, exactly mirroring `FakeSupervisor` before `LLMSupervisor`.
`LLMArbiter` reuses `supervisor.ModelRouter`/`InferenceTransport`
directly (no parallel model-routing stack; the existing
`SwitchyardInferenceRouter` adapter serves it with `Stage: "arbitrate"`).
`ArbitrationRequest` carries only the one relevant `ReviewGroup`, Core
limits, and bounded recent-event summaries - never other tenants' data,
full transcripts, or secrets.

## Structured-output boundary

`DecodeArbitrationResponse` mirrors `supervisor/wire.go` exactly, reusing
its already-fuzzed `DetectDuplicateKeys`/`ValidID` helpers (exported for
this purpose) rather than re-implementing them: response-size bound,
duplicate-key rejection, `DisallowUnknownFields`, trailing-data
rejection, enum validation (assessment may only be
`unconfirmed`/`supported`/`contested`/`needs_evidence` - **never**
`verified`/`rejected`, structurally unreachable via the wire schema),
bounded arrays/text, explicit `toDomainAction` wire->domain conversion
boundary.

## Authoritative verification path

`ArbiterProposal -> strict decode -> ReviewValidator -> ReviewAuthorizer
-> ReviewApplier's atomic apply`, with validate+authorize+mutate running
as **one atomic unit under ReviewState's lock** against freshly-read
state - built this way from the start, having learned from ADR 0002's
Phase 2A/2B closure review that validating against a pre-lock snapshot
creates a TOCTOU window under concurrent callers. `ReviewAuthorizer.verifyClaim`
independently re-derives the independent confirm/refute sets and the
artifact-freshness check every time, regardless of what the triggering
`RecommendVerificationAction` or the Arbiter's `Assessment` claimed.

## Evidence/artifact-version binding against stale findings

A `ReviewGroup` is permanently scoped to one `ArtifactVersion`.
`FindGroup` never joins an observation against a different version into
an existing group, however identical its fingerprint - a new group
opens instead. This is the mechanism that prevents a finding verified
against commit X from silently being treated as still verified on
commit Y: `TestIntegration_VerifyRemediateReReview` proves a v1-scoped
`verified` group and a v2-scoped `rejected` group coexist, the old
group's status is never touched by new-version activity, and a
production-readiness check that only looks at the *current* artifact
version's claims never sees the stale `v1` verification as applying to
`v2`.

## Conflict handling

`DeriveStatus` returns `contested` whenever both independent confirms and
independent refutes exist - deterministically, no "pick the stronger
model." `ReviewAuthorizer.verifyClaim`/`rejectClaim` both explicitly
refuse (`EvidenceConflict`) to move a contested claim to a terminal
verdict while *any* independent observation on the other side remains
outstanding - `TestIntegration_ConflictScenario` proves this holds even
when an Arbiter (possibly compromised or simply wrong) explicitly
proposes verification anyway.

## Review scheduling

`ToSchedulerProposal` translates a `RequestIndependentReviewAction` into
a plain `supervisor.CreateTaskProposal`, reusing Phase 3's unmodified
Scheduler/Applier/capability-based routing rather than adding a parallel
scheduling path. The Arbiter never names a concrete agent (the proposal
only carries `RequiredCapabilities`). **Independence is enforced at
verification time, not scheduling time** - a deliberate choice: Scheduler's
capability-based routing has no "excluded agent" concept, and adding one
would couple Phase 3 scheduling to Phase 4 review semantics for a
guarantee the counting algorithm (`IsIndependentObservation`) already
provides unconditionally, regardless of which agent Scheduler happens to
route a review task to.

## Remediation/re-verification flow

Proven end-to-end by `TestIntegration_VerifyRemediateReReview`:
verify (v1, 2 independent confirms) -> `ToSchedulerProposal` creates a
remediation task -> new artifact version (v2) -> independent re-review
opens a *new* `ReviewGroup` (never silently reusing v1's verified status)
-> a second independent v2 review corroborates absence -> Core authorizes
rejecting the v2-scoped claim. "Agent says fixed" alone never closes
anything - `TestNegative_RemediationAgentCannotSoleVerifyItsOwnFix` proves
a single remediating agent's own observation, even with the right stance,
cannot alone authorize the determination; a second independent agent is
required, exactly mirroring the verification threshold.

## Concurrency/idempotency guarantees

`ReviewState`/`ReviewApplier.Apply` hold one lock across validate,
authorize, and mutate - proven race-safe for concurrent `Ingest` on the
same claim (no lost updates, exactly-once group creation), concurrent
`Apply` on the same group (counters never lose an increment), and
`Ingest` racing against `Apply` on the same group (no corruption, no
duplicate observations) - all under `-race`. Duplicate observation IDs
(replay) are rejected at `Ingest` (`FindingCorrelationFailure`) and never
inflate the independent-confirmation count, whether delivered once or
five times.

## Security findings discovered during implementation

1. **Asymmetric verification/rejection thresholds.** The first
   implementation required 2 independent confirmations to verify a claim
   but only 1 independent refutation to reject one. This directly
   contradicted the explicit requirement that a remediating agent cannot
   be the *sole* authority verifying its own fix -
   `TestNegative_RemediationAgentCannotSoleVerifyItsOwnFix` caught it
   immediately (a single refuting observation was enough to "reject" the
   original claim). **Fixed**: `MinIndependentRefutationsForRejection`
   raised from 1 to 2, matching `MinIndependentConfirmationsForVerification`.
2. **Dead defense-in-depth code.** `checkArtifactFreshness` was
   originally unreachable: `IsIndependentObservation` already filtered
   out version-mismatched observations before anything reached the
   freshness check, so a hand-constructed test scenario meant to trigger
   `StaleArtifactEvidence` instead produced `EvidenceInsufficient`
   (correct, but via the wrong code path, and not exercising the
   freshness check at all). **Fixed**: independence and freshness were
   separated into distinct concerns - `IsIndependentObservation` no
   longer checks `ArtifactVersion` at all; `checkArtifactFreshness`
   (called from `verifyClaim`/`rejectClaim`) is now the sole, actually
   reachable owner of that check, giving a precise `StaleArtifactEvidence`
   error instead of a misleading `EvidenceInsufficient` one.

## Residual risks / explicitly not done this phase

- **`ReviewGroup.Status` is not synced back onto a live
  `FindingPayload.Status`.** `internal/review` tracks its own
  authoritative status; wiring a verdict back onto
  `collaboration.Engine`'s finding records via its existing `Resolve`
  path is a natural next step, deliberately deferred to avoid touching
  `collaboration/engine.go`'s large, already-tested surface without the
  time budget to fully regression-test that specific integration. The
  `protocol.FindingStatus` vocabulary was extended now specifically so
  that integration, when it happens, needs no further enum changes.
- **`MaxReviewDepth`, `MaxIndependentReviews`, `MaxReviewCost` are
  declared in `ReviewLimits` but not yet enforced anywhere** - only
  `MaxArbitrationRounds` and `MaxEvidenceRequests` are. Disclosed rather
  than silently left incomplete, matching the same honest pattern
  `supervisor.SupervisorLimits.MaxCost` used in earlier phases before its
  enforcement was wired up.
- **`EvidenceRefs` are opaque strings, never validated against a real
  evidence store** - "forged evidence reference" is not currently
  detectable, since there is no evidence-content store this package
  checks against. A real integration would validate these against
  `protocol.ChangeEvidence`/`EvidencePayload` records.
- **`ReviewState.Snapshot()` returns every group across all tenants**,
  with no tenant-filtering parameter. Not currently a live leak (no code
  path feeds a full `Snapshot()` to an `Arbiter` - `ArbitrationRequest`
  only ever carries one `ReviewGroup`), but a careless future caller
  could create one. Flagged for Phase 5 if a broader "list reviews for
  tenant X" use case emerges.
- **Replaying an already-terminal action is a benign no-op that still
  consumes an `ArbitrationRounds` slot.** Low severity (the group is
  already terminal; nothing further needs to happen on it), not fixed.

## Security model (summary)

| Concern | Status |
|---|---|
| Authority bypass (Arbiter mutates state directly) | Structurally impossible - no `ArbiterAction` carries a status field |
| Evidence/identity spoofing via Arbiter output | Impossible - wire schema has no agent-identity or evidence-content field |
| Majority voting treated as proof | Explicitly absent - `DeriveStatus`/`verifyClaim` count only mutually-independent observations |
| Self-review / self-verification | Structurally excluded from all counted aggregates (`IsIndependentObservation`) |
| Stale artifact carried forward | Blocked by `ArtifactVersion`-scoped `ReviewGroup`s + `checkArtifactFreshness` |
| Cross-tenant correlation | Blocked by exact `Tenant` match in `FindGroup` |
| Replay inflating support count | Blocked - duplicate observation IDs rejected at `Ingest` |
| Prompt injection | Inert - Arbiter output is strictly decoded, never free-text-parsed for control flow |
| Concurrency races | Proven race-free under `-race` for Ingest/Apply/concurrent-claim scenarios |
| Unbounded arbitration/evidence-request loops | Bounded (`MaxArbitrationRounds`/`MaxEvidenceRequests`); `MaxReviewDepth`/`MaxIndependentReviews`/`MaxReviewCost` declared, not yet enforced (disclosed above) |

## Final invariants (proven by the test suite)

```text
Agent agreement is not authority.           -> DeriveStatus never returns verified/rejected
Arbiter output is not authority.             -> ReviewAuthorizer independently re-derives every verdict
Evidence and policy determine verdicts.      -> verifyClaim/rejectClaim, not Assessment
A claimant cannot self-verify.               -> IsIndependentObservation excludes same-agent
A remediator cannot be sole verifier.        -> TestNegative_RemediationAgentCannotSoleVerifyItsOwnFix
Artifact-version binding prevents staleness. -> TestIntegration_VerifyRemediateReReview
Switchyard selects models.                   -> LLMArbiter reuses supervisor.ModelRouter unchanged
HarnessMesh selects agents.                  -> ToSchedulerProposal defers to Scheduler unchanged
HarnessMesh Core owns review/assurance state. -> ReviewApplier is the only mutator
LLM output remains proposal, never authority. -> every ArbiterAction passes Validator+Authorizer
```
