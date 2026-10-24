# External API Compatibility

This document is the compatibility baseline for the current production
candidate. The public transports are versioned independently where their
protocols require it.

The machine-readable HTTP baseline is [openapi.json](api/openapi.json). It is
intentionally a stable transport contract; MCP tool schemas remain generated
from the server's negotiated MCP definitions.
The release worksheet for external clients is
[external-client-matrix.md](external-client-matrix.md).

Static bearer tokens remain supported for local/single-node deployments. MCP
and Bridge may additionally validate bearer tokens through RFC 7662 OAuth
introspection; Bridge's optional required-scope gate is applied after
introspection.

## Compatibility matrix

| Interface | Current version | Compatibility rule | Breaking-change path |
| --- | --- | --- | --- |
| Bridge REST | `/api/v1` | Existing fields and routes remain backward compatible within v1. Unknown request fields are rejected. | Add `/api/v2`; do not silently change v1 semantics. |
| Bridge WebSocket | `/api/v1/events/ws` | Event envelopes retain stable event names, IDs, timestamps, and replay semantics. | Add a new event/API version and retain replay support for v1. |
| MCP | MCP protocol negotiation plus HarnessMesh tool schemas | Negotiate supported MCP protocol revisions; tool names and required arguments are stable within the supported revision. | Add supported protocol revision or a new tool; remove only after deprecation. |
| Provider Responses | `/v1/responses` | Responses API request/response and SSE lifecycle are preserved; unsupported fields fail explicitly. | Preserve `/v1`; introduce a separate provider API version for incompatible wire changes. |
| Provider models | `/v1/models` | Generic OpenAI model shape and Codex client catalog dialect remain stable for supported client versions. | Add catalog dialect support; never silently remove a model advertised to an active client. |
| SQLite schema | Internal migration version | Migrations are forward-applied transactionally and recorded in `schema_migrations`. | Ship a tested migration and rollback/backup procedure before release. |
| Knowledge archive | Archive format/version header and checksum | Restore verifies structure and checksums before replacing active data. | Add format migration or a documented export/import conversion. |

## Contract rules

- Every mutating REST request should use `Idempotency-Key` when a caller may
  retry after a timeout.
- Authentication failures and policy failures remain distinguishable from
  malformed requests and upstream/provider failures.
- A change to a persisted protocol type requires a migration or an explicit
  compatibility test.
- New required fields are prohibited in a live v1 contract; add optional
  fields or a new version.
- Error responses must preserve a stable machine-readable error class where a
  client needs to retry, re-authenticate, or request operator intervention.
- External clients must be tested against the exact released binary, not only
  against package-level handlers.

## Release checklist

- [ ] Update this matrix for every route/tool/catalog change.
- [ ] Add a contract test for every new or changed request/response shape.
- [ ] Run SQLite migration tests from the previous release fixture.
- [ ] Run provider model-catalog compatibility against the supported Codex
  client versions.
- [ ] Verify rollback from a backup made immediately before migration.
- [ ] Record deprecated fields/routes and their removal release.
