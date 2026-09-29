# Security & Context Protection

HarnessMesh implements conservative data minimization and path filtering to ensure secrets, tokens, and private repositories are never accidentally transmitted between backends or logged.

## Automatic Secret Filtering

The context projector automatically filters out common secret file patterns:
- `.env`, `.env.*`
- `*.pem`, `*.key`, `*.p12`, `*.pfx`
- `id_rsa`, `id_rsa.*`, `id_ed25519`, `id_ed25519.*`
- `secrets/**`, `credentials/**`
- `terraform.tfstate`, `*.tfstate`, `*.tfstate.backup`
- `.git/**`, `.harnessmesh/**`

Any git diff hunk or status entry referencing these files is stripped prior to projection.

## Path Traversal & Symlink Escape Prevention

- **Path Traversal**: Any attempt to access files outside the repository root via `../` is rejected with `ContextRejectedError`.
- **Symlink Escapes**: Symlinks inside the repository pointing to files outside the repository root are detected using `filepath.EvalSymlinks` and rejected.

## Redaction in Logs and Status

- Prompts, raw tool arguments, and secret file contents are never written to unredacted log sinks.
- Agent environment variables matching `KEY`, `TOKEN`, `SECRET`, or `PASSWORD` are automatically masked as `<redacted>` in diagnostic outputs.

## Trust model

- By default, the HarnessMesh MCP server communicates over standard I/O (stdio) locally and no network ports are opened.
- The Streamable HTTP transport (`/mcp`) and the local VS Code bridge (`/api/v1/*`) are opt-in. When enabled, both bind to `127.0.0.1` by default; binding to `0.0.0.0` requires an explicit `--listen 0.0.0.0:<port>` (or equivalent config), which is a deliberate, auditable choice, never the default.
- Neither transport is ever served unauthenticated: `mcp serve --listen` refuses to start without a bearer token or OAuth introspection URL configured, and `bridge serve` refuses to start without a bearer token. See [docs/mcp.md](mcp.md) and [docs/chatgpt-integration.md](chatgpt-integration.md).
- A remote peer connecting over `/mcp` (e.g. ChatGPT) is authenticated the same way as any other caller and is still subject to every authorization check below - remote reachability is not a bypass of the local trust boundaries.

## Agent Sandbox Policies

Adapters may use `executil.SandboxPolicy` to restrict executable names and working directories. Production deployments should provide explicit command and path allowlists, deny secret directories, and run the harness process under a dedicated OS user or container.

## Remote MCP

Remote MCP supports bearer tokens, OAuth2 token introspection, rate limits, and native TLS certificates (or a reverse proxy / secure tunnel in front, for production - see [docs/chatgpt-integration.md](chatgpt-integration.md#production-deployment)). Expose it only behind network policy, rotate credentials, and monitor `/metrics` and the authenticated admin endpoints.

CORS on `/mcp` and on the VS Code bridge's `/api/v1/*` is a strict, explicit origin allowlist; neither ever sets `Access-Control-Allow-Origin: *`. An empty allowlist (the default) allows only requests with no `Origin` header at all (non-browser clients, e.g. ChatGPT's server-side MCP client or the VS Code extension host process).

## Credit isolation (ChatGPT bridge)

An `execution_mode: external` participant (the ChatGPT-browser role) can never resolve to a metered backend (the OpenAI API or the Codex CLI): this is enforced at config-load time (`internal/creditguard`, config validation rejects such a configuration outright) and is provable at runtime via `harnessmesh_metered_backend_calls_total{backend=...}` in `/metrics`, which must stay at zero across any ChatGPT-bridge workflow. See [docs/chatgpt-integration.md](chatgpt-integration.md#credit-isolation) for the full guarantee and its test coverage.

## Single-writer invariant

At most one participant may be configured `writable: true` (rejected at config-validation time if violated). MeshCommit's `change.create` and the commit path additionally re-check, at runtime, that the acting participant is writable and managed (not `execution_mode: external`) - the invariant does not rely solely on which tools happen to be wired up in a given transport.

## Threat model summary

| Threat | Mitigation |
| :--- | :--- |
| Malicious repository content / prompt injection via file contents | Content from files, tool output, and other participants is transported as opaque payload data; it is never interpreted as HarnessMesh control input, and MCP tool schemas separate control fields (caller identity, IDs) from content fields. |
| Compromised VS Code extension or stolen bridge/MCP bearer token | Token grants only what the associated participant's config allows (authorization is participant-scoped, not just "authenticated"); rotate via reissuing the token and restarting the server. |
| Malicious/careless remote MCP caller (e.g. misconfigured ChatGPT connector) | Caller-identity spoofing is rejected on every mutating tool; single-writer and credit-isolation invariants are enforced independent of caller-supplied fields. |
| Cross-workspace data leakage | Space/session-scoped queries; a caller must supply the specific `space_id`/`session_id` it has legitimate access to - there is no unscoped "list everything" tool. |
| Path traversal / symlink escape / secret exfiltration | See Path Traversal & Symlink Escape Prevention and Automatic Secret Filtering above; covered by tests under `internal/contextpack`. |
| Remote command execution / privilege escalation via ChatGPT | `execution_mode: external` participants are never built into the invocable harness map and are rejected by `Ask`/`Converse`/`change.create` at runtime - there is no code path from an external participant to `exec.Command`. |
| Replay / duplicate mutation from network retries | Idempotency-key support on mutating MCP tools and bridge REST endpoints. |
| Denial of service | Rate limiting, request body size caps, bounded WebSocket send queues (slow consumers are disconnected, not buffered without bound). |
| Confused deputy (a passive participant tricking a managed executor into acting) | Managed executors act on explicit, auditable task/change records, not on arbitrary inbound messages; MeshCommit's independent-review invariant additionally forbids self-review. |
