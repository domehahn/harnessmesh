# ChatGPT MCP bootstrap

HarnessMesh can prepare its complete local side for a ChatGPT Custom MCP
connector with one command. This starts the authenticated Streamable HTTP MCP
server, performs a health check, optionally creates an HTTPS tunnel, and
writes a mode `0600` connector payload containing the URL and bearer token.

## One-command local bootstrap

Build HarnessMesh and start the bootstrap wrapper:

```bash
make build
HARNESSMESH_TUNNEL=auto ./scripts/start-chatgpt-mcp.sh
```

The wrapper can use an `ngrok` development tunnel. It does not use a
Cloudflare Quick Tunnel because Cloudflare documents that Quick Tunnels do not
support SSE. For a stable production deployment, use the Compose profile with
a named Cloudflare Tunnel or a reverse proxy and pass its HTTPS URL explicitly:

```bash
HARNESSMESH_PUBLIC_MCP_URL=https://mesh.example.com \
HARNESSMESH_TUNNEL=none \
  ./scripts/start-chatgpt-mcp.sh
```

The default config is `configs/chatgpt-claude.json`, the caller is
`chatgpt-browser`, and the repository is the current directory. Override them
with `HARNESSMESH_CONFIG`, `HARNESSMESH_REPO`, and `HARNESSMESH_BIN`.

## Connector payload

The wrapper writes `.harnessmesh/chatgpt-mcp-connector.json` unless
`HARNESSMESH_CONNECTOR_FILE` is set. It contains:

```json
{
  "name": "HarnessMesh",
  "transport": "streamable-http",
  "url": "https://mesh.example.com/mcp",
  "headers": {
    "Authorization": "Bearer <generated-token>"
  },
  "caller": "chatgpt-browser"
}
```

The file is ignored by Git and written with owner-only permissions. Do not
publish it or paste the token into logs. The server itself refuses to start
without authentication.

## What remains account-controlled

A local program cannot silently create or approve a connector in a ChatGPT
workspace. ChatGPT must still accept the HTTPS MCP endpoint under the
workspace's app/connector settings. Use the generated payload as the exact
source for the URL and bearer header. Connector availability and write-action
permissions depend on the OpenAI plan and workspace policy.

After the connector is accepted, start a normal ChatGPT conversation and
select or mention HarnessMesh. The model then calls the read/propose-oriented
HarnessMesh tools; HarnessMesh does not proxy the ChatGPT conversation or
expose the user's browser ChatGPT history.

## Shutdown and cleanup

Press `Ctrl-C` in the bootstrap process. It stops the MCP server and any
temporary tunnel it started. Remove the generated credential artifact when no
longer needed:

```bash
rm -f .harnessmesh/chatgpt-mcp-connector.json
```

For persistent deployments, use Docker Compose or a system service behind a
TLS reverse proxy and rotate `HARNESSMESH_MCP_TOKEN` after connector changes.
