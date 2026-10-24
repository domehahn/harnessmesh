# External agent plugins

HarnessMesh can load managed agent adapters from `plugins/<name>/plugin.json`.
The manifest is the admission boundary; a plugin is not started merely because
its directory exists. Only `enabled: true` manifests with a valid executable,
compatible protocol and (in production) a matching SHA-256 hash are registered
in the agent catalog.

## Minimal manifest

```json
{
  "name": "my-agent",
  "version": "1.0.0",
  "protocol": "harnessmesh-agent-v1",
  "binary": "bin/my-agent",
  "enabled": true,
  "sha256": "<sha256 of bin/my-agent>",
  "capabilities": ["review", "answer_questions"],
  "risk": "medium",
  "sandbox": {
    "network": "none",
    "timeout_seconds": 120,
    "max_message_bytes": 1048576,
    "max_output_bytes": 4194304,
    "allowed_paths": ["/workspace/repository"],
    "denied_paths": ["/workspace/repository/.git"]
  }
}
```

The process communicates through newline-delimited JSON on stdin/stdout. Each
request has `id`, `method` and `payload`; each response must return the same
`id`, `ok`, and an optional `payload` or `error`. Implement these methods:
`health`, `start_session`, `resume_session`, `close_session`, and `invoke`.
`invoke` returns `text`, optional `session_id`, `usage` and `duration_ms`.

The runtime validates the protocol, name, version range, executable location,
capabilities and risk. It verifies `sha256`; production can additionally
require Ed25519 signatures with `HARNESSMESH_PLUGIN_REQUIRE_SIGNATURE=1`.
Network isolation uses bubblewrap when `network` is `none` and fails closed if
the OS sandbox is unavailable. Message size and request timeout limits are
enforced by the host process. Plugins must never put credentials in manifests;
use tenant Secrets/Vault and pass only explicitly scoped environment values.

## Activation and operations

```sh
export HARNESSMESH_PLUGIN_DIR="$PWD/plugins"
export HARNESSMESH_PLUGIN_REQUIRE_HASH=1
export HARNESSMESH_PLUGIN_REQUIRE_SIGNATURE=1 # optional, recommended in production
make local-up
```

The dashboard Settings → Agent & MCP marketplace shows discovered plugins,
version, capabilities, risk, hash/signature state and a live health probe.
After activation, add the plugin name as an agent `kind`/`adapter` in the
normal HarnessMesh config; no Go code change is required. The adapter SDK
catalog also exposes the plugin descriptor to orchestration and routing.
Set `plugin: true` on that agent entry (as shown in the example) so config
validation explicitly opts into dynamic adapter loading.

`plugins/harnessmesh-codex` is a client marketplace integration and has no
runtime binary; it is intentionally displayed as `catalog-only` and is not
started by HarnessMesh.
