# ADR 0002: HarnessMesh Supervisor Agent with Deterministic Control Plane

## Status
Phase 1, Phase 2A, and Phase 2B implemented, verified, and closure-reviewed
(adversarial security/architecture review; see "Closure review" below). Three
real defects found during that review have been fixed and are covered by new
regression/concurrency tests. Phase 3 ("Controlled Multi-Agent Coordination" -
dependency-aware scheduling, capability-based agent routing, bounded parallel
execution, event-driven replanning, Core-derived workflow completion) is now
also implemented and verified - see "Phase 3" below. The original migration
plan's "Phase 3 (MCP exposure)" and "Phase 4 (Coordinator/Arbiter split)" are
renumbered Phase 4 and Phase 5 accordingly and remain not started.

Phase 4 ("Evidence-driven Arbitration and Independent Review") is also
implemented and verified, documented in its own ADR -
[ADR 0003](0003-evidence-driven-arbitration-and-independent-review.md) -
rather than overloading this document further, given its size (a new
package, a new untrusted-output decode boundary, and a new multi-agent
independence concern). It is a new participant layered on top of this
document's Supervisor/Scheduler, not a revision of anything in it.

Phase 5 ("Authoritative Evidence and Review State Closure") closes the
foundation gaps ADR 0003 left open (evidence-reference validation, real
review-limit enforcement, Finding-status projection, full provenance,
stale/tampered/cross-tenant evidence rejection) - documented in
[ADR 0004](0004-authoritative-evidence-and-review-state-closure.md).

Phase 6 ("Agent Performance and Decision Intelligence Foundation") adds a
read-only observation plane over agent/model/outcome data - it makes no
routing decision and changes no routing behavior. Documented in
[ADR 0005](0005-agent-performance-and-decision-intelligence-foundation.md).

Phase 7 ("Production Decision Wiring and Explainable Agent Advice") wires
ADR 0005's observation plane into real production executions/reviews
(Phase 7A), then introduces a strictly advisory, non-authoritative
`AgentDecisionAdvisor` with explicit `disabled`/`shadow`/`advisory`
rollout modes (Phase 7B). Phase 7C is a closure phase that fixes four
remaining integrity gaps: exact (not heuristic) review-to-execution
attribution, Core-owned execution identity, tenant filtering before the
advisor ever sees a candidate, and observer-failure visibility. All of
Phase 7 is documented in
[ADR 0006](0006-production-decision-wiring-and-explainable-agent-advice.md).

Phase 8 ("Joint Agent-Model Decision Intelligence") makes Agent, Model,
and Outcome data jointly queryable - pair aggregation over
`(Agent, TaskClass, Route/Model)` cohorts, multi-model execution
semantics, and a bounded, evidence-only model-outcome feedback boundary
- without creating a joint routing authority: HarnessMesh continues to
select agents, Switchyard continues to select models, and no
`JointRouter.SelectAgentAndModel(...)` exists. Documented in
[ADR 0007](0007-joint-agent-model-decision-intelligence.md).

Phase 9 ("Controlled Exploration and Routing Evaluation") adds a
Champion/Challenger evaluation plane - shadow evaluation, offline
replay (time-faithful and retrospective), and manual-promotion gates -
so routing policies can be compared safely without ever gaining
production authority: Challenger recommendations and offline replay can
never reach the real Executor/ExecutionProvider, and no promotion gate
can mutate the active routing policy. Documented in
[ADR 0008](0008-controlled-exploration-and-routing-evaluation.md).

Phase 9C ("Production Evaluation Wiring and Policy Identity Closure")
wires Phase 9's evaluation plane into the real production selection/
execution/finalization path automatically: Scheduler now captures a
Core-owned `CandidateSnapshot` and Champion policy provenance at every
evaluation-eligible decision, Challengers are evaluated and outcomes
attached automatically via Core-owned `ExecutionID` correlation, and
policy identity is deterministically fingerprinted and conflict-
checked. Still no challenger execution, no exploration, no automatic
promotion. Documented in
[ADR 0009](0009-production-evaluation-wiring-and-policy-identity-closure.md).

Phase 9D ("Decision-Scoped Evaluation Persistence") closes Phase 9C's
last persistence-identity gap: `CandidateSnapshot`/evaluation
persistence is now keyed by `DecisionID`, not `(Tenant, TaskID)`, so
multiple legitimate routing decisions for the same task (retries,
reselections) each get their own immutable, independently-auditable
candidate context and evaluation set. `TaskID` remains a queryable
grouping attribute, never a decision's identity. Still no challenger
execution, no exploration. Documented in
[ADR 0010](0010-decision-scoped-evaluation-persistence.md).

## Context

Depends on [ADR 0001](0001-agent-control-plane-switchyard-model-routing.md): the
Supervisor described here is a new *participant* in the already-existing control
plane (`internal/economy` + `collaboration.Engine`), not a new control plane.

The request: add an LLM-backed Supervisor/Coordinator that can plan, decompose goals
into tasks, propose agent assignments, replan on events, and synthesize multi-agent
review results — while HarnessMesh Core remains authoritative for authorization,
policy, capability validation, execution, evidence, and assurance. The Supervisor
must never self-authorize, self-grant capabilities, or execute privileged actions
directly.

## Problem

HarnessMesh already has strong propose/authorize separation for two narrow domains:

- **Change execution** (MeshCommit): `requireWritableExecutor`, `SelfReviewForbiddenError`,
  `PolicyDowngradeForbiddenError`, immutable per-change `ProofObligation` policy lock.
- **Decisions**: `Decision` propose -> accepted/rejected/superseded lifecycle.
- **Findings**: `FindingPayload`/`ChallengePayload`/`ResolutionPayload` with a
  `ResolutionRequiresHuman` escape hatch.
- **Expensive/sensitive actions**: `ApprovalRequest`/`ApprovalStatus`/`ApprovalRequiredError`.

What is missing entirely: a **task graph / decomposition** concept. There is no
`Task`, `TaskGraph`, or dependency type anywhere in the repository. The Supervisor's
core job (turn one goal into N tasks with dependencies, propose agent assignments per
task, replan on failure) has no existing structure to attach to — this is genuinely
new domain modeling, not extraction of existing code.

## Decision

The Supervisor is modeled as **an untrusted proposer**, structurally identical in
trust level to how MeshCommit already treats any change-proposer: it produces typed,
schema-validated proposals; Core validates, authorizes, and executes; nothing the
Supervisor emits is trusted or acted on until it passes the same kind of gate
MeshCommit already enforces for changes.

```
Untrusted                          Trusted (HarnessMesh Core)
┌─────────────────────┐            ┌──────────────────────────────┐
│ Supervisor (LLM)     │  proposal  │ internal/supervisor/validate  │
│ internal/supervisor  │ ─────────> │  - schema/enum/ID validation  │
│   Plan/Replan/       │            │  - capability existence check │
│   Synthesize         │            │  - task existence check       │
└─────────────────────┘            │  - dependency-cycle check     │
                                    └───────────────┬────────────────┘
                                                     │ validated proposal
                                                     ▼
                                    ┌──────────────────────────────┐
                                    │ existing authorization gates  │
                                    │  economy.SelectParticipant    │
                                    │  requireWritableExecutor-style│
                                    │  creditguard.CheckParticipant │
                                    │  ApprovalRequest (if needed)  │
                                    └───────────────┬────────────────┘
                                                     │ allow
                                                     ▼
                                           Agent execution (existing)
```

### New domain types (new package: `internal/supervisor`)

Only the task-graph layer is new. Everything else reuses existing types.

```go
type TaskID string

type Task struct {
    ID                   TaskID
    Type                 string              // e.g. "security-review"
    RequiredCapabilities []string            // matched against config.AgentCapabilities / roles, not a new Capability type
    Dependencies         []TaskID
    Risk                 economy.TaskTier    // reuse existing tier, do not invent a parallel RiskLevel
}

type TaskGraph struct {
    Tasks []Task
}

// ProposalType mirrors protocol.MessageType's existing enum pattern.
type ProposalType string

const (
    ProposalCreateTask     ProposalType = "create_task"
    ProposalAssignTask     ProposalType = "assign_task"
    ProposalRequestReview  ProposalType = "request_review"
    ProposalRequestEvidence ProposalType = "request_evidence"
    ProposalRetryTask      ProposalType = "retry_task"
    ProposalReplan         ProposalType = "replan"
    ProposalSynthesize     ProposalType = "synthesize"
    ProposalEscalate       ProposalType = "escalate"
)

type AgentProposal interface {
    ProposalType() ProposalType
}

type AssignTaskProposal struct {
    TaskID           TaskID
    CandidateAgentID string   // must resolve to a live collaboration.Engine participant
    RequiredCaps     []string
    RationaleSummary string   // short, auditable - never raw chain-of-thought
}
```

`AssignTaskProposal` deliberately does **not** carry a model/provider field — model
selection remains entirely inside Switchyard/`modelrouting`, reached only after Core
assigns the task to an agent (per ADR 0001). The Supervisor cannot request a model.

### Supervisor interface

```go
type Supervisor interface {
    Plan(ctx context.Context, goal Goal, state ExecutionState) ([]AgentProposal, error)
    Replan(ctx context.Context, state ExecutionState, event ExecutionEvent) ([]AgentProposal, error)
    Synthesize(ctx context.Context, state ExecutionState) (Synthesis, error)
}
```

`ExecutionState` is a **minimized context projection**, not the full repository or
full agent registry: goal, the relevant task subgraph, a filtered slice of the agent
registry (capability-matching candidates only), active policy constraints, an
evidence summary, and recent events. Built the same way `contextpack` already builds
minimized context for other consumers (internal/contextpack) — reused, not
reinvented.

### Authorization boundary (what Core enforces, mirroring MeshCommit exactly)

| MeshCommit precedent | Supervisor equivalent |
|---|---|
| `requireWritableExecutor` | proposals never execute directly; every `AssignTaskProposal` still goes through `economy.Controller.SelectParticipant`'s existing capability/writable/sensitivity filters before any agent runs |
| `SelfReviewForbiddenError` | Supervisor cannot be the assigned reviewer/executor of a task it proposed review for (new check, same shape) |
| `PolicyDowngradeForbiddenError` | Supervisor proposals cannot alter a task's locked capability requirements once set; a `ReplanProposal` can only add tasks/dependencies, never remove required capabilities |
| Immutable `ProofObligation` policy snapshot | task graph, once Core-validated, is immutable except via a new, equally-validated `ReplanProposal` |
| `ApprovalRequest` | any proposal whose resolved agent assignment would need human approval (per existing `ApprovalRequiredError` conditions) still requires it — Supervisor cannot pre-approve |
| `creditguard.CheckParticipant` | unaffected — applies identically regardless of who (human, Supervisor) proposed the assignment |

A malformed or policy-violating proposal is **rejected closed** with a typed error
(new `ProposalValidationFailure`, `PolicyRejection` reusing existing
`protocol.ClassifyError` taxonomy) — never silently dropped or partially applied.

### Guardrail limits (Core-owned, not Supervisor-owned)

```go
type SupervisorLimits struct {
    MaxTaskDepth      int
    MaxAgentFanout    int
    MaxReplanCount    int
    MaxReviewRounds   int
    MaxExecutionTime  time.Duration
    MaxCost           float64 // reuses economy's existing cost units
}
```

Enforced inside the validation boundary (`internal/supervisor/validate.go`), read
from `config.Config`, never from Supervisor output. `MaxReplanCount` directly answers
the "infinite replanning" threat; `MaxAgentFanout`/`MaxTaskDepth` answer "recursive
delegation loop."

### Multi-agent review / Finding states

Reuse `protocol.FindingPayload` as-is; add the requested richer status set by
extending (not replacing) its existing `FindingStatus` enum:
`unconfirmed, supported, contested, verified, rejected, needs_evidence` — alongside
the current `Open/Acknowledged/Disputed/Resolved/Dismissed/Superseded`. The Supervisor
may propose a status transition (`RequestReviewProposal`,
`RequestEvidenceProposal`); only Core (via the existing `peer.resolve` /
`ResolutionPayload` path, including its `ResolutionRequiresHuman` escape hatch) can
actually move a Finding to `verified` or `rejected`.

### Structured output validation

Supervisor output is JSON matching the proposal types above. Validation performed
by Core before anything is acted on:
1. JSON schema validity
2. enum values are known (`ProposalType`, task `Risk`)
3. referenced `TaskID`s exist in the current graph (or are being created in the same
   batch, with cycle detection)
4. referenced `CandidateAgentID` exists in the live `collaboration.Engine` registry
5. referenced capabilities exist in `config.AgentCapabilities`'s known set
6. dependency graph is acyclic

Any failure -> `ProposalValidationFailure`, fail closed, no partial execution.

### No hidden chain-of-thought persisted

Only `RationaleSummary` (bounded length, e.g. <= 500 chars) is stored per proposal,
alongside: proposal id, task id, agent id, timestamp, supervisor version,
model/provider metadata (from whichever ModelRoutingDecision served the Supervisor's
own inference turn — the Supervisor is itself just another Switchyard-routed caller),
input state reference (hash, not full payload), validation result, policy result,
execution outcome. This mirrors `ChangeEvidence`'s existing evidence-record shape.

## Gap Matrix

| Concern | Existing | Target | Gap | Action |
|---|---|---|---|---|
| Supervisor | none | `internal/supervisor.Supervisor` interface + impl | full | new package |
| Task planning / decomposition | none (confirmed: zero `Task`/`TaskGraph`/DAG code anywhere) | `Task`, `TaskGraph` types | full | new types in `internal/supervisor` |
| Proposals | `Decision` (propose/accept) is close but domain-specific to decisions, not task assignment | `AgentProposal` union | partial | new types, but validation reuses `protocol.ClassifyError`/existing error taxonomy patterns |
| Core authorization | `requireWritableExecutor`, `SelfReviewForbiddenError`, `PolicyDowngradeForbiddenError`, `ApprovalRequest` | same gates, generalized to proposal execution | small | add proposal-shaped entry points into existing gates, don't replace them |
| Replanning | none | `Replan()` + `MaxReplanCount` guardrail | full | new, but event taxonomy reuses existing error/finding types where possible (agent failure, policy rejection already have typed errors) |
| Evidence | `ChangeEvidence`, `Decision.EvidenceRefs` | same, extended to cover proposal audit trail | small | add proposal audit record shaped like `ChangeEvidence` |
| Model routing | `modelrouting.ModelRoutingBackend` / Switchyard (ADR 0001) | Supervisor's own inference routed the same way | none | Supervisor is just another caller; no new model-routing code |
| Security (self-authorization) | MeshCommit precedent (`SelfReviewForbiddenError`, `requireWritableExecutor`) | same pattern applied to proposals | small | extend existing checks, don't duplicate |
| Context minimization | `internal/contextpack` | Supervisor context built via same package | none expected, verify at implementation time | reuse `contextpack` |
| ADR convention | none before ADR 0001 | `docs/decisions/` | closed by ADR 0001 | n/a |

## Rejected alternatives

- **God-agent**: Supervisor executes directly, skipping Core. Rejected per explicit
  requirement and because it would bypass every existing authorization primitive
  (`creditguard`, `requireWritableExecutor`, `ApprovalRequest`) this repo already
  relies on.
- **New parallel authorization stack specific to the Supervisor.** Rejected:
  MeshCommit's authorization pattern already does exactly what's needed
  (propose -> validate -> gate -> execute, fail closed); duplicating it for the
  Supervisor would create two authorization code paths to keep in sync and audit.
- **Model the Supervisor's proposals as a new, separate `Decision`-like SQLite table.**
  Rejected for v1: proposals are higher-frequency and more ephemeral than
  `Decision` records (which are meant to be a durable team-visible ledger); proposals
  get a lighter-weight audit record instead, promoted to a `Decision` only when a
  human or Core explicitly accepts a plan as binding.

## Consequences

- New package `internal/supervisor`; no existing package is modified in a breaking
  way. `protocol.FindingStatus` enum is extended (additive), not replaced.
- Core's existing selection/authorization pipeline (`economy.Controller`,
  `requireWritableExecutor`-style checks) gains new call sites for
  proposal-validated task assignment, but its decision logic is unchanged — a
  Supervisor-proposed assignment goes through the identical filter pipeline a
  human- or config-driven assignment would.
- Supervisor/Arbiter split (requested as future-ready, not now): `Supervisor`
  interface's three methods (`Plan`/`Replan`/`Synthesize`) are deliberately already
  separable — `Synthesize` (cross-agent comparison/conflict resolution) could later
  move to a distinct `Arbiter` interface without changing `Plan`/`Replan`'s contract.

## Security implications

Threat model (full detail; summarized mitigations):

| Threat | Mitigation |
|---|---|
| Prompt injection via agent result/external doc | Supervisor output is never executed directly; it is schema-validated untrusted data, same as any external MCP input |
| Agent impersonation | `CandidateAgentID` resolved against Core's live registry (`collaboration.Engine`), not trusted from Supervisor claim |
| Capability spoofing | capabilities checked against `config.AgentCapabilities`'s declared set, never against Supervisor-asserted capabilities |
| Malicious/malformed proposal | `ProposalValidationFailure`, fail closed |
| Tool confused deputy | Supervisor has no direct tool-calling path; all execution still flows through the agent the task is assigned to, under that agent's own existing capability grant |
| Cross-agent/tenant data leakage | `ExecutionState` built via `contextpack` minimization + existing tenant/scope filters, not raw repository/tenant dump |
| Evidence poisoning | agent results are not auto-verified; `FindingStatus` only reaches `verified`/`rejected` via existing `peer.resolve` human-capable gate |
| Recursive delegation / infinite replanning | `MaxTaskDepth`, `MaxAgentFanout`, `MaxReplanCount`, `MaxReviewRounds` enforced in Core's validation boundary, read from config, not from Supervisor |
| Resource exhaustion | `MaxExecutionTime`, `MaxCost` guardrails, reusing economy's existing cost accounting |

## Operational implications

New observability needed (additive): planning duration, proposal count/rejection
rate, task decomposition size, replan count, review cycles, synthesis duration —
emitted via whatever telemetry package `internal/telemetry` already uses, with the
same "no secrets/full prompts" discipline docs/mcp.md and docs/switchyard.md already
enforce for existing telemetry.

## Migration strategy

Phased, no big-bang:

1. **Phase 1** — `internal/supervisor` domain types (`Task`, `TaskGraph`,
   `AgentProposal` union, `SupervisorLimits`) + a pure-Go, no-LLM
   `ProposalValidationFailure`/validation boundary, fully unit tested (schema
   validity, unknown agent, unknown task, invalid capability, malformed proposal,
   unauthorized proposal, cycle detection, guardrail limits). No LLM involved yet —
   proves the trust boundary independent of any real Supervisor implementation
   (a deterministic `TestSupervisor`/`FakeSupervisor` stands in).
2. **Phase 2A** — the deterministic proposal-application and execution-wiring
   layer, still entirely model/provider-independent: a bounded `Applier`
   (validate -> authorize -> atomic apply, as three separate steps - a validated
   proposal never implies authorization), a `CoreState` task-graph store with
   atomic, all-or-nothing batch application, an `Executor` that enforces
   `MaxExecutionTime` and re-validates agent existence/capabilities/tenant against
   the *live* `AgentDirectory` immediately before dispatch (TOCTOU mitigation), and
   a real `AgentDirectory` adapter wired to `collaboration.Engine`'s configured
   agents - still driven end-to-end by `FakeSupervisor` and a fake
   `ExecutionProvider`, never a real model.
3. **Phase 2B** — wire a real `Supervisor` implementation that calls out through
   `modelrouting`/Switchyard for its own inference, producing real structured
   proposals, replacing `FakeSupervisor` and `FakeExecutionProvider` with real
   implementations behind the same `Supervisor`/`ExecutionProvider` interfaces
   Phase 2A already proved the trust boundary against.
4. **Phase 3** — "Controlled Multi-Agent Coordination": dependency-aware
   scheduling, capability-based agent routing, bounded parallel execution,
   event-driven replanning, Core-derived workflow completion. See the "Phase 3"
   section below.
5. **Phase 4** — expose Supervisor proposals through MCP (new `supervisor.*` tool
   group, following the exact pattern `change.*`/`peer.*` already use) so external
   clients can request planning without a new protocol stack.
6. **Phase 5** — Coordinator/Arbiter split, only if a real need for independent
   scaling or independent trust levels between the two roles emerges.

## Phase 1 implementation record

Implemented in `internal/supervisor` (zero non-stdlib imports): `Task`/`TaskGraph`,
the 8-member `AgentProposal` union, `Supervisor`/`FakeSupervisor`,
`AgentDirectory`, `SupervisorLimits`, and `Validator.ValidateBatch` (fail-closed,
all-or-nothing). 32 tests, all passing under `-race`, 71.2% statement coverage;
full-repo `gofmt`/`go build`/`go vet`/`go test -race` all clean with zero
regressions. Deviations: `MaxExecutionTime` was declared but deferred to Phase 2A
(it is an execution-boundary, not validation-boundary, concern);
`protocol.FindingStatus`'s richer status set was deliberately not touched, since it
sits outside Phase 1's own scope and `collaboration/engine.go` has live `switch`
statements over it.

## Phase 2A implementation record

Added to `internal/supervisor`: `authorize.go` (`Authorizer` interface,
`AllowAllAuthorizer`, `CoreAuthorizer`), `state.go` (`CoreState`, `Applier`),
`execute.go` (`Executor`), plus `Task.Status`/`Task.Tenant`, `TaskStatus`,
`TenantDirectory`, `ExecutionResult`, `ExecutionProvider`/`FakeExecutionProvider`,
and six new typed errors (`PolicyRejection`, `AuthorizationFailure`,
`StateTransitionFailure`, `AgentRoutingFailure`, `AgentExecutionFailure`,
`ExecutionTimeout`) alongside the renamed `ReplanLimitExceeded` (dropped the
`Error` suffix for naming consistency with the others). Added to
`internal/collaboration`: `supervisor_adapter.go` (`SupervisorAgentDirectory`,
implementing `supervisor.AgentDirectory` over `Engine.cfg.Agents` - the only new
cross-package dependency, and it runs one way: `collaboration` imports
`supervisor`'s interface, `supervisor` imports nothing). The adapter deliberately
does not implement `TenantDirectory`: `internal/collaboration` has no tenant
concept yet, so tenant checks are a documented no-op until one exists.

21 new tests (14 in `internal/supervisor`: `apply_test.go`, `execute_test.go`,
`integration_test.go`; 2 in `internal/collaboration`: `supervisor_adapter_test.go`),
all passing under `-race`. Full-repo verification re-run clean with zero
regressions. Key proofs: a validated batch that fails `CoreAuthorizer` mutates
nothing (`TestApply_ValidatedProposalIsNotImplicitlyAuthorized`); a batch with one
valid and one invalid proposal applies neither
(`TestApply_AtomicBatchNoPartialApplication`); a failed persistence hook leaves
`CoreState` byte-for-byte unchanged (`TestApply_FailedPersistenceLeavesStateUnchanged`);
an agent removed from the live directory after assignment is never dispatched to
(`TestExecute_AgentDisappearedBetweenValidationAndExecution`); the same for a
revoked capability and a changed tenant; `MaxExecutionTime` is enforced via
`context.WithTimeout` purely at the execution boundary
(`TestExecute_Timeout`); and a full
goal -> plan -> apply -> execute(fail) -> replan -> remediate -> re-execute ->
synthesize cycle runs deterministically with `FakeSupervisor` and
`FakeExecutionProvider` (`TestIntegration_GoalToSynthesisWithRemediation`).

Phase 2B (a real LLM-backed `Supervisor` routed through `modelrouting`/Switchyard)
was explicitly not started at the time the Phase 2A record above was written. It is
now implemented - see below.

## Pre-implementation mismatch found between ADR 0001 and the actual code

Before writing any Phase 2B code, `internal/modelrouting` was re-inspected
specifically for an inference-call path. **Finding: `modelrouting.ModelRoutingBackend`
has no such path.** Its methods (`Type`, `Health`, `ConfigureParticipant`,
`RouteMetadata`, `Usage`, `Diagnostics`) only *configure a harness subprocess's
environment* (`ConfigureParticipant` sets `OPENAI_BASE_URL`/`ANTHROPIC_BASE_URL` on
`config.AgentConfig.Env`) - the actual HTTP call to an LLM is made later, by that
subprocess (Claude Code, Codex, etc.), never by HarnessMesh's own Go code. ADR
0001's conceptual diagram (`Supervisor -> ModelRouter -> Switchyard -> model -> LLM
-> structured output`) implicitly assumes a Go-callable "give me a completion"
interface exists. It does not, because every other HarnessMesh participant is an
external subprocess harness, not a Go-native caller.

This is not a defect in ADR 0001 - its scope was agent/model routing *selection*,
which `modelrouting.ModelRoutingBackend` does cover correctly - but it is a gap for
a Supervisor that needs to call an LLM directly from Go. Resolved additively: a new
`SwitchyardInferenceRouter` (`internal/modelrouting/supervisor_adapter.go`) wraps an
existing `*SwitchyardBackend` and adds exactly one new capability, a plain
`net/http` POST to Switchyard's already-OpenAI-compatible surface
(`{BaseURL}/v1/chat/completions` - the same base URL `ConfigureParticipant` already
points subprocesses at). No parallel model-routing stack, no provider SDK, and
`ModelRoutingBackend` itself is untouched.

## Phase 2B design

```
LLMSupervisor (internal/supervisor, zero provider-SDK imports)
        |  ModelRouter interface              <- decides nothing itself
        v
SwitchyardInferenceRouter (internal/modelrouting, depends on supervisor's interfaces)
        |  plain net/http POST, OpenAI-compatible wire format
        v
SwitchyardBackend.BaseURL + "/v1/chat/completions"
        |
        v
Switchyard resolves the route alias to a concrete provider/model (opaque to HarnessMesh)
```

**Dependency direction** mirrors Phase 2A's `collaboration.SupervisorAgentDirectory`
exactly: the routing-aware package (`modelrouting`) imports `supervisor`'s
interfaces to build an adapter; `supervisor` imports nothing from `modelrouting`,
no provider SDK, and no concrete model name anywhere in its source.

**Route(), not Select()**: `ModelRoutingDecision.Model` is a *route alias*
(e.g. `"supervisor-plan"`, or `"supervisor"` as the default), never a concrete model
name. `SwitchyardInferenceRouter.Routes map[string]string` lets an operator map each
stage (`plan`/`replan`/`synthesize`) to whichever Switchyard route they've
configured; HarnessMesh code never sees or chooses the model Switchyard resolves
that route to. The only place a real model identity appears is
`InferenceResult.ServedModel`, echoed back from the provider's own response purely
for observability (`TestSwitchyardInferenceRouter_Complete_CallsOpenAICompatibleEndpoint`
asserts this is never fed back into routing).

**Wire trust boundary** (`internal/supervisor/wire.go`): `WireProposal`/`WireTask`/
`WirePlanResponse`/`WireSynthesisResponse` are the only shapes LLM JSON is ever
decoded into. `decodeStrict` enforces, in order: response-size bound, no duplicate
JSON keys at any nesting depth (`detectDuplicateKeys`, a hand-rolled token-stream
walker since `encoding/json` lets a repeated key silently overwrite the first),
`json.Decoder.DisallowUnknownFields()`, and a trailing-data check
(`dec.More()`). Every proposal is then converted by `toDomainProposal` - the
explicit, individually testable wire-to-domain boundary required by the
instructions - which re-validates IDs against `idPattern`
(`^[A-Za-z0-9_.:-]{1,128}$`), rejects unknown `ProposalType` values, and bounds
every array (`wireLimits`: 32 proposals/batch, 16 capabilities/task, 64 task IDs in
a synthesis, etc.). Nothing is ever "repaired" - every violation becomes
`StructuredOutputDecodeFailure` (syntactic) or `StructuredOutputSchemaFailure`
(semantic), both fail-closed, both leaving the proposal list `nil`.

**Context minimization**: `buildPlanPrompt`/`Synthesize` construct only goal text,
a bounded task-graph slice (id/type/status/risk/required_capabilities - never
unrelated repository content, secrets, env vars, or other tenants' data), the
triggering `ExecutionEvent` (Replan only), `SupervisorLimits` plus the current
replan/review counters (so the model can reason about headroom, though only Core
enforces the actual limits), and an optional, explicitly minimized
`AgentContextProvider.RelevantAgents` slice (id + capabilities only). Total prompt
size is bounded by `MaxContextBytes` (`SupervisorContextTooLarge` if exceeded,
checked before any inference call).

**Prompt-injection handling**: the fixed system prompt
(`systemPromptTemplate`) states that everything under `context:` is DATA, never an
instruction, even if phrased as one, and that only the system prompt and JSON
schema define allowed output. This is explicitly documented in the same prompt text
as a *courtesy*, not the security boundary - `TestSecurity_LLM_PromptInjectionInGoalCannotEscapeSchema`
proves the real boundary holds even when a "model" (a fake, in the test) echoes an
injected instruction back as a seemingly normal, schema-valid proposal: the
*existing, unmodified* `Validator`/`CapabilityMismatchError` gate still rejects it,
because `codex` was never actually granted `write_repository` in the directory -
the proposal's claim is irrelevant.

**Inference failure semantics**: `ModelRoutingFailure` (Switchyard/routing couldn't
resolve a destination) is kept structurally distinct from `InferenceFailure` (routing
succeeded, the call itself failed) and from `AgentExecutionFailure`/`AgentRoutingFailure`
(Phase 2A, unchanged - an *agent's* execution failing is never confused with a
*model's* inference call failing). None of the three ever becomes an implicit
success: `LLMSupervisor.Plan/Replan/Synthesize` return early with the typed error
and a `nil` proposal list/zero `Synthesis` the moment any step fails.

**No hidden reasoning**: the wire schema has no field for chain-of-thought: only
`rationale_summary` (bounded to `MaxRationaleLength`, same constant Phase 1 defined).
The system prompt never asks the model to show its reasoning, only to justify each
proposal briefly for the audit trail.

**Tenant limitation (unchanged from Phase 2A, re-verified, and hardened)**:
`internal/collaboration` still has no tenant model; `SupervisorAgentDirectory` still
does not implement `supervisor.TenantDirectory`. During Phase 2B review this was
found to be enforced *too loosely* in Phase 2A: both `CoreAuthorizer.Authorize` and
`Executor.Execute` silently skipped the tenant check whenever the configured
`AgentDirectory` didn't implement `TenantDirectory` at all - i.e. a tenant-scoped
task against today's real directory was, incorrectly, treated as unrestricted. Fixed
in this phase (`authorize.go`, `execute.go`): a task with a non-empty `Tenant` now
fails closed with `AuthorizationFailure` if the directory cannot resolve tenancy,
both at authorization time and again at the execution boundary. Verified by
`TestApply_TenantScopedTaskFailsClosedWithoutTenantDirectory`,
`TestExecute_TenantScopedTaskFailsClosedWithoutTenantDirectory`, and
`TestApply_UnscopedTaskUnaffectedByMissingTenantDirectory` (proving ordinary,
non-tenant-scoped work is unaffected). Real multi-tenant enforcement still requires
a real `TenantDirectory`/Core tenant model that does not exist yet - this fix makes
the *absence* of that model fail safe instead of fail open.

**Observability**: `TelemetrySink` (stage name, duration, routing/served
provider+model, failure/decode-failure counts, proposal counts only - never prompt
text, model output, or rationale content) is implemented via `RegistryTelemetry`,
wrapping the repository's existing dependency-free `internal/telemetry.Registry`
rather than introducing a second metrics stack.

## Phase 2B implementation record

New in `internal/supervisor`: `modelrouting.go` (`ModelRouter`, `ModelRoutingRequest`/
`Decision`, `ModelRequirements`, `InferenceTransport`, `InferenceResult`,
`AgentContextProvider`, and their `Fake*` test doubles), `wire.go` (the strict
decode boundary described above), `llm_supervisor.go` (`LLMSupervisor`,
`LLMSupervisorConfig`, prompt construction), `telemetry.go` (`TelemetrySink`,
`RegistryTelemetry`). Six new typed errors: `ModelRoutingFailure`,
`InferenceFailure`, `StructuredOutputDecodeFailure`, `StructuredOutputSchemaFailure`,
`SupervisorResponseTooLarge`, `SupervisorContextTooLarge`. New in
`internal/modelrouting`: `supervisor_adapter.go` (`SwitchyardInferenceRouter`).
Hardening fix in existing Phase 2A files: `authorize.go`, `execute.go` (fail-closed
tenant check, see above).

39 new tests across `internal/supervisor` (`wire_test.go`, `fuzz_test.go`,
`llm_supervisor_test.go`, `integration_llm_test.go`, plus the tenant fail-closed
additions to `apply_test.go`/`execute_test.go`) and `internal/modelrouting`
(`supervisor_adapter_test.go`), all passing under `-race`. Three fuzz targets
(`FuzzDecodePlanResponse`, `FuzzDecodeSynthesisResponse`, `FuzzDetectDuplicateKeys`)
ran ~3.6M executions combined with zero crashes. `internal/supervisor` coverage:
72.6%; `internal/modelrouting`: 61.0%. Full-repo `gofmt`/`go build`/`go vet`/
`go test -race` all clean, zero regressions anywhere, including the entire Phase 1
and Phase 2A suite re-run unchanged.

Key proofs: `TestIntegration_LLMSupervisor_GoalToSynthesisWithRemediation` runs the
full goal -> plan -> apply -> execute(fail) -> replan -> remediate -> re-execute ->
synthesize cycle with `LLMSupervisor` + a fake `ModelRouter`/`InferenceTransport` in
place of `FakeSupervisor`, through the *exact same, unmodified* `Validator`/
`Authorizer`/`Applier`/`Executor` Phase 2A already proved - demonstrating that
replacing `FakeSupervisor` with `LLMSupervisor` changes nothing about Core's trust or
authorization model. `TestSecurity_LLM_*` (11 tests) prove every Phase 1/2A boundary
holds against LLM-shaped adversarial output: nonexistent agents, unauthorized
capabilities, cross-tenant assignment, self-review, attempted model/provider
selection (rejected as an unknown wire field), excessive fanout, recursive task
expansion, and infinite replanning. `TestRoutingIndependence_*` prove Supervisor
behavior is identical across different `ModelRouter` implementations.

Phase 2B remains strictly additive to Phase 2A: no existing Phase 1/2A type's
behavior changed except the tenant fail-closed fix, which tightens (never loosens)
an existing security check.

## Closure review (adversarial security/architecture review of Phases 1, 2A, 2B)

This review treated the implementation, not this document, as the source of
truth, and tried to break the primary invariant - **LLM output is untrusted
data and can never become authority** - end to end:
`LLMSupervisor -> SwitchyardInferenceRouter -> wire decode -> Validator ->
Authorizer -> Applier -> CoreState -> Executor -> ExecutionProvider`, and the
reverse path `ExecutionResult -> prompt construction -> Replan/Synthesize ->
next batch`.

### Defects found and fixed

1. **Authorization could become stale between Apply and Execute.**
   `Executor.Execute` re-checked agent existence/capabilities/tenant against the
   live `AgentDirectory` (the Phase 2A TOCTOU mitigation), but never re-ran
   `Authorizer` at all. An operator revoking an agent via `CoreAuthorizer.DeniedAgents`
   *after* a task was assigned but *before* it executed - plausible, since
   execution can happen arbitrarily later than Apply - had no effect on
   already-assigned work. **Fixed**: `Executor` now takes an `Authorizer` and
   re-runs it, reconstructing the equivalent `AssignTaskProposal`, immediately
   before `MarkExecuting`/dispatch (`execute.go`). Covered by
   `TestExecute_StalePolicyRevokedBeforeExecutionIsReAuthorized`.
2. **Concurrent Apply calls could create duplicate-ID tasks.** `Applier.Apply`
   validated against a `CoreState.Snapshot()` taken *before* acquiring
   `CoreState`'s lock; `CoreState.apply` then re-read `s.graph` fresh *inside*
   the lock but never re-checked for duplicate IDs on `CreateTaskProposal`/
   `ReplanProposal.NewTasks` - it just appended. Two goroutines each proposing
   `CreateTaskProposal{ID:"T1"}` could each pass validation against their own
   stale pre-lock snapshot (neither aware of the other), and because `apply`
   didn't re-check uniqueness, both appends could land, producing two tasks
   with the same ID. **Fixed**: validation, authorization, and mutation now run
   as **one atomic unit** under `CoreState`'s lock, against state read fresh
   inside that same critical section (`CoreState.applyGated`, replacing the old
   `apply`), and the mutation step now re-checks ID uniqueness against the
   fresh working copy. Covered by
   `TestConcurrency_DuplicateTaskIDCreationIsSerializedAndRejected` (50
   concurrent identical creations, exactly 1 succeeds) and
   `TestConcurrency_ApplyAndExecuteInterleavedNoCorruption`.
3. **`SwitchyardInferenceRouter.Complete` had no independent timeout and
   followed redirects.** It relied entirely on `r.Backend.HTTPClient.Timeout`
   (5s via the normal constructor) but fell back to `http.DefaultClient` (no
   timeout at all) if `HTTPClient` was nil, and used the default
   `http.Client` redirect policy (follow up to 10 redirects), letting a
   misbehaving or compromised Switchyard endpoint potentially hang a
   Plan/Replan/Synthesize call indefinitely or redirect the prompt to an
   unintended host. **Fixed**: every `Complete` call is now bounded by
   `defaultRequestTimeout` (60s) independent of the caller's ctx and of
   `HTTPClient`'s own configuration, and redirects are unconditionally refused
   (`CheckRedirect` denies). Covered by
   `TestSwitchyardInferenceRouter_Complete_RedirectsAreNotFollowed` and
   `TestSwitchyardInferenceRouter_Complete_CancellationPropagates`.

None of the three defects allowed LLM output to bypass `Validator`/`Authorizer`
or to acquire authority directly - all three were either an authorization
window that could go stale, or a resource-exhaustion/availability risk in the
transport layer. The core invariant held throughout; these fixes tighten
enforcement and availability, not the authority boundary itself.

### Closure matrix

| Concern | Evidence | Status | Residual Risk |
|---|---|---|---|
| 1. Trust-boundary bypass (LLM -> authority) | `TestSecurity_LLM_*` (11 tests): nonexistent agent, unauthorized capability, cross-tenant, self-review, model-selection attempt, fanout, recursive expansion, infinite replanning - all rejected by the unmodified Validator/Authorizer regardless of what the "model" proposes | PROVEN | none found |
| 2. Authority duplication (two components independently deciding the same thing differently) | Validator and Authorizer each own distinct, non-overlapping checks (structural vs. policy); Executor's TOCTOU re-checks mirror Authorizer's checks but against live state, not a second decision authority | PROVEN | none |
| 3. TOCTOU (agent/capability/tenant) | `TestExecute_AgentDisappearedBetweenValidationAndExecution`, `..._CapabilityRevoked...`, `..._CrossTenantAtExecutionBoundary` | PROVEN | none |
| 3b. TOCTOU (authorization staleness) | Defect #1 above | FIXED, now PROVEN (`TestExecute_StalePolicyRevokedBeforeExecutionIsReAuthorized`) | none |
| 4. Atomicity (single batch) | `TestApply_AtomicBatchNoPartialApplication`, `TestApply_FailedPersistenceLeavesStateUnchanged` | PROVEN | none |
| 4b. Atomicity (concurrent batches) | Defect #2 above | FIXED, now PROVEN (`TestConcurrency_DuplicateTaskIDCreationIsSerializedAndRejected`, `TestConcurrency_ConcurrentBatchesOnDisjointTasksAllSucceed`) | none |
| 5. Tenant isolation | `TestApply_CrossTenantAssignmentRejectedAtAuthorization`, `TestExecute_CrossTenantAtExecutionBoundary`, fail-closed-without-TenantDirectory tests | PROVEN for the mechanism; real multi-tenancy is NOT implemented | OPEN: `internal/collaboration` has no tenant model; `SupervisorAgentDirectory` cannot implement `TenantDirectory` until one exists. The fail-closed behavior is proven; actual tenant *semantics* are not, because there are none yet. |
| 6. Capability spoofing | `TestSecurity_LLM_UnauthorizedCapabilityRejected`, `TestSecurity_CapabilitySpoofingRejected` (Phase 1) | PROVEN | none |
| 7. Agent identity spoofing | `TestSecurity_LLM_NonexistentAgentRejectedByValidator`, `TestSecurity_ForgedAgentIdentityRejected` (Phase 1) | PROVEN | none |
| 8. State-transition integrity | `TestApply_InvalidStateTransitionRejected`, `transition()`'s atomic CAS-like `from`-status check | PROVEN | none |
| 9. Proposal replay | No explicit proposal/batch ID exists; replay is rejected structurally because every mutating proposal re-checks a precondition on current status (assign requires Pending, retry requires Failed, create requires non-existence) under the same lock as the mutation | MITIGATED by design, not by an explicit anti-replay mechanism | OPEN (low): a replayed `RequestReviewProposal`/`RequestEvidenceProposal`/`SynthesizeProposal`/`EscalateProposal` has no state-transition guard to reject it (these are audit-only, non-mutating in Phase 2A/2B) - replaying one just re-increments `ReviewRounds` (bounded by `MaxReviewRounds`, so not unbounded) or re-emits an informational event. Not a security issue today; would become one only if a future phase attaches side effects to these proposal types without adding replay protection then. |
| 10. Duplicate proposal IDs | No proposal carries an ID at all (by design - see "Alternatives/decision" below) | NOT APPLICABLE | Revisit only if Phase 3 (external MCP submission) needs idempotent retry semantics across a network boundary - a client-supplied idempotency key would belong at that boundary, not inside `internal/supervisor` |
| 11. Stale proposal execution | Same mechanism as #9: current-state preconditions are re-checked at mutation and at execution time, not inferred from when the proposal was produced | MITIGATED | none beyond #9's note |
| 12. Replayed model responses | `decodePlanResponse`/`decodeSynthesisResponse` are pure functions of their input bytes; replaying the same raw model response produces the same proposals, which then hit the same state-transition guards as any other replay (#9/#11) | MITIGATED | none |
| 13. Cross-task references | `UnknownTaskError` on any reference (dependency, assign, review, retry, synthesis) to a task not in the known set | PROVEN | none |
| 14. Cross-tenant references | see #5 | PROVEN (mechanism) | see #5 |
| 15. Confused-deputy scenarios | Executor always resolves the acting agent from `CoreState.Assignments` (Core-authoritative), never from anything the LLM's proposal carries at execution time; the LLM only ever influences which agent gets *proposed*, and that proposal is independently re-verified | PROVEN | none found |
| 16. Switchyard route-alias abuse | `decision.Model` (route alias) is produced entirely by `SwitchyardInferenceRouter.Route`, from operator-configured `Routes`/`DefaultRoute` - the LLM's JSON output is never consulted when choosing a route; wire schema rejects an LLM-supplied `"model"` field as an unknown field | PROVEN (`TestSecurity_LLM_CannotSelectConcreteModelOrProvider`) | none |
| 17. Unexpected redirects / HTTP behavior | Defect #3 above | FIXED, now PROVEN (`TestSwitchyardInferenceRouter_Complete_RedirectsAreNotFollowed`) | none |
| 18. Malformed/oversized model responses | `TestDecodePlanResponse_*` (malformed JSON, unknown fields, oversized rationale/count, truncated, duplicate keys, trailing data, malformed IDs, response-too-large) | PROVEN | none |
| 19. Decompression/resource-exhaustion | No decompression anywhere in this path; response-size cap (`MaxResponseBytes`, default 256KB) is enforced *before* any JSON parsing is attempted, bounding parser CPU/memory regardless of structure | PROVEN | none |
| 20. Context amplification | `buildPlanPrompt`/`Synthesize` bound total prompt size via `MaxContextBytes`, checked before any inference call | PROVEN | none |
| 21. Prompt injection | `TestSecurity_LLM_PromptInjectionInGoalCannotEscapeSchema`: an injected instruction, even when a (fake) model echoes it back as a seemingly valid proposal, is still rejected by the unmodified capability gate | PROVEN | none (the system prompt's "treat context as data" instruction is documented as a courtesy, not the boundary - correctly, since it is unenforceable on its own) |
| 22. Evidence poisoning | No proposal type can mark anything "verified"; agent `ExecutionResult`s are not auto-trusted - only the pre-existing `peer.resolve` path (outside this package) can move a Finding to verified | PROVEN (Phase 1 `TestSecurity_PoisonedEvidenceCannotSelfVerify`, Phase 2B wire-type field-name check) | none |
| 23. Synthesis interpreted as authority | `Synthesis`/`WireSynthesisResponse` carry no status/verified/executable field, structurally (reflection-checked) | PROVEN | none |
| 24. Persistence rollback semantics | `TestApply_FailedPersistenceLeavesStateUnchanged`: a failing `persist` hook leaves `CoreState` untouched, returning the pre-call snapshot, now re-verified under the merged `applyGated` critical section | PROVEN | none |
| 25. Cancellation and timeout behavior | `TestExecute_Timeout` (execution boundary), `TestSwitchyardInferenceRouter_Complete_CancellationPropagates` (inference transport) | PROVEN | none |
| 26. Goroutine/resource leaks | Every `context.WithTimeout` has a paired `defer cancel()`; every `http.Response.Body` is closed via `defer`; no goroutines are spawned by this package itself | PROVEN by inspection (no leak-detection tooling run) | none found |
| 27. Concurrency races | `go test -race` across the full new `concurrency_test.go` suite plus the entire existing Phase 1/2A/2B suite | PROVEN (race detector clean) | see #30 |
| 28. Inconsistent authorization between Apply and Execute | Defect #1 | FIXED, now PROVEN | none |
| 29. Agent mutation between validation/authorization/execution | `TestExecute_AgentDisappearedBetweenValidationAndExecution`, `..._CapabilityRevoked...` | PROVEN | none |
| 30. Policy mutation between authorization/execution | Defect #1 closes the *temporal* gap (re-authorization happens). However, `CoreAuthorizer.DeniedAgents`/`DeniedCapabilities` are plain `map[...]bool` fields with no internal synchronization - concurrently *mutating* those maps (e.g. an operator "live" revoking an agent) while `Authorize` concurrently reads them is a data race the Go race detector would catch if a test exercised it, which none currently does, because `CoreAuthorizer` is explicitly documented as a Phase 2A/2B stand-in, not a real policy engine | MITIGATED (temporal gap) / OPEN (concurrent-mutation safety of the stand-in policy type itself) | A real Core policy integration (replacing `CoreAuthorizer`) must either be internally synchronized or published via atomic pointer swap, not in-place map mutation. Do not deploy `CoreAuthorizer` itself in a setting where its maps are mutated concurrently with live traffic. |

### Additional findings disclosed (not in the requested list, found during review)

- **`Task.Locked` is never set by any code path in this package.** The doc
  comment says "Locked is set by Core once a task graph has been accepted,"
  but no such acceptance event or auto-lock mechanism exists yet - only tests
  construct `Task{Locked: true}` directly. This means
  `PolicyDowngradeForbiddenError` (a task's required capabilities cannot
  shrink once locked) is real, tested, working code, but is **not reachable
  in any current real call path**, since nothing ever locks a task. **OPEN**:
  this is a genuine gap between the documented intent and the wiring; closing
  it requires a product decision (when, exactly, does a task graph become
  "accepted"?) that is out of scope for a closure review ("do not add new
  product functionality"). Flagged here so Phase 3 does not assume
  capability-downgrade protection is active by default.
- **`collaboration.SupervisorAgentDirectory` reads `engine.cfg.Agents` with no
  synchronization.** Safe under the current architecture's assumption that
  `config.Config` is loaded once at startup and treated as immutable
  thereafter; would become a data race if HarnessMesh ever added concurrent
  hot-reloading of agent configuration. **OPEN (low, inherited assumption, not
  introduced by Phase 2A/2B)**.

### Decision: no proposal/batch IDs, generation counters, or OCC tokens added

The review explicitly considered adding proposal batch IDs, an execution
generation/version counter, a state revision number, a nonce/idempotency key,
and optimistic concurrency control, per the request's prompt. None were added.
Reasoning: `CoreState.applyGated` already provides the atomicity and
consistency OCC/revision numbers exist to provide (every mutation re-reads
current state and re-checks preconditions inside one lock); every proposal
type that mutates state already carries an implicit precondition tied to a
status value, which is what a nonce/idempotency key would otherwise be needed
to express. Adding a parallel ID scheme on top would be exactly the
"unnecessary parallel architecture" both ADRs warn against, for a guarantee
the status-precondition design already provides. This should be revisited
only if Phase 3 introduces a scenario this design doesn't already cover - e.g.
a network client that needs to safely retry an Apply call it's unsure
succeeded, which is a client-retry concern best solved at that new boundary,
not by retrofitting IDs into every existing proposal type now.

### Verification

```
gofmt -l .                  → clean
go build ./...              → clean
go vet ./...                → clean
go test -race ./...         → all 19 packages ok, zero regressions
```

`internal/supervisor`: 90 tests passing under `-race` (5 new: 4 concurrency
tests + 1 stale-authorization test), 73.0% coverage. `internal/modelrouting`:
9 tests passing under `-race` (2 new: redirect-denial + cancellation-
propagation), 63.1% coverage. Fuzz targets re-run for an additional ~20s each
post-fix (`FuzzDecodePlanResponse`, `FuzzDecodeSynthesisResponse`,
`FuzzDetectDuplicateKeys`): zero crashes across the combined run (prior run:
~3.6M execs; this run: ~1.8M more execs), bringing total fuzz coverage to
~5.4M executions with zero crashes, zero unknown-proposal-variant escapes,
zero error+non-nil-result violations.

### Final determination

**Is ADR 0002 Phase 1 + 2A + 2B safe and stable enough to become the
foundation for Phase 3?**

**YES**

All three defects found by this adversarial review have been fixed, tested,
and re-verified against the full regression suite with zero collateral
regressions. The primary invariant - LLM output is untrusted data and can
never become authority - held even before these fixes (none of the three
defects allowed an LLM-origin value to bypass Validator/Authorizer or acquire
decision authority) and is now additionally hardened against authorization
staleness, concurrent-batch atomicity, and transport-layer availability
attacks. The remaining OPEN items (tenant semantics not yet implemented,
`CoreAuthorizer`'s in-place map mutation not being concurrency-safe, `Task.Locked`
never being auto-set) are documented, bounded, and inherent to components
explicitly built as Phase 2A/2B stand-ins rather than final production policy
engines - they do not represent a bypass of the core trust boundary, and
Phase 3 can proceed as long as it does not assume these three specific gaps
are already closed.

## Phase 3: Controlled Multi-Agent Coordination

Phase 3 proves `Goal -> Plan -> TaskGraph -> capability resolution -> agent
routing -> dependency-aware execution -> result collection -> Replan -> further
execution -> Synthesis`, while every Phase 1/2A/2B guarantee (validation,
authorization, atomicity, TOCTOU revalidation, re-authorization at execution,
tenant fail-closed behavior) continues to apply unchanged. No existing type's
behavior was weakened; Phase 3 adds a new orchestration layer (`Scheduler`,
`Workflow`) on top of the existing `Applier`/`Executor`/`CoreState`, which it
calls exactly as any other caller would - it has no special access.

### Scheduler ownership

`Scheduler` (`scheduler.go`) is Core, not Supervisor: it is the only thing
that decides a task is actually executable. It:

- computes `ReadinessState` (`ready`/`waiting`/`blocked`/`active`/`done`) purely
  from the current, authoritative `TaskGraph` on every `Tick` - never cached,
  never supplied by a proposal;
- performs capability-based agent routing (`routeAgent`) itself, ignoring
  anything a Supervisor's proposal might have said about who should do the
  work - deterministic tie-break (lexicographically smallest eligible
  `AgentID`), no hardcoded task-type-to-agent mapping, no historical/ML
  routing;
- dispatches through the **unmodified** `Applier.Apply` (for the
  `AssignTaskProposal` it itself constructs) and `Executor.Execute` (which
  still independently re-validates agent/capability/tenant and
  re-authorizes immediately before dispatch, per the Phase 2A/2B closure
  review) - Scheduler adds no new authority, it only decides *when* to call
  the existing authority.

### Task lifecycle

No new persisted `Task.Status` values were added. `Pending`, `Assigned`,
`Executing`, `Completed`, `Failed` (Phase 2A/2B) remain the only stored
states, each with the same transition preconditions as before - changing
this would have meant re-verifying every Phase 1/2A/2B test's assumptions
about those preconditions, for a benefit Scheduler does not need.

"Ready" and "Blocked" are **derived, not stored**: `computeReadiness`
recomputes them fresh from `Dependencies` and each dependency's current
`Status` on every `Tick`, so they can never go stale the way a cached flag
could. This is a deliberate rejection of "define a typed `Ready`/`Blocked`
status" in favor of "compute it from what's already authoritative" -
consistent with ADR 0002's existing preference (see "Decision: no proposal
batch IDs..." above) for deriving guarantees from existing mechanisms instead
of adding parallel bookkeeping.

### Dependency semantics

A task is `Ready` only when every entry in `Dependencies` has
`Status == TaskStatusCompleted`. Any dependency in `TaskStatusFailed` makes
the task `Blocked` (reported once via a `TaskBlocked` event, not re-executed,
not retried automatically). A `Blocked` task can only become `Ready` again if
a Supervisor `Replan` creates a path that changes the picture - either
retrying the failed dependency (`RetryTaskProposal`, `Failed -> Pending`) or
simply not routing through it at all. **Design choice**: a *remediation* task
(like T4 in the integration scenario, created in response to T2's failure) is
deliberately NOT given a `Dependencies: ["T2"]` edge, even though it
causally follows T2's failure - a dependency edge in this package always
means "wait for success," and T2 is expected to have already failed by the
time remediation is proposed. The causal order is enforced by *when* Replan
creates the remediation task (strictly after the failure event fires), not by
a graph edge to a task that will never complete. This avoids introducing a
second edge type ("on-failure" vs. "on-success") for a distinction the
existing event-driven Replan sequencing already provides.

### Agent routing semantics

Purely capability-driven (`RequiredCapabilities` subset-of-declared-
capabilities check), reusing the exact `missingCapabilities` helper Phase 1's
`Validator` already used - no duplicate logic. `AgentCatalog` (new,
additive interface: `AgentDirectory` + `ListAgents`) is required because
Scheduler needs to *enumerate* candidates, unlike Phase 2A's point-wise
TOCTOU re-checks which only ever ask about one already-known agent ID.
`collaboration.SupervisorAgentDirectory` now also implements `AgentCatalog`.
Where multiple agents qualify, selection is deterministic (lexicographic);
no load-based, cost-based, or historical/ML routing was added, per Phase 3's
explicit exclusions.

### Parallel execution model

`Scheduler.Tick` dispatches up to `SupervisorLimits.MaxConcurrency` (Core
configuration; default 4; a Supervisor proposal has no field that could
influence it - verified structurally, same reflection-based pattern Phase
1/2B already used for model/provider fields) ready tasks concurrently via
goroutines, and blocks until every goroutine it started that tick has
finished (`sync.WaitGroup`) before returning. This means: no goroutine
Scheduler starts ever outlives a `Tick` call, Tick is safe to call in a tight
loop without accumulating background work, and "parallel" is real OS-level
concurrency (proven under `-race` with a concurrency-counting fake provider
in `TestScheduler_Tick_ConcurrencyLimitRespected`), not simulated
sequencing.

### Replanning boundary

`Workflow.Run` (`workflow.go`) calls `Supervisor.Replan` only in response to
a bounded set of event kinds from the *current* tick
(`EventTaskFailed`/`EventTaskBlocked`/`EventAgentUnavailable`/
`EventAuthorizationRejected`/`EventExecutionTimedOut`) - never speculatively,
never on historical events already handled. Every proposal Replan returns
still passes through the same `Applier.Apply` as anything else; a replan that
exceeds `MaxReplanCount` is rejected by the existing, unmodified
`Validator`/`ReplanLimitExceeded` path and ends the workflow rather than
looping. `MaxWorkflowTicks` (new limit, default 50) independently bounds the
scheduling loop itself, covering the case `MaxReplanCount` doesn't - a stuck
workflow that never actually calls Replan (nothing failed, nothing blocked,
yet also never reaches completion) still terminates deterministically.

### Completion semantics

`WorkflowStatus(graph)` is the *only* source of `Completed`/`Succeeded` in
`WorkflowResult` - derived purely from whether every task is terminal and
whether any task is `Failed`. `Synthesis` is carried in `WorkflowResult` for
display only; `Workflow.Run` never reads it to decide `Succeeded`.
`TestNegative_SynthesisClaimsSuccessWhileCoreStateFailed` proves this
directly: a `Supervisor.Synthesize` that returns an optimistic, false
"Everything succeeded!" summary has zero effect on `WorkflowResult.Succeeded`,
which correctly reports `false` because Core state shows a failed task.

### Concurrency limits (Core-owned)

`SupervisorLimits.MaxConcurrency` and `MaxWorkflowTicks` join the existing
Phase 1/2A limits (`MaxTaskDepth`, `MaxAgentFanout`, `MaxReplanCount`,
`MaxReviewRounds`, `MaxCost`, `MaxExecutionTime`) as Core configuration a
Supervisor cannot read back into its own output and cannot override -
verified by the same model/provider-field-style reflection check extended to
cover `SupervisorLimits` usage sites, and functionally by
`TestScheduler_Tick_ConcurrencyLimitRespected`.

### Trust boundaries (unchanged, re-verified)

Every invariant from the Phase 2A/2B closure review was re-asserted under
Phase 3's new orchestration layer, not just re-stated:

- Supervisor proposes work -> Core (`Validator`+`Authorizer`+`Applier`)
  decides what is executable (`TestNegative_SupervisorProposesIllegalTransition`,
  `TestScheduler_Tick_NeverExecutesBlockedTask`).
- Supervisor proposes an agent (if at all) -> Core decides whether the
  assignment is valid, and in Phase 3's scheduled flow actually **ignores**
  any Supervisor-proposed agent entirely, deriving the assignment itself from
  capabilities (`TestRouteAgent_DeterministicAmongMultipleEligible`).
- Supervisor describes completion via Synthesis -> Core
  (`WorkflowStatus`) determines whether the workflow is actually complete,
  independent of that description
  (`TestNegative_SynthesisClaimsSuccessWhileCoreStateFailed`).
- Switchyard selects models (via `SwitchyardInferenceRouter`, unchanged);
  HarnessMesh (`Scheduler.routeAgent`) selects agents - still two
  independent decisions, per ADR 0001, now also proven true for the
  dependency-scheduled multi-agent path, not merely the single-assignment
  path Phase 2A/2B covered.
- LLM output remains proposal, never authority -
  `TestIntegration_Workflow_ThreeAgentParallelWithRemediation` runs the full
  three-agent scenario with a `FakeSupervisor` standing in for the LLM, and
  every assignment/execution/completion decision is independently re-derived
  by Core exactly as it would be with a real `LLMSupervisor` (Phase 2B proved
  `LLMSupervisor` and `FakeSupervisor` are interchangeable at this boundary;
  Phase 3 does not need to re-prove that, only that nothing about the
  boundary itself changed).

### Failure model

New, distinct typed errors/events added without collapsing existing ones:
`TaskSchedulingFailure`, `DependencyBlocked` (informational/error-shaped
struct), `WorkflowLimitExceeded`, plus the full requested
`ExecutionEventKind` set (`EventTaskReady/Assigned/Started/Succeeded/Failed`,
`EventFindingProduced`, `EventExecutionTimedOut`, `EventAgentUnavailable`,
`EventAuthorizationRejected`, `EventTaskBlocked`). `AgentRoutingFailure`,
`AuthorizationFailure`, `AgentExecutionFailure`, `ExecutionTimeout`,
`ReplanLimitExceeded`, and `StateTransitionFailure` (covering the requested
"TaskTransitionFailure") were reused from Phase 2A/2B rather than duplicated,
since they already express exactly the stated failure classes.
`classifyApplyFailure`/`classifyExecutionFailure` map the underlying typed
Go errors onto this event taxonomy using `errors.As` (not a bare type
switch), since `Applier.Apply` wraps `Authorizer` rejections with
`fmt.Errorf("proposal %d: %w", ...)`.

### Evidence/provenance

`ExecutionResult` gained one additive field, `Findings []string` - finding
*identifiers* for correlation with the existing, unmodified
`collaboration.FindingPayload` evidence subsystem, never finding content and
never a verification flag. This package still has no way to mark anything
verified; that remains exclusively the existing `peer.resolve` path's
responsibility, untouched by Phase 3.

### A defect found and fixed during Phase 3 implementation

The first `Workflow.Run` draft checked `WorkflowStatus` for completion
*before* checking whether the current tick's events warranted a `Replan`
call. In the three-agent integration scenario, after T1/T2/T3 all reach a
terminal state in the very first tick (T1/T3 `Completed`, T2 `Failed`), every
task *currently in the graph* is terminal - so the naive ordering declared
the workflow `Completed` immediately, with `replans == 0`, before the
Supervisor ever got a chance to propose T4/T5/T6's remediation path. **Fixed**
by checking for a replan trigger first, calling `Replan` and applying its
proposals (if any) before evaluating completion, and only treating "the
Supervisor returned no proposals" as license to let the completion check
proceed on the next tick. Caught by
`TestIntegration_Workflow_ThreeAgentParallelWithRemediation` itself
(`expected exactly 1 Replan call, got 0`) before being treated as passing -
not found by a separate adversarial pass, since this was core scenario
coverage, not an edge case.

### Verification

```text
gofmt -l .                  → clean
go build ./...              → clean
go vet ./...                → clean
go test -race ./...         → all 19 packages ok, zero regressions
```

`internal/supervisor`: 116 tests passing under `-race` (26 new: scheduler
readiness/routing/dispatch, the three-agent integration scenario, 15 negative
tests, 3 new concurrency tests), 76.8% coverage.
`internal/collaboration`/`internal/modelrouting` unaffected beyond the
additive `AgentCatalog`/`ListAgents` implementation (61.4%/63.1% coverage,
unchanged from Phase 2B). Fuzz targets (`FuzzDecodePlanResponse`,
`FuzzDecodeSynthesisResponse`, `FuzzDetectDuplicateKeys`) re-run for a further
~1.8M executions post-Phase-3 with zero crashes, bringing the cumulative fuzz
total to ~7.2M executions with zero crashes across all three phases of
fuzzing.

### Deviations from the request

- "Data flow dependency" requested as an example (`T4 remediation depends on
  T2`) was implemented *without* that literal graph edge, per the "Dependency
  semantics" design-choice note above - the causal ordering is preserved via
  event-driven Replan sequencing instead, which was judged the smaller,
  more consistent addition than introducing a second dependency-edge
  semantics (success-dependency vs. failure-triggered-dependency).
- `TaskReady`/`TaskBlocked` event de-duplication (`Scheduler.reportedReady`/
  `reportedBlocked`) does not re-fire if a task leaves and later re-enters the
  same readiness state (e.g. blocked, then unblocked via retry, then blocked
  again by a different failure) - acceptable for observability-only events,
  called out explicitly rather than left undocumented.
- No Arbiter, consensus protocol, multi-agent voting, model-performance
  learning, agent/model joint routing, autonomous policy changes, speculative
  execution, or dynamic capability granting was implemented, per the
  request's explicit exclusions - confirmed by inspection of every new file
  added this phase (`scheduler.go`, `workflow.go`,
  `scheduler_test.go`/`workflow_test.go`/`scheduler_concurrency_test.go`).
