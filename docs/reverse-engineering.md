# HarnessMesh — AI-DLC Reverse Engineering

Status: prepared from the existing repository on 2026-10-22.

## Executive summary

HarnessMesh is a Go-based local control plane for multi-agent software-engineering workflows. Its primary product is a durable collaboration fabric: agents, spaces, channels, threads, inboxes, reviews, evidence, decisions, approvals, and controlled repository changes. A second, deliberately isolated product is a Codex-compatible model-provider gateway that exposes Responses API-compatible HTTP endpoints and routes inference to configured local, Bedrock, or explicitly permitted remote backends.

The project is already substantially implemented rather than a skeleton. The source contains approximately 73k lines of Go across 20 internal packages, a CLI, Docker Compose services, a VS Code extension, integration scripts, extensive documentation, and release/security automation. The current source version is v0.1.0, while the implementation includes features documented as later roadmap/ADR work; the roadmap should therefore be reconciled with the actual feature set.

## Business/domain overview

The central business transaction is an evidence-gated coding task:

1. A human or external client creates or joins a collaboration space and submits a task.
2. HarnessMesh persists the task/session and selects a managed participant using capability, writability, sensitivity, economy, and policy constraints.
3. The executor receives a bounded repository projection and may invoke its configured harness.
4. Other participants can be asked for advice or review through durable messages and peer sessions.
5. Reviewers submit structured findings, evidence, challenges, and resolutions.
6. A MeshCommit change transaction binds the proposed change to a workspace/tree fingerprint and proof obligations.
7. Deterministic gates verify tests, independent review, evidence freshness, authorization, and workspace state.
8. Only the authorized managed writable participant can commit; stale or incomplete proof blocks the commit.
9. Events, transcripts, findings, decisions, metrics, and knowledge records remain available for later sessions.

The provider transaction is separate:

1. A Codex-compatible client authenticates to `/v1/models` or `/v1/responses`.
2. The gateway parses and bounds the request, resolves a configured backend, and applies zero-credit/metering policy.
3. The request is translated to the backend wire format and the response is normalized back to Responses API or chat-completions format.
4. Streaming, tool calls, audit records, metrics, cancellation, timeouts, and backend failures are handled without accessing collaboration state.

## Architecture and code structure

| Layer | Packages/files | Responsibility |
| --- | --- | --- |
| Entrypoint | `cmd/harnessmesh` | CLI routing, integration bootstrap, provider/bridge/MCP startup, diagnostics |
| Domain/control plane | `internal/collaboration`, `internal/workflow`, `internal/supervisor`, `internal/review` | Sessions, routing, reviews, challenges, scheduling, approvals, MeshCommit, event handling |
| Shared contracts | `internal/protocol` | Domain types, wire envelopes, typed errors, request/response shapes |
| Persistence | `internal/store` | SQLite schema/migrations, transactions, sessions, jobs, events, evidence, budgets, outbox, dead letters |
| Agent execution | `internal/agent`, `internal/executil` | Managed harness adapters, external participant boundary, process invocation, sandbox policy |
| Context/security | `internal/contextpack`, `internal/workspace`, `internal/config`, `internal/creditguard` | Bounded projections, path/symlink filtering, policy validation, single-writer and billing invariants |
| Routing/intelligence | `internal/economy`, `internal/modelrouting`, `internal/performance` | Task classification, participant selection, Switchyard boundary, evaluation/experiments/advice |
| Knowledge/observability | `internal/knowledge`, `internal/report`, `internal/telemetry` | Append-only archive/search/RAG, reporting, metrics, OTLP export |
| Transports | `internal/mcp`, `internal/bridge` | MCP stdio/HTTP/SSE and REST/WebSocket bridge |
| Provider plane | `internal/provider` | Responses API, chat-completions adapter, model catalog, local/Bedrock/SIWC backends, auth/audit |
| Client | `extensions/vscode` | VS Code status, participants, tasks, reviews, findings, reconnect/event UI |

The largest concentration areas are persistence (`internal/store/store.go`), collaboration orchestration (`internal/collaboration/engine.go`), CLI composition (`cmd/harnessmesh/main.go`), MCP transport (`internal/mcp/server.go`), and provider wire compatibility (`internal/provider/wire.go`). These are natural maintenance hotspots because they combine many responsibilities and have high change fan-out.

## Main interaction paths

### Collaboration path

```text
Human / ChatGPT / VS Code
        |
        +--> MCP or REST/WebSocket transport
                    |
                    v
             collaboration.Engine
              |       |       |
              v       v       v
           SQLite  EventBus  Knowledge archive
              |
              v
       routing / policy / workflow
              |
              v
       managed agent adapter ---> repository workspace
              |
              v
        findings + evidence + MeshCommit gate
```

Text alternative: transports call one shared engine; the engine persists state, publishes events, routes work to adapters, and gates repository changes with evidence.

### Provider path

```text
Codex client
    |
    v
provider HTTP server --> auth/limits/policy --> backend registry
                                                |     |     |
                                                v     v     v
                                             local Bedrock SIWC
    ^
    +---- Responses/chat-completions normalization, SSE, audit, metrics
```

Text alternative: the provider server is independent of the collaboration engine and does not use collaboration persistence.

## External interfaces

- CLI: `collaborate`, spaces/channels/threads/inbox/subscriptions/decisions, `change`, `peer`, `agents`, `session`, `findings`, `evidence`, `knowledge`, `config`, `switchyard`, `doctor`, `integrate`, `mcp`, `bridge`, and `provider`.
- MCP: stdio plus Streamable HTTP/SSE at `/mcp`; health, metrics, and authenticated administration endpoints.
- Bridge: authenticated REST under `/api/v1/` for workspaces, inbox, messages, tasks, artifacts, findings/reviews, and event polling; WebSocket event replay under `/api/v1/events/ws`.
- Provider: authenticated `/v1/models`, `/v1/responses`, `/v1/chat/completions`, `/v1/status`; unauthenticated health/readiness/metrics endpoints are separated from inference operations.
- VS Code extension: TypeScript client of the bridge with participant/task/review/finding views and reconnect behavior.

## Technology and dependencies

- Go 1.25; SQLite through `mattn/go-sqlite3`.
- AWS SDK v2 and Bedrock Runtime for Bedrock inference.
- Gorilla WebSocket for bridge events.
- TOML parsing via `pelletier/go-toml`.
- Docker Compose with isolated MCP, bridge, provider, backup, and restore profiles.
- GitHub Actions for tests/race/vet/vulnerability scanning, CodeQL, release SBOM/checksum/signing, and wiki publishing.
- Optional external harnesses/backends: Claude Code, Codex, Copilot CLI, Antigravity, OpenAI-compatible servers, AWS Bedrock, ChatGPT SIWC, and NVIDIA NeMo Switchyard.

## Evidence of implementation maturity

- `go test ./...`: passed for all packages; the report includes extensive integration and protocol tests.
- `go vet ./...`: passed.
- Tests cover collaboration lifecycle, event coalescing, cycle detection, MeshCommit authorization/TOCTOU/stale evidence, context security, MCP/bridge authentication and idempotency, provider wire/SSE/model compatibility, concurrency, recovery, and zero-credit policy.
- CI additionally specifies race tests, fuzz seeds, vulnerability scanning, production-readiness gates, Compose validation, extension compilation, and Docker builds.
- Security boundaries are documented and tested: single writer, external-participant non-invocation, secret filtering, traversal/symlink protection, authenticated transports, rate/size limits, and fail-closed metered-backend rejection.

## Risks, gaps, and maintenance observations

1. The project is broad relative to its v0.1.0 label. Collaboration, supervisor/performance intelligence, provider compatibility, integrations, and operations are all active surfaces. A future change should be scoped to one plane or one domain transaction and accompanied by a targeted integration test.
2. The largest files are orchestration hotspots. `internal/store/store.go`, `internal/collaboration/engine.go`, `cmd/harnessmesh/main.go`, and `internal/mcp/server.go` would be difficult to modify safely without package-level seams or focused regression tests.
3. The roadmap describes older milestones through v0.6, while current code and ADRs implement many of those capabilities. Release planning and documentation should distinguish shipped behavior, experimental behavior, and future work.
4. The provider plane has intentionally distinct billing/security semantics from the collaboration plane. Operators must not treat the shared binary or shared deployment as shared trust/state; the configuration and deployment profiles need to remain separate.
5. Runtime safety depends partly on operator configuration: writable agent commands, sandbox allowlists, repository trust, remote exposure, TLS/tunnel setup, and credential handling remain deployment responsibilities.
6. The test suite validates deterministic behavior locally, but live external backend behavior, browser/SIWC account state, and real harness binaries are necessarily environment-dependent and are not established by `go test ./...` alone.
7. There is no existing reverse-engineering artifact or AI-DLC state file in the repository. This document is the baseline; subsequent AI-DLC work should update it rather than re-discovering the architecture.

## Recommended next AI-DLC decision point

Review this reverse-engineering baseline and choose one bounded follow-on objective. The highest-value candidates are: (a) reconcile release/roadmap documentation with shipped behavior, (b) harden one operational deployment profile, or (c) select one maintenance hotspot for decomposition with a behavior-preserving test plan. No implementation should begin until that objective is explicitly selected.
