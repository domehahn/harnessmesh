#!/usr/bin/env bash
set -Eeuo pipefail

# Automates the local Compose side of the production ChatGPT MCP path. A
# named Cloudflare Tunnel is required because Quick Tunnels do not support
# SSE. The one-time Cloudflare account/domain setup cannot be performed by a
# local script without account credentials and is validated here.

root_dir="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root_dir"
env_file="${HARNESSMESH_CHATGPT_ENV_FILE:-$root_dir/.harnessmesh/chatgpt-compose.env}"
connector_file="${HARNESSMESH_CONNECTOR_FILE:-$root_dir/.harnessmesh/chatgpt-mcp-connector.json}"
repo_path="${HARNESSMESH_REPO:-$root_dir}"
config_path="${HARNESSMESH_MCP_CONFIG:-$root_dir/configs/chatgpt-claude.json}"
public_url="${HARNESSMESH_PUBLIC_MCP_URL:-}"
tunnel_token="${HARNESSMESH_CLOUDFLARED_TUNNEL_TOKEN:-}"

command -v docker >/dev/null 2>&1 || { echo "docker is required." >&2; exit 2; }
docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 is required." >&2; exit 2; }
[[ -d "$repo_path" ]] || { echo "Repository does not exist: $repo_path" >&2; exit 2; }
[[ -f "$config_path" ]] || { echo "Config does not exist: $config_path" >&2; exit 2; }
[[ -n "$public_url" ]] || { echo "HARNESSMESH_PUBLIC_MCP_URL must be set to the named tunnel hostname." >&2; exit 2; }
[[ "$public_url" == https://* ]] || { echo "HARNESSMESH_PUBLIC_MCP_URL must use https://." >&2; exit 2; }
[[ -n "$tunnel_token" ]] || { echo "HARNESSMESH_CLOUDFLARED_TUNNEL_TOKEN must be set." >&2; exit 2; }

command -v openssl >/dev/null 2>&1 || { echo "openssl is required." >&2; exit 2; }
token="${HARNESSMESH_MCP_TOKEN:-$(openssl rand -hex 32)}"
mkdir -p "$(dirname "$env_file")" "$(dirname "$connector_file")"
umask 077

cat > "$env_file" <<EOF
HARNESSMESH_REPO=$repo_path
HARNESSMESH_MCP_CONFIG=$config_path
HARNESSMESH_MCP_TOKEN=$token
HARNESSMESH_PUBLIC_MCP_URL=$public_url
HARNESSMESH_CLOUDFLARED_TUNNEL_TOKEN=$tunnel_token
EOF
chmod 600 "$env_file"

docker compose --env-file "$env_file" --profile chatgpt up -d harnessmesh-mcp harnessmesh-chatgpt-tunnel

for _ in {1..30}; do
  if curl -fsS --max-time 3 "$public_url/healthz" >/dev/null 2>&1; then break; fi
  sleep 2
done
if ! curl -fsS --max-time 5 "$public_url/healthz" >/dev/null 2>&1; then
  echo "Public MCP endpoint did not become healthy: $public_url/healthz" >&2
  docker compose --env-file "$env_file" logs --no-color --tail=80 harnessmesh-mcp harnessmesh-chatgpt-tunnel >&2 || true
  exit 1
fi

python3 - "$connector_file" "$token" "$public_url" <<'PY'
import json, os, sys
path, token, url = sys.argv[1:]
with open(path, "w", encoding="utf-8") as f:
    json.dump({"name":"HarnessMesh","transport":"streamable-http","url":url.rstrip("/")+"/mcp","headers":{"Authorization":"Bearer "+token},"caller":"chatgpt-browser","generated_by":"scripts/start-chatgpt-compose.sh"}, f, indent=2)
    f.write("\n")
os.chmod(path, 0o600)
PY

echo "HarnessMesh MCP Compose stack is healthy: $public_url/mcp"
echo "Connector payload: $connector_file"
echo "Secret environment file: $env_file"
echo "Stop with: ./scripts/stop-chatgpt-compose.sh"
