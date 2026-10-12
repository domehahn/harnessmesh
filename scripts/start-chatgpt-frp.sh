#!/usr/bin/env bash
set -Eeuo pipefail

root_dir="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root_dir"
env_file="${HARNESSMESH_CHATGPT_ENV_FILE:-$root_dir/.harnessmesh/chatgpt-frp.env}"
frpc_config="${HARNESSMESH_FRPC_CONFIG:-$root_dir/.harnessmesh/frpc.toml}"
connector_file="${HARNESSMESH_CONNECTOR_FILE:-$root_dir/.harnessmesh/chatgpt-mcp-connector.json}"
repo_path="${HARNESSMESH_REPO:-$root_dir}"
config_path="${HARNESSMESH_MCP_CONFIG:-$root_dir/configs/chatgpt-claude.json}"
frp_server="${HARNESSMESH_FRP_SERVER:-}"
frp_token="${HARNESSMESH_FRP_TOKEN:-}"
public_url="${HARNESSMESH_PUBLIC_MCP_URL:-}"
remote_port="${HARNESSMESH_FRP_REMOTE_PORT:-8787}"

command -v docker >/dev/null 2>&1 || { echo "docker is required." >&2; exit 2; }
docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 is required." >&2; exit 2; }
command -v openssl >/dev/null 2>&1 || { echo "openssl is required." >&2; exit 2; }
[[ -n "$frp_server" ]] || { echo "HARNESSMESH_FRP_SERVER must be set." >&2; exit 2; }
[[ -n "$frp_token" ]] || { echo "HARNESSMESH_FRP_TOKEN must be set." >&2; exit 2; }
[[ "$public_url" == https://* ]] || { echo "HARNESSMESH_PUBLIC_MCP_URL must be an https:// URL." >&2; exit 2; }
[[ -d "$repo_path" ]] || { echo "Repository does not exist: $repo_path" >&2; exit 2; }
[[ -f "$config_path" ]] || { echo "Config does not exist: $config_path" >&2; exit 2; }

token="${HARNESSMESH_MCP_TOKEN:-$(openssl rand -hex 32)}"
mkdir -p "$(dirname "$env_file")" "$(dirname "$frpc_config")" "$(dirname "$connector_file")"
umask 077

cat > "$frpc_config" <<EOF
serverAddr = "$frp_server"
serverPort = 7000

[auth]
method = "token"
token = "$frp_token"

[[proxies]]
name = "harnessmesh-mcp"
type = "tcp"
localIP = "harnessmesh-mcp"
localPort = 8787
remotePort = $remote_port
EOF
chmod 600 "$frpc_config"

cat > "$env_file" <<EOF
HARNESSMESH_REPO=$repo_path
HARNESSMESH_MCP_CONFIG=$config_path
HARNESSMESH_MCP_TOKEN=$token
HARNESSMESH_FRPC_CONFIG=$frpc_config
HARNESSMESH_PUBLIC_MCP_URL=$public_url
EOF
chmod 600 "$env_file"

docker compose --env-file "$env_file" --profile chatgpt-frp up -d harnessmesh-mcp harnessmesh-frpc

for _ in {1..30}; do
  if curl -fsS --max-time 3 "$public_url/healthz" >/dev/null 2>&1; then break; fi
  sleep 2
done
if ! curl -fsS --max-time 5 "$public_url/healthz" >/dev/null 2>&1; then
  echo "Public MCP endpoint did not become healthy: $public_url/healthz" >&2
  docker compose --env-file "$env_file" logs --no-color --tail=80 harnessmesh-mcp harnessmesh-frpc >&2 || true
  exit 1
fi

python3 - "$connector_file" "$token" "$public_url" <<'PY'
import json, os, sys
path, token, url = sys.argv[1:]
with open(path, "w", encoding="utf-8") as f:
    json.dump({"name":"HarnessMesh","transport":"streamable-http","url":url.rstrip("/")+"/mcp","headers":{"Authorization":"Bearer "+token},"caller":"chatgpt-browser","generated_by":"scripts/start-chatgpt-frp.sh"}, f, indent=2)
    f.write("\n")
os.chmod(path, 0o600)
PY

echo "HarnessMesh MCP frp stack is healthy: $public_url/mcp"
echo "Connector payload: $connector_file"
echo "Secret environment file: $env_file"
echo "Stop with: ./scripts/stop-chatgpt-frp.sh"
