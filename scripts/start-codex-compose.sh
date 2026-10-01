#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT"

umask 077
if [ ! -f .env ]; then
  token="$(openssl rand -hex 32)"
  printf '%s\n' "HARNESSMESH_PROVIDER_TOKEN=$token" > .env
  printf '%s\n' 'HARNESSMESH_PROVIDER_CONFIG=configs/codex-chatgpt.example.json' >> .env
  printf '%s\n' "HARNESSMESH_SIWC_HOME=$HOME/.harnessmesh" >> .env
  printf '%s\n' "HARNESSMESH_UID=$(id -u)" >> .env
  printf '%s\n' "HARNESSMESH_GID=$(id -g)" >> .env
  echo "Created .env with a local provider token."
fi

if ! grep -q '^HARNESSMESH_PROVIDER_TOKEN=' .env; then
  printf '%s\n' "HARNESSMESH_PROVIDER_TOKEN=$(openssl rand -hex 32)" >> .env
fi
if ! grep -q '^HARNESSMESH_PROVIDER_CONFIG=' .env; then
  printf '%s\n' 'HARNESSMESH_PROVIDER_CONFIG=configs/codex-chatgpt.example.json' >> .env
fi
if ! grep -q '^HARNESSMESH_SIWC_HOME=' .env; then
  printf '%s\n' "HARNESSMESH_SIWC_HOME=$HOME/.harnessmesh" >> .env
fi
if ! grep -q '^HARNESSMESH_UID=' .env; then
  printf '%s\n' "HARNESSMESH_UID=$(id -u)" >> .env
fi
if ! grep -q '^HARNESSMESH_GID=' .env; then
  printf '%s\n' "HARNESSMESH_GID=$(id -g)" >> .env
fi

# Load the same token into the environment inherited by Codex/VS Code.
set -a
. ./.env
set +a

if [ -x ./bin/harnessmesh ]; then
  ./bin/harnessmesh integrate codex-provider \
    --scope user \
    --listen 127.0.0.1:8789 \
    --model harnessmesh-chatgpt
else
  echo "Missing bin/harnessmesh; run scripts/setup.sh first." >&2
  exit 1
fi

mkdir -p "$HARNESSMESH_SIWC_HOME"
if [ ! -f "$HARNESSMESH_SIWC_HOME/chatgpt-siwc-auth.json" ]; then
  ./bin/harnessmesh provider auth chatgpt \
    --token-path "$HARNESSMESH_SIWC_HOME/chatgpt-siwc-auth.json"
fi

# Compose interpolates variables from inactive profiles too. These placeholders
# are never passed to a started service here; mcp/bridge remain disabled.
HARNESSMESH_MCP_TOKEN="${HARNESSMESH_MCP_TOKEN:-provider-setup-unused-mcp}" \
HARNESSMESH_BRIDGE_TOKEN="${HARNESSMESH_BRIDGE_TOKEN:-provider-setup-unused-bridge}" \
  docker compose --profile provider up --build -d harnessmesh-provider

echo
echo "HarnessMesh provider is running at http://127.0.0.1:8789/v1"
echo "Start VS Code with the token environment using:"
echo "  set -a; . ./.env; set +a; code ."

if command -v code >/dev/null 2>&1 && [ "${HARNESSMESH_OPEN_CODE:-0}" = 1 ]; then
  exec code "$@"
fi
