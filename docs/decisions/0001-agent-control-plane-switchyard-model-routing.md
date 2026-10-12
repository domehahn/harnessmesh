# ADR 0001: HarnessMesh as Agent Control Plane with Switchyard Model Routing

## Status
Accepted (documents an already-implemented separation; formalizes naming and closes minor gaps).

## Context

The request was to ensure HarnessMesh decides *what work happens and who (which agent)
does it*, while NVIDIA Switchyard decides *which concrete model/provider* serves an
inference turn inside that agent's execution.

Before writing any code, the full repository was audited for existing agent-routing,
task-routing, capability-registry, and model-routing abstractions (see Gap Matrix below).

## Problem

A naive reading of the request implies building a new `internal/agentcontrolplane`
package, new `Task`/`AgentDescriptor`/`AgentRoutingDecision` types, and a new
`ModelRouter` interface with a `SwitchyardModelRouter` implementation, as if none of
this existed yet.

That is not the actual state of the repository.

## Decision

**No new control-plane package is introduced.** The existing split is kept and named
explicitly as the authoritative architecture:

```
HarnessMesh  = decides WHAT and WHO
  internal/economy            -> task classification, participant selection
  internal/collaboration      -> agent registry (live), orchestration, authorization
  internal/config             -> declared agent capabilities (AgentConfig)

Switchyard   = decides WITH WHAT a selected agent thinks
  internal/modelrouting       -> ModelRoutingBackend interface
  internal/modelrouting.SwitchyardBackend -> real HTTP implementation
```

This matches docs/economy-routing.md's existing "Two-Level Orchestration Architecture"
(Level 1: HarnessMesh picks the participant; Level 2: Switchyard picks the model)
verbatim, and docs/ARCHITECTURE.md's "Two planes" statement.

### Responsibilities

| Layer | Owns | Does not own |
|---|---|---|
| `economy.Controller` + `collaboration.Engine` | task difficulty classification, capability filtering, participant/agent selection, escalation | which model a selected agent's backend calls |
| `modelrouting.ModelRoutingBackend` (Switchyard impl) | model/provider selection inside an agent's backend call, Switchyard health, route metadata | agent selection, capability policy, task classification |

### What already satisfies each requested primitive

| Requested type/concept | Existing equivalent | File |
|---|---|---|
| `Task` / task classification | `economy.DifficultySignals`, `economy.TaskTier`, `ClassifyTaskDifficulty` | internal/economy/economy.go |
| `Capability` | `config.AgentCapabilities` (typed bool fields) + free-text capability/role matching | internal/config/config.go |
| `AgentDescriptor` | `config.AgentConfig` (declared) + `agent.Harness` (behavioral) | internal/config/config.go, internal/agent/agent.go |
| Live agent registry | `collaboration.Engine.harnesses map[string]agent.Harness` | internal/collaboration/engine.go |
| `AgentRoutingDecision` (explainable) | `economy.RoutingDecision` (eligible list, selected, reasons[]), persisted via `store.RoutingDecisionRecord` | internal/economy/economy.go |
| `ModelRouter` interface | `modelrouting.ModelRoutingBackend` | internal/modelrouting/backend.go |
| `SwitchyardModelRouter` | `modelrouting.SwitchyardBackend` | internal/modelrouting/backend.go |
| Cross-cutting backend-access guard | `creditguard.CheckParticipant` (blocks `execution_mode:external` from metered backends regardless of model-routing config) | internal/creditguard/creditguard.go |

### Closed gaps (small, additive)

1. **Naming/documentation gap only**: nothing in code or docs explicitly labeled
   `internal/economy` + the selection half of `collaboration.Engine` as "the Agent
   Control Plane." This ADR and an addition to docs/ARCHITECTURE.md close that gap —
   no code moves.
2. **Minor duplication**: `agent.CheckSwitchyard` (internal/agent/agent.go) and
   `modelrouting.SwitchyardBackend.Health` both independently probe Switchyard health.
   Left as-is in this ADR (out of scope — removing it is a separate, small cleanup
   with its own blast radius on `agent` package callers) but flagged for a follow-up.

### OpenResponses

Evaluated per the request. docs/openresponses.md describes an **already-built**,
architecturally separate gateway (`internal/provider`) implementing `/v1/responses`
for external Codex/ChatGPT clients. It is a different "plane" (model-provider plane)
from the collaboration/agent-control plane described here, and is not an "agent
invocation boundary" into HarnessMesh's task/agent routing — it never decomposes
tasks or selects agents, it only proxies inference turns to a SIWC backend.

**Decision: no change.** OpenResponses is not integrated as an agent-invocation
boundary into the control plane described here, because the two are not the same
concern: `internal/provider` terminates at "forward this inference turn," while this
ADR's control plane terminates at "which agent and task." Revisit only if a future
request specifically asks external non-MCP clients to submit *tasks* (not inference
turns) to HarnessMesh.

### MCP

MCP tool groups (`change.*`, `peer.*`, `collaboration.*`, `operations.*`,
`knowledge.*`) remain the external API surface of the existing control plane
(internal/mcp/server.go). No routing or model-selection logic is added to MCP; it
continues to call straight into `collaboration.Engine`.

## Gap Matrix

| Concern | Existing | Target | Gap | Action |
|---|---|---|---|---|
| Task classification | `economy.ClassifyTaskDifficulty` | same | none | document as "Task Router" equivalent |
| Capability registry | `config.AgentCapabilities` + `agent.Harness` | same | naming only | document; no new type |
| Agent selection | `economy.Controller.SelectParticipant` | same | none | document as "Agent Router" |
| Model routing | `modelrouting.SwitchyardBackend` | Switchyard | none | none |
| Agent/model separation | already enforced (different packages, different call sites) | same | none | ADR formalizes it |
| Explainable routing decision | `economy.RoutingDecision` + `store.RoutingDecisionRecord` | same | none | none |
| OpenResponses as invocation boundary | separate plane (`internal/provider`) | not integrated into control plane | intentional non-gap | documented rejection above |
| ADR convention | none existed | `docs/decisions/NNNN-*.md` | was missing | this file establishes it |

## Alternatives considered

- **Build a new `internal/agentcontrolplane` package** mirroring the proposal's
  example Go types verbatim. Rejected: would duplicate `economy`/`collaboration`
  logic, violating the explicit "no parallel architecture" and "no big-bang rewrite"
  constraints, and the existing split already satisfies the responsibility boundary.
- **Merge `modelrouting` into `economy`**. Rejected: they are already correctly
  separated by call site and by what they decide; merging would recreate the coupling
  the request explicitly warns against.

## Consequences

- No breaking changes; this ADR is documentation-only plus a doc update, consistent
  with "evolutionary, not big-bang."
- Future routing-criteria additions (availability, trust level, cost, historical
  success rate — requested as "architecture must be extensible, not all implemented
  now") should land inside `economy.Controller.SelectParticipant`'s existing filter
  pipeline, not a new layer.
- Joint agent+model historical routing (requested as a future capability) should be
  built as a read-only analytics layer over `store.RoutingDecisionRecord` (agent-level)
  and Switchyard's own route metadata (model-level) — kept as two correlated datasets,
  not merged into one selector, preserving the responsibility boundary.

## Security implications

No new attack surface introduced (documentation-only). Existing guards remain
load-bearing and unchanged: `creditguard.CheckParticipant` (backend-access by
execution mode), capability/writable filters in `SelectParticipant`.

## Operational implications

None — no code or config changes ship with this ADR.

## Migration strategy

None required. This ADR is the migration: from "this separation exists but is
undocumented/unnamed" to "this separation is the documented, named architecture."
