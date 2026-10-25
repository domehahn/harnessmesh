# Managed Marketplace

The dashboard marketplace is a tenant-scoped lifecycle for local, policy-bound
agent plugins. It is intentionally compatible with Codex-style manifests where
possible, but it does not download arbitrary remote executables.

## Lifecycle

1. Register a local source directory (`source_register`).
2. Search the discovered manifests with `GET /api/v1/dashboard/marketplace?q=`.
3. Install or update a plugin (`install` / `update`). The manifest is checked
   before the directory is atomically promoted into `plugins/<name>`.
4. Approve the installation (`approve`).
5. Activate it for the authenticated tenant (`activate`).
6. Deactivate, uninstall, or rollback when required.

All lifecycle mutations require the tenant owner role. They are persisted in
the configured SQLite/PostgreSQL store and emitted as `marketplace.*`
control-plane events. The file fallback is used only when an older custom
Store adapter does not implement the marketplace persistence interface.
Runtime admission still applies the plugin protocol, version, executable,
hash, signature, sandbox and resource-limit checks. Production deployments
should set `HARNESSMESH_PLUGIN_REQUIRE_HASH=1` and
`HARNESSMESH_PLUGIN_REQUIRE_SIGNATURE=1` with trusted Ed25519 public keys.

The UI exposes the catalog, source registration, installation, approval,
activation, update, rollback and removal controls. A plugin is never started
merely because it appears in a catalog; it must be installed, approved,
activated and then pass the runtime admission checks.

## API actions

Authenticated owner requests are sent as JSON to:

```text
POST /api/v1/dashboard/marketplace/source_register
POST /api/v1/dashboard/marketplace/source_remove
POST /api/v1/dashboard/marketplace/install
POST /api/v1/dashboard/marketplace/update
POST /api/v1/dashboard/marketplace/approve
POST /api/v1/dashboard/marketplace/activate
POST /api/v1/dashboard/marketplace/deactivate
POST /api/v1/dashboard/marketplace/rollback
POST /api/v1/dashboard/marketplace/uninstall
```

Example:

```json
{"plugin":"my-agent","source_id":"local"}
```

The current repository's `harnessmesh-codex` entry remains a catalog-only
Codex client plugin. It can be registered with Codex independently and is not
an executable HarnessMesh runtime adapter.

## Supply-chain and HA configuration

Local development can start the S3-compatible MinIO service with:

```sh
make marketplace-storage-up
```

The S3 adapter uses SigV4 through the AWS SDK and supports AWS S3, MinIO and
Cloudflare R2 by changing only the endpoint, bucket, region and credentials:
`HARNESSMESH_MARKETPLACE_ARTIFACT_ENDPOINT`,
`HARNESSMESH_MARKETPLACE_BUCKET`, `HARNESSMESH_MARKETPLACE_REGION`,
`HARNESSMESH_OBJECTSTORE_ACCESS_KEY` and
`HARNESSMESH_OBJECTSTORE_SECRET_KEY`. Bucket creation, IAM policy and CDN
distribution remain infrastructure concerns and should be provisioned by
Terraform/CloudFormation or the hyperscaler; the application never creates a
public bucket implicitly.

The production equivalent is configured through
`HARNESSMESH_MARKETPLACE_ARTIFACT_ENDPOINT` and
`HARNESSMESH_MARKETPLACE_CDN_BASE_URL`, pointing at an S3/R2/MinIO gateway and
its CDN. Artifact keys are content-addressed and uploaded atomically. The
PostgreSQL store provides the shared marketplace state and distributed lease
boundary for multiple MCP replicas.

Set `HARNESSMESH_MARKETPLACE_REQUIRE_SCANS=1` and configure Syft, Grype or
Trivy, ClamAV and optionally `skil` binaries for fail-closed admission. When
configured, `skil scan --static-only` rejects prompt-injection, poisoned-tool,
secret-boundary and other AI-skill security findings before promotion; it never
executes the scanned content. Registry indexes are
Ed25519 signed; rotate keys before revoking the old key and keep the trusted
public-key set in Vault/KMS-managed deployment configuration.

Dependency cycles, missing constraints and explicit conflicts are rejected
before an installation is promoted. The same admission contract is used for
local source directories and remote artifact gateways.

Every successful install/update publishes a versioned `plugin.tar` artifact
and updates the registry index. The authenticated endpoint
`GET /api/v1/dashboard/marketplace/registry` exposes the index for compatible
clients. Set `HARNESSMESH_MARKETPLACE_REQUIRE_INDEX_SIGNATURE=1` together with
the Vault-injected Ed25519 key and key ID to fail closed unless the complete
index is signed.

Install/update stages emit durable `marketplace.progress` events (`started`,
`staged`, `scanned`, `completed` or `failed`) so operators can correlate
progress and failures with the control-plane event stream. Set
`HARNESSMESH_MARKETPLACE_MAX_ACTIVE_PER_TENANT` to enforce an activation quota;
`0` disables that limit.

Tenant-authorized session replay plans are available at
`GET /api/v1/dashboard/sessions/{session_id}/replay`. They contain only
redacted, ordered events and a SHA-256 digest; they are analysis artifacts and
never execute agents, tools or providers.

Runtime communication is protected separately from static admission. With
`HARNESSMESH_RUNTIME_SECURITY=1`, Ask, Review, Converse and collaboration
messages pass through the HarnessMesh security gate: recognized credentials
are redacted, high-confidence instruction-override/secret-exfiltration
patterns are blocked, and redaction/block decisions are emitted as
`security.redaction`/`security.blocked` events without recording message
content.

The monitoring endpoint `GET /api/v1/dashboard/assurance/graph` exposes the
non-secret declared capability graph for agents and MCP servers, including
content digests and declared edges. Security decisions carry a digest-bound
evidence object; set `HARNESSMESH_REQUIRE_SIGNED_EVIDENCE=1` and inject the
assurance Ed25519 key to fail closed when evidence cannot be signed. The
reviewed policy format is demonstrated in
`configs/assurance-policy.example.yaml` and can be selected with
`HARNESSMESH_POLICY_FILE`.

The tenant-scoped endpoint `GET /api/v1/dashboard/assurance/events` returns
the most recent structured `security.*` events from the tenant's sessions.
It includes event type, session, timestamp, risk score and a safe reason or
finding, but never exposes raw prompts or tool arguments. The React console
renders both endpoints under Monitoring as the Security assurance graph and
Security evidence panels. A missing event stream is displayed as an empty
state; it does not weaken the runtime fail-closed decision path.

For organizations using Rego, set `HARNESSMESH_OPA_BIN` and optionally
`HARNESSMESH_OPA_PACKAGE` (default `data.harnessmesh.allow`). HarnessMesh
passes only structured, redacted decision input to a local OPA process and
fails closed on process, parse or policy errors. Repeated blocked runtime
decisions quarantine the source agent for the configured duration; the graph
reports both observed invocation counts and active quarantine entries.

MCP tool-description drift can be blocked with
`HARNESSMESH_MCP_TOOL_LOCK=/run/harnessmesh/mcp-tools.lock.json`. Generate a
reviewed lock with `mcp.GenerateToolLock` during provisioning; HarnessMesh
hashes each tool name, description and input schema and refuses both listing
and invocation when a tool changes or disappears.

MCP `tools/call` arguments also pass through the runtime security gate before
dispatch. Credential-like values are redacted, configured YAML/OPA policies
can deny the operation, and blocked calls never reach the gateway or local
tool handler.

For deployments that allow external source directories, set
`HARNESSMESH_MARKETPLACE_SOURCE_ROOT` to the explicit parent directory. Source
registration is rejected outside that root.
