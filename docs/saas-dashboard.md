# HarnessMesh SaaS Dashboard

The embedded dashboard is served by the MCP HTTP process at `/dashboard`.
It is intentionally dependency-free and uses the same tenant API-key boundary
as the identity API.

The modern React/TypeScript console lives in `frontend/`. Run it locally with
`npm run dev` from that directory; Vite proxies the control-plane APIs to the
MCP server. For production, serve `frontend/dist` through the TLS-terminating
web tier and route `/api`, `/healthz` and `/metrics` to HarnessMesh. The
embedded dashboard remains available as a dependency-free fallback.

## Authentication

Enter an API key issued for an active tenant membership. The browser sends it
as `X-HarnessMesh-API-Key` and stores it only in `sessionStorage`. Logout removes
the key immediately. For Keycloak/OIDC deployments, configure the dashboard
RFC 7662 introspection boundary and send an access token as `Authorization:
Bearer ...`:

```sh
export HARNESSMESH_DASHBOARD_OAUTH_INTROSPECTION_URL="https://keycloak.example/realms/harnessmesh/protocol/openid-connect/token/introspect"
export HARNESSMESH_DASHBOARD_OAUTH_CLIENT_SECRET="<confidential-client-secret>"
```

Keycloak remains responsible for login, MFA, token expiry and role claims.
HarnessMesh requires an active token with `sub` and a tenant claim (`tenant`,
`org_id` or `organization`) and maps Keycloak roles to tenant authorization.
Dashboard API requests are rate limited per client; tune the limit with
`HARNESSMESH_DASHBOARD_RATE_LIMIT`.

## Read APIs

- `GET /api/v1/dashboard/summary`
- `GET /api/v1/dashboard/sessions`
- `GET /api/v1/dashboard/sessions/{id}`
- `GET /api/v1/dashboard/jobs?status=...`
- `GET /api/v1/dashboard/jobs/{id}` includes the tenant-scoped persistent event history.
- `GET /api/v1/dashboard/jobs/{id}/events` returns the durable execution timeline.
- `GET /api/v1/dashboard/jobs/{id}/events/stream` provides an authenticated
  SSE stream for durable job events.
- `GET /api/v1/dashboard/approvals?status=...`
- `GET /api/v1/dashboard/usage`
- `GET /api/v1/dashboard/optimization` returns tenant-scoped usage buckets by
  agent, model and session, persisted token/cost/duration data, agent health,
  quality samples, and an explicit provider-quota availability state. Set
  `HARNESSMESH_PROVIDER_QUOTA_URL` to an operator-owned provider quota endpoint
  returning `used_percent`, `next_available_at`, `remaining` and `limit`.
  Optionally set `HARNESSMESH_PROVIDER_QUOTA_TOKEN` for bearer authentication.
  Set `HARNESSMESH_MCP_HEALTH_URL` and `HARNESSMESH_BRIDGE_HEALTH_URL` to the
  respective authenticated `/readyz` endpoints to expose live transport
  probes. Optional MCP and Bridge tokens are sent as bearer credentials;
  absent endpoints are reported as `not_configured`, never as healthy.
- `GET /api/v1/dashboard/health`
- `GET /api/v1/dashboard/incident` returns the tenant-scoped incident posture,
  failed jobs, dead letters and unhealthy agent records.
- `GET /api/v1/dashboard/simulation` returns deterministic safety scenarios;
  `POST` evaluates an observation without invoking an external agent or model.
- `GET /api/v1/dashboard/approvals/{id}` returns the approval decision context
  and immutable status-transition audit history.
- `GET /api/v1/dashboard/sessions/{id}/bundle` downloads a deterministic ZIP
  run bundle. Sensitive credentials are redacted before the bundle is written.
- `GET /api/v1/dashboard/sessions/{id}/events/stream` provides a tenant-scoped
  Server-Sent Events stream of persisted session events. It requires the same
  API-key or OIDC authentication as the dashboard API.
- `GET/POST /api/v1/dashboard/spaces` lists or creates tenant-owned visual
  collaboration spaces.
- `GET /api/v1/dashboard/agents` lists the safe, non-secret configured agent
  catalog available for visual orchestration.
- `GET /api/v1/dashboard/spaces/{id}` reads a space; `POST` to
  `/lifecycle` pauses, resumes or stops it, and `POST` to `/participants`
  connects an external agent participant with an adapter and activity mode.
- `POST /api/v1/dashboard/spaces/{id}/participants/{participant}/mode` changes
  a participant's activity mode with owner authorization.
- `GET /api/v1/dashboard/spaces/{id}/messages?channel=general&limit=100` returns
  the tenant-scoped collaboration transcript for the selected channel. The
  limit is bounded to 500.
- `GET /api/v1/dashboard/spaces/{id}/messages/stream?channel=general` provides
  an authenticated SSE stream for collaboration messages.
- `POST /api/v1/dashboard/spaces/{id}/messages` publishes an operator message
  through the Collaboration Engine with `{ "channel", "message", "to", "mentions" }`.

Every response is derived from the authenticated tenant context. Tenant IDs in
URLs or request bodies are not trusted for authorization.

The sessions, jobs and approvals list endpoints preserve their legacy array
response by default. Add `page` or `page_size` to receive a paginated response
with `items`, `page`, `page_size`, `total` and `has_more`; page size is capped
at 200.

## Write APIs

- `GET/POST /api/v1/dashboard/members` lists or manages tenant members. Owner
  role is required.
- `GET/POST /api/v1/dashboard/keys` lists keys or issues one for an existing
  member. The secret is returned only on creation and owner role is required.
- `POST /api/v1/dashboard/keys/{id}/revoke` revokes a tenant API key.
- `POST /api/v1/dashboard/jobs` submits a durable job.
- `POST /api/v1/dashboard/approvals/{id}` accepts `{ "status": "approved" }`
  or `{ "status": "rejected" }`.

Approval decisions record the authenticated subject and decision timestamp.
Job execution remains asynchronous and is handled by the durable worker path.

The React console polls tenant data every 15 seconds and supports deep links
through URL fragments (`#sessions`, `#jobs`, `#approvals`, `#optimization`,
`#monitoring` and `#simulation`). The optimization view is deliberately
observational: routing recommendations and provider quota projections do not
mutate production routing automatically.

The Orchestrator view is the visual control surface for collaboration spaces.
It does not execute arbitrary local binaries from the browser: managed agents
still connect through the configured MCP/Bridge transport, while the UI owns
space lifecycle, participant membership and tenant-scoped monitoring.

The dashboard read and simulation contracts are also represented in
[`docs/api/openapi.json`](api/openapi.json), including both API-key and OIDC
Bearer authentication schemes.

## Tenant secrets and deployment

Owners can store provider API keys, MCP credentials and other integration
secrets from Settings. `POST /api/v1/dashboard/secrets` accepts a name and
value; list responses contain metadata only and never return the value. The
default local backend encrypts values with AES-GCM using
`HARNESSMESH_SECRET_KEY`. Set `HARNESSMESH_SECRET_BACKEND=vault` together
with `VAULT_ADDR`, `VAULT_TOKEN` and `HARNESSMESH_VAULT_MOUNT` to use
HashiCorp Vault KV v2 instead.

`make local-up` starts the local Compose stack. `make vault-up` starts the
development-only Vault profile. `make k8s-validate`, `make k8s-apply` and
`make k3d-up` provide the Kubernetes/k3d path. Production clusters should
replace the included single-replica PostgreSQL StatefulSet with managed HA
PostgreSQL and use an externally managed Vault.

When `HARNESSMESH_REQUIRE_STRONG_TOKENS=1` is enabled, the MCP process fails
closed if the configured Vault backend is unavailable or local encrypted
storage has no `HARNESSMESH_SECRET_KEY`. This prevents a production control
plane from starting with a dashboard that cannot manage credentials.

For local OIDC testing, `make keycloak-up` starts the development Keycloak
profile and imports the HarnessMesh realm. Configure the introspection URL,
client ID and client secret from `.env.example` before starting MCP. The
realm is intentionally a local harness; production requires a managed realm,
TLS, trusted redirect URIs and provider-managed MFA.

## Usage and cost boundary

Usage visibility is available, including observation and job counts and the
zero-API-billing status. HarnessMesh is deliberately not a billing system: it
does not accept payments, manage subscriptions, create invoices, process
payment methods, perform plan changes or handle payment-failure webhooks. The
projection is limited to tenant-scoped usage, configured cost estimates,
budgets and operational limits.

## Production frontend container

The `harnessmesh-console` Compose profile builds a multi-stage Node/Nginx image,
serves the React build and proxies API, health and metrics requests to the
HarnessMesh control plane:

```sh
docker compose -f compose.yaml -f compose.production.yaml \
  --profile mcp --profile console up -d
```
