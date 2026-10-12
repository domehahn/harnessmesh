#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "$0")/.." && pwd)"
env_file="${HARNESSMESH_CHATGPT_ENV_FILE:-$root_dir/.harnessmesh/chatgpt-frp.env}"
if [[ ! -f "$env_file" ]]; then
  echo "No ChatGPT frp environment file found: $env_file"
  exit 0
fi
docker compose --env-file "$env_file" --profile chatgpt-frp down
