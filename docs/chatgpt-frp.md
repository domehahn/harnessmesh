# ChatGPT MCP with self-hosted frp

HarnessMesh can use a completely self-hosted `frp` tunnel instead of
Cloudflare. The local Compose profile runs `frpc`; a VPS runs `frps` and Caddy.
Caddy terminates HTTPS and forwards `/mcp` traffic to the frp TCP port.

```text
ChatGPT
   │ HTTPS/SSE
   ▼
Caddy on your VPS
   │ TCP :8787
   ▼
frps on your VPS
   │ encrypted/authenticated frp connection
   ▼
frpc in HarnessMesh Compose
   ▼
harnessmesh-mcp
```

The project includes the VPS templates in [`deploy/`](../deploy/).

## VPS setup

1. Copy `deploy/frp-server.compose.yaml`, `deploy/frps.toml`, and
   `deploy/Caddyfile` to a VPS.
2. Replace the token in `frps.toml` with a long random value.
3. Set `HARNESSMESH_MCP_DOMAIN` to a DNS name pointing to the VPS.
4. Start the server:

   ```bash
   HARNESSMESH_MCP_DOMAIN=mesh.example.com \
     docker compose -f frp-server.compose.yaml up -d
   ```

Allow TCP ports `80`, `443`, `7000`, and `8787` according to your firewall
policy. Port `7000` is the frp control connection; Caddy exposes only HTTPS
publicly. The token in the VPS `frps.toml` must match `HARNESSMESH_FRP_TOKEN`
on the local machine.

## Local HarnessMesh setup

```bash
export HARNESSMESH_FRP_SERVER="mesh.example.com"
export HARNESSMESH_FRP_TOKEN="<same-token-as-frps.toml>"
export HARNESSMESH_PUBLIC_MCP_URL="https://mesh.example.com"

make chatgpt-frp-up
```

The script creates the local MCP token, `frpc.toml`, a protected environment
file, starts the Compose profile, waits for the public health endpoint, and
writes `.harnessmesh/chatgpt-mcp-connector.json`.

Stop it with:

```bash
make chatgpt-frp-down
```

Unlike a hosted tunnel service, frp requires a VPS, DNS, TLS, firewall rules,
and one shared tunnel token. After that one-time infrastructure setup, the
local HarnessMesh start is fully automated.
