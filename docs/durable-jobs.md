# Durable Jobs

HarnessMesh supports restart-safe, tenant-scoped jobs in the existing SQLite
store. A job has an opaque JSON payload, an execution kind, retry budget and a
lease owner. Workers claim jobs atomically; a crashed worker's lease expires
and another worker can claim the job.

```sh
bin/harnessmesh jobs submit --tenant acme --kind agent.task \
  --payload '{"task":"run the staging diagnostics"}'
bin/harnessmesh jobs list --tenant acme
bin/harnessmesh jobs list --status dead
```

Runtime handlers use `internal/jobs.Runtime`. Successful handlers mark jobs
`completed`; failed handlers receive exponential backoff and transition to
`dead` after `max_attempts`. The job payload is deliberately opaque to the
store so agent-specific runtimes can evolve independently.

This is the foundation for scheduled workflows, webhook-triggered agents and
multi-instance workers. The full collaboration store still requires a broader
PostgreSQL backend migration; the `Store` interface and lease semantics are
designed for that incremental path.

The first PostgreSQL slice is available as `store.PostgresJobStore`. It accepts
an injected `database/sql` connection, runs its own PostgreSQL migration, and
uses `FOR UPDATE SKIP LOCKED` for concurrent claims. This keeps the driver
optional for SQLite deployments; production wiring can use the organization's
chosen PostgreSQL driver (for example pgx/stdlib).

`store.PostgresSaaSStore` combines that job/lease persistence with the tenant
directory and API-key tables. It is the intended shared Control-Plane store for
the first SaaS deployment stage; the larger collaboration tables remain on the
incremental migration path.

`store.PostgresSessionStore` is the first tenant-aware collaboration slice. It
persists sessions, participants, budgets and lifecycle status with mandatory
tenant IDs. Messages, findings, evidence and event tables remain subsequent
migration slices and are not silently routed through SQLite when a PostgreSQL
deployment is selected.

`store.PostgresCollaborationStore` adds tenant-aware persistence for messages,
findings, evidence and events. Every operation requires an authenticated tenant
context, and every query includes both tenant and session boundaries.

`store.PostgresGovernanceStore` adds the same boundary for human approvals and
engineering decisions. Approval expiry is an atomic tenant-scoped update, while
decision payloads retain their JSON representation for forward-compatible
governance fields.

`store.PostgresMeshCommitStore` persists changes, paths, proof obligations,
evidence and gate results with tenant-scoped indexes. `store.PostgresExperimentStore`
adds performance observations plus atomic experiment reservations, admissions and
audit records. Both use the shared repository migration and must be provisioned
against the same PostgreSQL pool.

`store.PostgresSpaceStore` covers spaces, channels, threads, subscriptions,
participant cursors and summaries. Message history remains in
`PostgresCollaborationStore`, keeping high-volume streams separate from mutable
control-plane documents.

`store.NewPostgresRepositorySet(db)` groups the SaaS, session and collaboration
slices, including governance, and exposes one startup migration call. The
repository set is suitable for the control-plane deployment, but it
intentionally does not claim to be a drop-in replacement for the complete
legacy `store.Store` interface until the remaining collaboration methods are
migrated.

For explicit production provisioning, the CLI now exposes the migration entry
point:

```sh
HARNESSMESH_POSTGRES_DSN='postgres://user:password@db/harnessmesh?sslmode=require' \
  bin/harnessmesh postgres migrate
```

The command verifies connectivity and applies all PostgreSQL repository-slice
migrations before the application is started.

For external event sources, the built-in signed webhook ingress only enqueues
work and never executes it inline:

```sh
HARNESSMESH_WEBHOOK_SECRET='change-me' \
  bin/harnessmesh jobs webhook --listen 127.0.0.1:8790
```

Send `POST /deploy.failed` with `X-HarnessMesh-Tenant` and
`X-HarnessMesh-Signature: sha256=<HMAC-SHA256-hex>`. Requests without a valid
signature, tenant, or event path are rejected.

For a single active scheduler across multiple instances, configure a shared
leader lease in `WorkerConfig` (`LeaderLease`/`LeaderTTL`). Job claims remain
individually leased, so workers can also be run without a global leader when
the trigger is already externally coordinated. Every worker may additionally
provide `AuthorizeTenant`; the check is repeated immediately before handler
execution to protect against stale assignments.
