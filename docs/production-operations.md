# Production Operations

This runbook targets the supported single-node production profile. It does not
claim high availability or multi-tenant isolation.

## Minimum operating objectives

Suggested initial SLOs; operators should tune them with real staging data:

| Signal | Initial objective | Alert condition |
| --- | --- | --- |
| MCP/Bridge availability | 99.9% monthly | `/readyz` fails for 2 minutes |
| Provider availability | 99.9% monthly | `/readyz` fails for 2 minutes |
| Task completion | 95% successful terminal outcomes | 15-minute rolling rate below target |
| Backup freshness | less than 24 hours | oldest successful backup exceeds 24 hours |
| Dead letters | zero unreviewed high-severity entries | any high-severity entry or sustained growth |
| Disk usage | below 70% normal operation | warning at 70%, critical at 85% |

## Start and verify

Use the production configuration and overlay only after replacing the example
agent commands, workspace path, and credentials:

```bash
export HARNESSMESH_MCP_TOKEN="<random-token-at-least-32-bytes>"
export HARNESSMESH_ADMIN_TOKEN="<different-random-token-at-least-32-bytes>"
export HARNESSMESH_BRIDGE_TOKEN="<random-token-at-least-32-bytes>"
export HARNESSMESH_PROVIDER_TOKEN="<random-token-at-least-32-bytes>"
docker compose -f compose.yaml -f compose.production.yaml \
  --profile mcp up -d
```

For Docker/Kubernetes secret mounts, use `HARNESSMESH_MCP_TOKEN_FILE`,
`HARNESSMESH_BRIDGE_TOKEN_FILE`, or `HARNESSMESH_PROVIDER_TOKEN_FILE`. The
separate admin token remains required as `HARNESSMESH_ADMIN_TOKEN` by the
production Compose overlay. A direct token environment variable takes
precedence over its file variant.

For federated deployments, MCP and Bridge also support RFC 7662 OAuth
introspection via `HARNESSMESH_MCP_OAUTH_INTROSPECTION_URL` and
`HARNESSMESH_BRIDGE_OAUTH_INTROSPECTION_URL`. Bridge can require a scope with
`HARNESSMESH_BRIDGE_REQUIRED_SCOPE`; MCP can require one with
`HARNESSMESH_MCP_REQUIRED_SCOPE`. Introspection failures fail closed.

Verify:

```bash
curl -fsS http://127.0.0.1:8787/healthz
curl -fsS http://127.0.0.1:8787/readyz
curl -fsS http://127.0.0.1:8788/readyz
docker compose -f compose.yaml -f compose.production.yaml ps
```

Do not expose the loopback ports directly to the Internet. Put a TLS-terminating
reverse proxy or approved tunnel in front of remote MCP, and enforce network
egress separately for control-plane and inference backends. Managed agents in
the production profile use Bubblewrap private network namespaces; verify this
with a staging task that attempts an outbound connection and confirm it fails.
The target runtime must permit the required user/network namespaces; if it does
not, the agent fails closed and must not be admitted as a production executor.
Run `HARNESSMESH_ISOLATION_IMAGE=<exact-image> make runtime-isolation-check`
before the first rollout and after runtime changes.
See [target-runtime-qualification.md](target-runtime-qualification.md) for
the complete release checklist and failure policy.

## Daily checks

For PostgreSQL provisioning, apply repository migrations before starting the
application:

```sh
HARNESSMESH_POSTGRES_PASSWORD='use-a-secret-manager' \
HARNESSMESH_ADMIN_TOKEN='...' \
HARNESSMESH_MCP_TOKEN='...' \
docker compose -f compose.yaml -f compose.production.yaml --profile postgres run --rm harnessmesh-postgres-migrate
```

The PostgreSQL profile is a single-node operational baseline. HA, managed
backups, PITR and failover still require the selected infrastructure provider.

Large evidence and report payloads should use the artifact boundary described in
[`docs/artifact-storage.md`](artifact-storage.md), not database blobs.

- Confirm `/readyz` for every enabled service.
- Check `/metrics` for errors, active streams, rate-limit denials, circuit
  breakers, retries, and dead letters.
- Confirm the latest backup directory and checksum manifest exist.
- Check disk, memory, process count, and container restart counts.
- Review unresolved high-severity findings and pending approvals.

Prometheus rule and Grafana dashboard starting points are versioned under
`deploy/monitoring/`. Import them into the deployment's monitoring stack and
verify alert delivery in staging; the repository does not assume a particular
Prometheus/Grafana installation.

## Incident actions

### Suspected credential compromise

1. Stop remote MCP/Bridge exposure.
2. Revoke and replace MCP, Admin, Bridge, Provider, tunnel, and backend tokens.
3. Preserve audit logs and the current database/archive before cleanup.
4. Restart with the production overlay and verify all readiness endpoints.

### Credential rotation drill

Run this procedure in staging before a production rollout:

1. Record the active token fingerprint outside the application logs.
2. Generate replacement MCP, admin, Bridge, Provider, and tunnel credentials.
3. Replace mounted secret files atomically, or update the configured secret
   manager version; never edit a secret in the repository. HarnessMesh reloads
   `*_TOKEN_FILE` values on each authenticated request.
4. Verify the new credential succeeds while the old credential returns `401`.
   Direct environment tokens require a restart because process environments are
   immutable.
5. For OAuth introspection, revoke the token at the authorization server and
   verify the next request is rejected without a HarnessMesh restart.
6. Preserve the rotation timestamp and result in the audit/change record.

The drill is complete only when old credentials fail, new credentials work,
and no token value appears in logs, metrics, backups, or evidence excerpts.

### Runaway agent or unsafe repository change

1. Use the emergency stop/human-control mechanism and stop the executor.
2. Preserve the session, evidence, logs, and current tree fingerprint.
3. Do not commit or reuse stale evidence.
4. Review the change through MeshCommit after the workspace is restored.

### Provider outage

1. Confirm `/readyz`, backend health, and provider error metrics.
2. Verify fallback policy does not select a forbidden metered backend.
3. Let circuit breakers and bounded retries drain naturally.
4. Escalate to an operator decision if the task deadline or budget is reached.

### Restore

1. Stop MCP, Bridge, and Provider services.
2. Run `HARNESSMESH_RESTORE_RTO_SECONDS=<target> make backup-restore-smoke` in
   CI or use a selected production backup; retain the reported elapsed RTO.
3. Restore only with `HARNESSMESH_RESTORE_SERVICES_STOPPED=1` and `--confirm`.
4. Verify checksums, SQLite integrity, `/readyz`, knowledge counts, and a read-only session query.
5. Preserve pre-restore copies until the incident is closed.

## Release gate

Before a production release, require:

- clean `go test`, race, vet, fuzz, vulnerability, and Compose gates;
- successful backup/restore smoke and staging restore drill;
- no unresolved critical security findings;
- production config validation with strong tokens and sandbox requirements;
- documented rollback version and verified backup;
- an operator available for the first real task after rollout.
