# MCP Registry and Integration Policy

HarnessMesh treats external MCP servers as untrusted extensions. A server must
be declared under `mcp.servers` and enabled explicitly before a transport
adapter may connect to it. Production profiles should set `mcp.default_deny`
to `true` and require a named policy for every call.

Validate and inspect the registry with:

```sh
harnessmesh mcp registry validate --config harnessmesh.json
harnessmesh mcp registry list --config harnessmesh.json
harnessmesh mcp registry check --config harnessmesh.json \
  --server serena --tool find_symbol --policy developer-readonly
```

The registry provides the control plane for the optional P1–P3 integrations:

- P1: allowlists, deny lists, approval gates, rate limits, network mode and
  explicit local/remote transport declarations.
- P2: local code intelligence can be exposed through Serena/Semble-compatible
  adapters; `harnessmesh code search` and `harnessmesh code impact` provide a
  dependency-free baseline for Go repositories.
- P2: mail/webhook adapters should expose only read and draft operations by
  default. Sending, deleting, browser actions and external side effects must
  require a separate policy and human approval.
- P3: browser, Home Assistant, LegalMCP and Remotion integrations are
  intentionally optional. They use the same registry boundary and should be
  enabled only with domain allowlists, isolated runtime networking, short
  credentials, rate limits and audit logging.

The current MCP server includes a guarded remote gateway. Enabled remote tools
are discovered during `mcp serve` startup and are exposed with names in the
form `server__tool`. Calls are forwarded only after the registry and named
policy allow them. Configure the active policy with
`HARNESSMESH_MCP_GATEWAY_POLICY`; set `HARNESSMESH_MCP_GATEWAY_STRICT=1` to
fail startup when an enabled upstream cannot be discovered. Remote bearer
tokens are read from the configured `token_env` environment variable.

The registry remains an admission layer, not a process supervisor. Local MCP
servers are not spawned implicitly; an explicit process supervisor/container
must launch them with the configured sandbox and network policy.
