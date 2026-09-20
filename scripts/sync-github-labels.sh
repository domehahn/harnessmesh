#!/usr/bin/env bash
set -euo pipefail

repo="${1:-}"
if [[ -z "$repo" ]]; then
  repo="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"
fi

labels=(
  "bug|d73a4a|Something is not working"
  "enhancement|a2eeef|New capability or improvement"
  "documentation|0075ca|Documentation-only change"
  "security|b60205|Security or privacy impact"
  "performance|f9d0c4|Performance, latency, or resource usage"
  "adapter|5319e7|Harness or provider adapter"
  "protocol|1d76db|Public protocol, MCP, or compatibility change"
  "good first issue|7057ff|Suitable for a new contributor"
  "help wanted|008672|Maintainers welcome community help"
  "breaking change|b60205|Requires migration or compatibility work"
)

for definition in "${labels[@]}"; do
  IFS='|' read -r name color description <<< "$definition"
  gh label create "$name" --repo "$repo" --color "$color" --description "$description" --force >/dev/null
  echo "synced: $name"
done
