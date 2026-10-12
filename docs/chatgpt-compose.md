# ChatGPT MCP with Docker Compose

The `chatgpt` Compose profile starts HarnessMesh MCP and a stable Cloudflare
Tunnel together. It generates the MCP bearer token, writes the private Compose
environment file, waits for the public health endpoint, and creates the
ChatGPT connector payload.

## One-time external setup

Create a remotely managed, named Cloudflare Tunnel and route its hostname to
the Compose service. Cloudflare's Quick Tunnels are not suitable here:
Cloudflare documents that they are for development, use an ephemeral hostname,
and do not support Server-Sent Events (SSE). A named tunnel is required for
stable production MCP access. See the [Cloudflare Tunnel documentation](https://developers.cloudflare.com/tunnel/).

Once the named tunnel exists, configure its token and public hostname in the
shell or secret manager:

```bash
export HARNESSMESH_CLOUDFLARED_TUNNEL_TOKEN="<named-tunnel-token>"
export HARNESSMESH_PUBLIC_MCP_URL="https://mesh.example.com"
```

The token and URL are the only infrastructure values that cannot safely be
invented by HarnessMesh. The token is account-scoped and the hostname must be
owned or controlled by you.

## Start everything

```bash
make chatgpt-compose-up
```

Equivalent direct command:

```bash
./scripts/start-chatgpt-compose.sh
```

The script automatically:

- creates a random MCP bearer token unless one is supplied;
- writes `.harnessmesh/chatgpt-compose.env` with mode `0600`;
- starts the `harnessmesh-mcp` and `harnessmesh-chatgpt-tunnel` services;
- waits for the public `/healthz` endpoint;
- writes `.harnessmesh/chatgpt-mcp-connector.json` with mode `0600`.

The repository and config can be overridden:

```bash
HARNESSMESH_REPO=/path/to/project \
HARNESSMESH_MCP_CONFIG=/path/to/project/configs/chatgpt-claude.json \
  make chatgpt-compose-up
```

## Stop everything

```bash
make chatgpt-compose-down
```

The generated secret files remain locally so a restart can reuse the same
connector credentials. Remove them explicitly when rotating or uninstalling:

```bash
rm -f .harnessmesh/chatgpt-compose.env \
      .harnessmesh/chatgpt-mcp-connector.json
```

ChatGPT-side connector creation and workspace approval remain account-level
actions. Use the generated connector JSON as the exact URL and Authorization
configuration. HarnessMesh cannot silently register an app in an OpenAI
workspace without an account-level API and administrator authorization.
