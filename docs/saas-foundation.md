# SaaS foundation

SaaS-facing requests must carry an authenticated identity containing a subject
and tenant. `internal/tenant` provides the shared context and fail-closed
authorization primitives:

- `tenant.WithIdentity` attaches an authenticated identity to a context.
- `tenant.RequireTenant` rejects missing identities and cross-tenant access.
- `tenant.Authorize` additionally checks a role and/or OAuth scope.

OAuth introspection now accepts `tenant`, `org_id` or `organization` claims and
normalizes `roles` and `scope`. MCP requests authenticated through introspection
carry that identity into the request context. Direct static bearer tokens remain
appropriate for local deployments, but are intentionally not treated as
multi-tenant identity.

Before SaaS launch, every persistent read/write path must consume this context;
request body, query parameter or URL tenant identifiers are selectors only and
must never establish authorization.

Knowledge records inherit the authenticated tenant on append and searches are
tenant-filtered. Tenant-authenticated statistics count only that tenant's
records, and tenant compaction removes only records owned by that tenant while
preserving every other tenant's data.

The persistent directory now contains tenants, principals, memberships and
revocable API keys. `internal/identity.Service` issues an API key only to an
active tenant member, stores only a SHA-256 hash, returns the secret once, and
re-checks membership on every authentication. API-key authentication therefore
produces the same tenant identity used by the authorization primitives.

Remote MCP clients may authenticate with the API key header:

```text
X-HarnessMesh-API-Key: hm_live_...
```

The server resolves the key to its tenant membership before processing the
request. Static bearer tokens do not create a SaaS tenant identity and should
therefore be limited to local or single-tenant deployments.

For local administration, the directory can be bootstrapped with:

```sh
bin/harnessmesh tenant create --id tenant-acme --slug acme --name Acme
bin/harnessmesh tenant member --id tenant-acme --subject user-1 --role owner
bin/harnessmesh tenant key --id tenant-acme --subject user-1
```

The last command prints the secret once. It must be stored in a secret manager
or client configuration immediately; HarnessMesh cannot recover it later.

The HTTP service exposes the same directory at `/api/v1/identity/`:

```sh
curl -H "X-HarnessMesh-API-Key: hm_live_..." \
  http://127.0.0.1:8787/api/v1/identity/me

curl -X POST \
  -H "X-HarnessMesh-API-Key: hm_live_..." \
  -H 'Content-Type: application/json' \
  -d '{"subject":"user-2","role":"member"}' \
  http://127.0.0.1:8787/api/v1/identity/tenants/tenant-acme
```

Only an authenticated `owner` may change memberships or issue keys. The API
never accepts a tenant identifier as authorization; it compares the path to
the tenant in the authenticated identity.

MCP applies both a global limit (`HARNESSMESH_MCP_RATE_LIMIT`) and, for
authenticated tenants, an independent per-tenant limit
(`HARNESSMESH_MCP_TENANT_RATE_LIMIT`). This prevents one organization from
consuming the complete transport capacity of a shared SaaS instance.
