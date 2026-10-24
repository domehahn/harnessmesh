# External Client Compatibility Matrix

This is the release worksheet for clients outside the Go package boundary.
Only rows with a released binary, the exact client version, and a recorded
staging run may be marked `verified`.

| Client | Interface | Version under test | Required checks | Status |
| --- | --- | --- | --- | --- |
| ChatGPT remote connector | MCP Streamable HTTP | Record per release | initialize, protocol negotiation, auth rotation, tool discovery, replay | pending staging run |
| VS Code extension | Bridge REST + WebSocket `/api/v1` | Record extension/package version | workspace/task lifecycle, idempotency, reconnect/replay, origin policy | package-level coverage; staging pending |
| Codex CLI | Provider `/v1/models` + `/v1/responses` | Record exact CLI version | model catalog dialect, non-streaming response, SSE ordering, cancellation, auth | provider contract tests; client staging pending |
| Claude Code | local HarnessMesh CLI/MCP integration | Record exact CLI version | managed execution policy, read-only reviewer, change-control evidence | deployment-specific |

## Release evidence

For each supported version, attach the exact binary/container digest, config
profile, test timestamp, environment, request/response fixtures, and pass/fail
result. A compatibility claim is not complete when only package-level handler
tests pass. Breaking changes require a new API/protocol version or a migration
entry in [api-compatibility.md](api-compatibility.md).
