# Load and Failure Validation

HarnessMesh has deterministic concurrency, recovery, timeout, provider,
WebSocket, and persistence tests. This document defines the remaining staging
validation required before claiming production capacity.

## Existing deterministic coverage

- `go test -race ./...` covers in-process data races and concurrent lifecycle
  paths.
- `make production-readiness` covers provider recovery, concurrency,
  filesystem write/edit, redaction, auth, and zero-billing policy.
- `make backup-restore-smoke` covers a clean SQLite/Knowledge restore with
  checksum verification and reports elapsed restore time. Set
  `HARNESSMESH_RESTORE_RTO_SECONDS` in CI/staging to enforce the target.
- Bridge tests cover idempotent concurrent task creation and bounded slow
  WebSocket consumers.
- Workflow tests cover peer timeout, unavailable peers, budgets, stalled loops,
  crash recovery, partial review failure, and provider routing failures.

## Staging load scenarios

The repository includes a bounded HTTP smoke probe for a disposable Bridge
deployment. It is intentionally explicit about the target and credentials:

```bash
HARNESSMESH_BRIDGE_URL=http://127.0.0.1:8788 \
HARNESSMESH_BRIDGE_TOKEN="<staging-token>" \
HARNESSMESH_LOAD_REQUESTS=100 \
HARNESSMESH_LOAD_CONCURRENCY=10 \
make staging-load-smoke
```

The probe checks readiness, executes authenticated workspace reads in bounded
batches, records status/latency, and prints `STAGING_LOAD_SMOKE=PASS`. It is a
smoke gate, not a substitute for the capacity report below.

Run these against a disposable staging deployment with synthetic repositories:

1. 100 concurrent read-only MCP callers listing spaces and pulling inboxes.
2. 25 concurrent task submissions with retries and duplicate idempotency keys.
3. 10 simultaneous provider streams with slow consumers and cancellation.
4. Event bursts from repository changes while reviewers are offline.
5. SQLite contention during concurrent findings, evidence, and knowledge writes.
6. Backup while writes are active, followed by restore into a fresh volume.

Record throughput, p50/p95/p99 latency, error rate, queue depth, SQLite lock
waits, memory, CPU, file descriptors, WebSocket disconnects, and dead letters.

## Chaos scenarios

1. Kill the executor during a task and verify bounded retry/recovery.
2. Kill MCP/Bridge during a persisted event burst and verify replay.
3. Cut provider connectivity during streaming and verify cancellation/fallback.
4. Fill the data volume and verify a safe failure without partial commit.
5. Restart during a schema migration and verify transactional recovery.
6. Mutate the repository after evidence capture and verify MeshCommit rejection.
7. Revoke each transport token and verify access stops without stale sessions.

## Acceptance

Production capacity is not established until a staging report records the
tested limits, observed failure behavior, recovery time, and any rejected or
deferred scenarios. Package tests alone prove correctness of bounded paths;
they do not prove deployment capacity.
