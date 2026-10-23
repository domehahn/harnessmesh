# HarnessMesh Production-Readiness Plan

## Target

Reach a controlled, auditable production pilot for a single-node deployment first. Treat multi-instance HA and general multi-tenant SaaS as separate later targets.

## Go-live blockers

| Priority | Workstream | Required outcome | Acceptance evidence |
| --- | --- | --- | --- |
| P0 | Supported topology | Single-node limits, capacity, RPO/RTO, upgrade policy, and non-goals are explicit. | Published deployment profile and operator runbook. |
| P0 | Agent isolation | Agent processes run as non-root with bounded filesystem, network, CPU, memory, process, and time permissions. | Hardened container/profile plus adversarial repository tests. |
| P0 | Identity and authorization | Human/operator identity, participant identity, scopes, role separation, token rotation, and revocation are defined. | Auth/RBAC integration tests and audit examples. |
| P0 | Secrets | Production credentials and encryption keys use a supported secret/KMS mechanism. | Secret rotation drill and backup redaction test. |
| P0 | Recovery | SQLite, Knowledge archive, configuration, and credentials have tested backup/restore and upgrade procedures. | Automated restore drill with measured RPO/RTO. |
| P0 | Readiness gate | Production-readiness, race, contract, security, and staging smoke gates pass in a real CI environment. | CI artifact with all gates green; no sandbox-only false positives. |
| P1 | Operations | SLOs, alerts, dashboards, traces, audit retention, and incident runbooks exist. | Staging incident exercise and alert verification. |
| P1 | Load/failure behavior | Queue, SQLite contention, provider failures, WebSockets, restarts, and disk pressure have bounded behavior. | Load and chaos test report. |
| P1 | API lifecycle | MCP/REST/provider contracts and migrations have compatibility and rollback policies. | Versioned schemas and upgrade matrix. |

## Implementation status

- [x] Managed-agent executable allowlists wired through all CLI adapters.
- [x] Fail-closed `security.require_agent_sandbox` configuration enforcement.
- [x] Single-node production configuration example.
- [x] Production Compose hardening overlay with resource and privilege limits.
- [x] Strong bearer-token policy for production MCP, Bridge, and Provider services.
- [x] Shared RFC 7662 OAuth introspection for MCP/Bridge, with optional Bridge scope enforcement.
- [x] Separate production MCP admin token.
- [x] Store-backed `/readyz` endpoints for MCP and Bridge.
- [x] Backup checksum manifests, atomic staging, restore verification, and restore smoke test in CI.
- [x] Knowledge archive encryption supports mounted key files with strict file permissions.
- [x] Docker image healthcheck and clean Trivy Dockerfile misconfiguration scan.
- [ ] OS-level network egress isolation path implemented via Bubblewrap private network namespaces; target-runtime smoke verification remains required because the current Docker/Colima kernel policy rejects unprivileged namespaces.
- [ ] Full human identity/RBAC/OIDC integration; transport-level OAuth introspection and Bridge scope gating are implemented, but organization-level roles and identity lifecycle remain deployment/product work.
- [ ] Multi-instance storage/HA.
- [ ] Full load/chaos test suite and staging evidence; deterministic in-process coverage, an executable bounded staging load smoke probe, and a staging scenario plan are documented, but no target staging deployment has been exercised.
- [x] Compatibility baseline, machine-readable OpenAPI baseline, external-client matrix worksheet, and release checklist documented; version-specific staging evidence remains release work.
- [x] Initial SLOs, Prometheus alert rules, dashboard starter, and incident runbook documented; alert delivery remains deployment work.

## Recommended delivery sequence

### Phase 1 — Production contract and boundaries

- Publish the supported single-node topology.
- Define capacity limits and unsupported configurations.
- Define threat model assumptions for trusted versus untrusted repositories.
- Freeze the minimum supported Go, Docker, Codex, MCP, and provider versions.
- Add a production configuration profile with fail-closed defaults.

### Phase 2 — Execution and credential hardening

- Run the harness and provider services as dedicated non-root users.
- Add per-task execution sandboxes and resource limits.
- Add network egress policy for agent processes.
- Move production secrets to a secret manager or KMS-backed mechanism.
- Add credential expiry, rotation, and revocation checks.
- Add secret scanning for projected context, logs, knowledge, artifacts, and backups.

### Phase 3 — Recovery and upgrade safety

- Automate online SQLite and archive backups.
- Verify checksums and integrity before restore.
- Run scheduled restore drills in an isolated environment.
- Test migration failure, restart, rollback, and interrupted-commit scenarios.
- Document data retention and archive compaction policies.

### Phase 4 — Observability and operator control

- Define SLOs for task completion, review latency, provider latency, and error rates.
- Add dashboards and alerts for dead letters, queue depth, circuit breakers, disk usage, backup age, auth failures, and token expiry.
- Add end-to-end traces using request, correlation, session, task, and change IDs.
- Make security/audit events tamper-evident and exportable.
- Create runbooks for provider outage, stuck task, failed restore, corrupted archive, and emergency stop.

### Phase 5 — Validation and staging

- Make test listeners environment-independent; avoid IPv6-only assumptions in test infrastructure.
- Run the full production-readiness gate on clean Linux CI runners.
- Add load tests for concurrent sessions, WebSockets, SQLite writes, and provider streams.
- Add chaos tests for process termination, network loss, provider timeout, disk-full, and restart during migration/commit.
- Run a staging pilot with real harness binaries and non-production credentials.

## Later extensions

These should follow the controlled pilot rather than precede it:

- PostgreSQL or another transactional multi-instance backend.
- HA scheduling, leader election, and horizontally scalable event delivery.
- Per-task worktree isolation with explicit merge/reconciliation.
- OIDC/SAML, organization/tenant hierarchy, and fine-grained RBAC.
- Artifact/object storage for large diffs, logs, evidence, and replay bundles.
- Policy-as-code for data residency, model/provider selection, repository trust, and approval thresholds.
- Admin UI for tasks, approvals, findings, budgets, health, and emergency controls.
- Evaluation/replay platform for agent, model, and routing decisions.
- Kubernetes/Helm deployment and managed backup integration.

## Definition of production-ready pilot

The pilot is ready only when:

- all P0 workstreams have documented evidence;
- the complete deterministic gate passes in CI;
- a real staging task completes from intake through reviewed MeshCommit;
- a backup is restored successfully within the target RPO/RTO;
- an operator can revoke credentials and stop execution;
- a provider outage and an agent crash recover without data corruption;
- no external listener is exposed without authentication, TLS/tunnel policy, and rate limits;
- known limitations are published to operators and users.

## Current assessment

The requirement-by-requirement evidence audit is maintained in
[docs/production-readiness-evidence.md](../../docs/production-readiness-evidence.md).

The controlled single-node pilot gates are implemented and verified. The
authoritative `make production-readiness` gate passed completely, as did the
full test suite, full race suite, configuration validation, Compose validation,
Trivy Dockerfile scanning, and the backup/restore smoke test.

The following remain deliberately outside the single-node pilot claim:

- Deployment-specific verification that Bubblewrap user/network namespaces are
  available under the target runtime; the code and production image now fail
  closed when the isolator is unavailable.
- Enterprise identity federation (OIDC/SAML), organization-level RBAC, and
  multi-tenant lifecycle management.
- Multi-instance HA, distributed scheduling, and a PostgreSQL-class backend.
- Full staging load/chaos evidence and deployment-specific dashboards/alerts.
- Version-specific external-client staging evidence and release-specific contract fixtures.

These are not silently treated as complete. They require a separate deployment
target, infrastructure choice, or product scope decision.
