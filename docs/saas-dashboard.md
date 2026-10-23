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
- `GET /api/v1/dashboard/approvals?status=...`
- `GET /api/v1/dashboard/usage`
- `GET /api/v1/dashboard/optimization` returns tenant-scoped usage buckets by
  agent, model and session, persisted token/cost/duration data, agent health,
  and an explicit provider-quota availability state.
- `GET /api/v1/dashboard/health`

Every response is derived from the authenticated tenant context. Tenant IDs in
URLs or request bodies are not trusted for authorization.

## Write APIs

- `POST /api/v1/dashboard/jobs` submits a durable job.
- `POST /api/v1/dashboard/approvals/{id}` accepts `{ "status": "approved" }`
  or `{ "status": "rejected" }`.

Approval decisions record the authenticated subject and decision timestamp.
Job execution remains asynchronous and is handled by the durable worker path.

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
