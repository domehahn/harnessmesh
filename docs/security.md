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

## Local-First Trust Model

- The HarnessMesh MCP server communicates over standard I/O (stdio) locally.
- No network ports are opened by default.
- If network listeners are configured, they bind strictly to `127.0.0.1`.

## Agent Sandbox Policies

Adapters may use `executil.SandboxPolicy` to restrict executable names and working directories. Production deployments should provide explicit command and path allowlists, deny secret directories, and run the harness process under a dedicated OS user or container.

## Remote MCP

Remote MCP supports bearer tokens, OAuth2 token introspection, rate limits, and native TLS certificates. Expose it only behind network policy, rotate credentials, and monitor `/metrics` and the authenticated admin endpoints.
