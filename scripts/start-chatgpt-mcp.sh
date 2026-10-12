#!/usr/bin/env bash
set -Eeuo pipefail

# Starts the HarnessMesh remote MCP surface for a ChatGPT connector. This
# automates every local step; ChatGPT-side connector creation remains an
# account/workspace action and is represented by the generated connector file.
#
# Environment:
#   HARNESSMESH_BIN             binary (default: ./bin/harnessmesh)
#   HARNESSMESH_REPO            repository exposed to HarnessMesh (default: .)
#   HARNESSMESH_CONFIG          config (default: configs/chatgpt-claude.json)
#   HARNESSMESH_MCP_LISTEN      local listen address (default: 127.0.0.1:8787)
#   HARNESSMESH_PUBLIC_MCP_URL  existing HTTPS URL; skips tunnel startup
#   HARNESSMESH_TUNNEL          auto|ngrok|none (default: auto)
#   HARNESSMESH_MCP_TOKEN       explicit token; generated when absent
#   HARNESSMESH_CONNECTOR_FILE  output JSON (default: .harnessmesh/chatgpt-mcp-connector.json)

root_dir="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root_dir"

bin_path="${HARNESSMESH_BIN:-$root_dir/bin/harnessmesh}"
repo_path="${HARNESSMESH_REPO:-$root_dir}"
config_path="${HARNESSMESH_CONFIG:-$root_dir/configs/chatgpt-claude.json}"
listen_addr="${HARNESSMESH_MCP_LISTEN:-127.0.0.1:8787}"
public_url="${HARNESSMESH_PUBLIC_MCP_URL:-}"
tunnel_mode="${HARNESSMESH_TUNNEL:-auto}"
connector_file="${HARNESSMESH_CONNECTOR_FILE:-$root_dir/.harnessmesh/chatgpt-mcp-connector.json}"
token="${HARNESSMESH_MCP_TOKEN:-}"

if [[ ! -x "$bin_path" ]]; then
  echo "HarnessMesh binary not found or not executable: $bin_path" >&2
  echo "Build it first with: make build" >&2
  exit 2
fi
if [[ ! -d "$repo_path" ]]; then
  echo "Repository does not exist: $repo_path" >&2
  exit 2
fi
if [[ ! -f "$config_path" ]]; then
  echo "HarnessMesh config does not exist: $config_path" >&2
  exit 2
fi
if [[ -z "$token" ]]; then
  if ! command -v openssl >/dev/null 2>&1; then
    echo "openssl is required when HARNESSMESH_MCP_TOKEN is not set." >&2
    exit 2
  fi
  token="$(openssl rand -hex 32)"
fi

case "$tunnel_mode" in
  auto|ngrok|none) ;;
  *) echo "HARNESSMESH_TUNNEL must be auto, ngrok, or none." >&2; exit 2 ;;
esac

mkdir -p "$(dirname "$connector_file")"
umask 077

# Keep generated credentials out of the repository and out of the server
# command line. The connector artifact is intentionally mode 0600.
python3 - "$connector_file" "$token" "$public_url" <<'PY'
import json
import os
import sys

path, token, url = sys.argv[1:]
payload = {
    "name": "HarnessMesh",
    "transport": "streamable-http",
    "url": url.rstrip("/") + "/mcp" if url else "",
    "headers": {"Authorization": "Bearer " + token},
    "caller": "chatgpt-browser",
    "generated_by": "scripts/start-chatgpt-mcp.sh",
}
with open(path, "w", encoding="utf-8") as f:
    json.dump(payload, f, indent=2)
    f.write("\n")
os.chmod(path, 0o600)
PY

mcp_pid=""
tunnel_pid=""
cleanup() {
  set +e
  [[ -n "$tunnel_pid" ]] && kill "$tunnel_pid" 2>/dev/null || true
  [[ -n "$mcp_pid" ]] && kill "$mcp_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

HARNESSMESH_MCP_TOKEN="$token" "$bin_path" mcp serve \
  --repo "$repo_path" \
  --config "$config_path" \
  --caller chatgpt-browser \
  --listen "$listen_addr" &
mcp_pid=$!

listen_host="${listen_addr%:*}"
listen_port="${listen_addr##*:}"
if [[ "$listen_host" == "0.0.0.0" || "$listen_host" == "::" ]]; then
  health_host="127.0.0.1"
else
  health_host="$listen_host"
fi
health_url="http://${health_host}:${listen_port}/healthz"
for _ in {1..40}; do
  if curl -fsS --max-time 2 "$health_url" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$mcp_pid" 2>/dev/null; then
    echo "HarnessMesh MCP server exited before becoming healthy." >&2
    exit 1
  fi
  sleep 0.25
done
if ! curl -fsS --max-time 2 "$health_url" >/dev/null 2>&1; then
  echo "HarnessMesh MCP server did not become healthy: $health_url" >&2
  exit 1
fi

if [[ -z "$public_url" && "$tunnel_mode" != "none" ]]; then
  if [[ "$tunnel_mode" == "auto" && -x "$(command -v ngrok 2>/dev/null || true)" ]]; then
    tunnel_mode=ngrok
  elif [[ "$tunnel_mode" == "auto" ]]; then
    echo "No SSE-capable development tunnel found; set HARNESSMESH_PUBLIC_MCP_URL or use make chatgpt-compose-up with a named Cloudflare Tunnel." >&2
    exit 2
  fi

  tunnel_log="$(mktemp "${TMPDIR:-/tmp}/harnessmesh-chatgpt-tunnel.XXXXXX")"
  ngrok http "${listen_addr##*:}" --log=stdout >"$tunnel_log" 2>&1 &
  tunnel_pid=$!
  for _ in {1..80}; do
    public_url="$(curl -fsS --max-time 2 http://127.0.0.1:4040/api/tunnels 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); print(next((x["public_url"] for x in d.get("tunnels",[]) if x.get("public_url","").startswith("https://")), "") )' 2>/dev/null || true)"
    [[ -n "$public_url" ]] && break
    sleep 0.25
  done
  if [[ -z "$public_url" ]]; then
    echo "Tunnel did not provide an HTTPS URL." >&2
    sed -n '1,80p' "$tunnel_log" >&2
    exit 1
  fi
  python3 - "$connector_file" "$token" "$public_url" <<'PY'
import json, os, sys
path, token, url = sys.argv[1:]
with open(path, "w", encoding="utf-8") as f:
    json.dump({"name":"HarnessMesh","transport":"streamable-http","url":url.rstrip("/")+"/mcp","headers":{"Authorization":"Bearer "+token},"caller":"chatgpt-browser","generated_by":"scripts/start-chatgpt-mcp.sh"}, f, indent=2)
    f.write("\n")
os.chmod(path, 0o600)
PY
fi

echo "HarnessMesh MCP is healthy: $health_url"
echo "Public MCP URL: ${public_url:-not configured (local-only)}"
echo "Connector payload: $connector_file"
echo "Keep this process running while ChatGPT uses the connector. Press Ctrl-C to stop it."
wait "$mcp_pid"
