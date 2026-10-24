# Production-Readiness Evidence Matrix

This matrix is the completion audit for the current single-node production
pilot target. A repository test is only marked as evidence where it covers the
stated requirement; deployment-dependent rows remain open until the target
environment produces a dated artifact.

| Workstream | Current evidence | Status | Remaining evidence |
| --- | --- | --- | --- |
| Supported topology | Production JSON profile, Compose overlay, operations runbook | Implemented | Capacity limits from staging |
| Agent command/path isolation | Config enforcement and adapter/exec tests | Implemented | Adversarial repository run in target image |
| Agent network isolation | Bubblewrap path, fail-closed behavior, image dependency, executable runtime gate, target-runtime qualification checklist | Implemented, runtime pending | Dated namespace + outbound-connectivity evidence from the target runtime |
| Identity/authentication | Strong tokens, separate admin token, RFC 7662 introspection, MCP/Bridge scope gates, live token-file reload, documented rotation drill | Implemented | IdP lifecycle, role mapping, dated rotation/revocation result |
| Secrets | Token files with live reload, encrypted Knowledge key file, redaction tests, documented rotation drill | Implemented | Secret-manager/KMS integration and dated rotation artifact |
| Recovery | Atomic backup, checksum/integrity verification, restore smoke, configurable RTO threshold | Implemented | Dated staging restore with production-sized data and measured RPO |
| Readiness gate | `make production-readiness` with all deterministic sub-gates green | Implemented | Linux CI artifact retained by release pipeline |
| Operations | SLOs, alerts/runbook, metrics and incident procedures documented | Implemented | Staging alert delivery and incident exercise |
| Load/failure behavior | Race/concurrency/recovery tests, load smoke probe, chaos scenarios | Partial | Real staging load/chaos report |
| API lifecycle | OpenAPI baseline, compatibility policy, client matrix worksheet | Partial | Version-specific external-client fixtures and migration rollback run |
| HA/multi-instance | Explicitly outside single-node target | Later target | PostgreSQL/HA design and deployment scope decision |

The open rows are external-state gates, not silently treated as passing. The
single-node pilot must not be promoted to a general multi-instance or
enterprise-identity claim without those artifacts.
