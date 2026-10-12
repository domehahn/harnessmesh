#!/usr/bin/env bash
set -euo pipefail

# Run the upstream OpenResponses compliance suite against a running
# HarnessMesh provider. The suite is deliberately opt-in: it is an external
# developer tool and must never be downloaded or executed by the normal,
# deterministic production-readiness gate.
#
# Required:
#   OPENRESPONSES_DIR   checkout of github.com/openresponses/openresponses
# Optional:
#   OPENRESPONSES_BASE_URL  default: http://127.0.0.1:8789/v1
#   OPENRESPONSES_API_KEY   bearer token for the provider
#   OPENRESPONSES_COMMIT    expected git commit; required for release/CI use

root_dir="$(cd "$(dirname "$0")/.." && pwd)"
responses_dir="${OPENRESPONSES_DIR:-}"
base_url="${OPENRESPONSES_BASE_URL:-http://127.0.0.1:8789/v1}"
api_key="${OPENRESPONSES_API_KEY:-${HARNESSMESH_PROVIDER_TOKEN:-}}"
expected_commit="${OPENRESPONSES_COMMIT:-}"

if [[ -z "$responses_dir" ]]; then
  echo "OPENRESPONSES_DIR is required (path to an OpenResponses checkout)." >&2
  exit 2
fi
if [[ ! -f "$responses_dir/package.json" ]]; then
  echo "OpenResponses checkout not found or missing package.json: $responses_dir" >&2
  exit 2
fi
if ! git -C "$responses_dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "OPENRESPONSES_DIR must be a git checkout so the tested version can be recorded." >&2
  exit 2
fi
actual_commit="$(git -C "$responses_dir" rev-parse HEAD)"
if [[ -z "$expected_commit" ]]; then
  echo "OPENRESPONSES_COMMIT is required; pin the reviewed OpenResponses commit." >&2
  exit 2
fi
if [[ "$actual_commit" != "$expected_commit" ]]; then
  echo "OpenResponses commit mismatch: expected $expected_commit, got $actual_commit" >&2
  exit 2
fi
if [[ -z "$api_key" ]]; then
  echo "Set OPENRESPONSES_API_KEY or HARNESSMESH_PROVIDER_TOKEN." >&2
  exit 2
fi
if ! command -v bun >/dev/null 2>&1; then
  echo "bun is required to run the upstream OpenResponses compliance suite." >&2
  exit 2
fi

cd "$root_dir"
echo "Running OpenResponses compliance suite against $base_url (commit $actual_commit)"
(
  cd "$responses_dir"
  exec bun run test:compliance --base-url "$base_url" --api-key "$api_key"
)
