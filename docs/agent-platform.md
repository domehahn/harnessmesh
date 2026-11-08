# Agent platform adapters

HarnessMesh remains the control plane for agent collaboration, policy,
approvals, evidence, routing and observability. Optional integrations are
configured under `agent_platform` and are disabled by default.

## A2A

The `internal/a2a` package provides a tenant-bound Agent Card and task
transport. When `agent_platform.a2a.enabled` is set, the bridge mounts it at
`/.well-known/agent-card.json` and `/a2a/v1/tasks`, backed by the same
collaboration engine. An A2A task must include `metadata.space_id`; the task
becomes a normal, audited collaboration message rather than a second task
system.

The endpoint must require a dedicated bearer token. Every task must carry a
tenant and every task lookup requires the matching `X-HarnessMesh-Tenant`
header. Never reuse an MCP, bridge or provider token.

## Durable workflows

`internal/workflow.DurableWorkflow` is the adapter boundary. The built-in
implementation uses the existing leased durable job store and works with
SQLite/PostgreSQL. Temporal or Restate adapters can be added without changing
tenant authorization, evidence or UI consumers.

## Evaluation

`internal/evaluation` evaluates observed outcomes deterministically. It scores
task completion, policy safety and efficiency, and can aggregate and rank
runs. It never treats an agent's text claim as authoritative and never calls
an external judge. External evaluation tools such as DeepEval can consume
exported, secret-redacted traces in a separate CI job:

```bash
harnessmesh eval export --dataset ./evaluations/production.jsonl \
  --format deepeval --out ./evaluations/deepeval.jsonl
```

## Browser, memory and LLM gateway adapters

These are explicit integration boundaries, not implicit network access:

- Browser providers (`playwright`, `browser-use`, `stagehand`) require HTTPS,
  domain allowlists, explicit approvals (mandatory for mutating `act` steps)
  and pre-execution evaluation through the HarnessMesh Action Firewall
  (`BrowserPolicy.AuthorizeAction`).
- Memory providers (`letta`, `cognee`, `haystack`, `custom`) must be
  tenant/project scoped and redact secrets before persistence. `MemoryClient`
  supports both key-value operations (`Put`, `Search`) and hierarchical
  Letta-style memory blocks (`PutBlock`, `Blocks` across `core`, `archival`
  and `recall` tiers).
- LLM gateways are optional and must not bypass HarnessMesh routing, quota or
  zero-API-billing policy.

The built-in clients are available in `internal/integrations`: `MemoryClient`
supports tenant/project-scoped put/search and tiered memory block calls,
`GatewayClient` provides an authenticated health probe, and `BrowserPolicy`
enforces HTTPS, domain allowlists, approval and Action Firewall digests before
a browser adapter is called. The workflow package also includes an
authenticated HTTP adapter for Restate-like durable providers; native Temporal
integrations should implement the same small `DurableWorkflow` interface with
the Temporal SDK.

Run `harnessmesh agent-platform doctor --config harnessmesh.json` or
`make agent-platform-doctor` to inspect the effective adapter configuration.

The complete example is
[`configs/agent-platform.example.json`](../configs/agent-platform.example.json).
The external projects are replaceable; HarnessMesh does not embed their
servers or credentials in the core image.
