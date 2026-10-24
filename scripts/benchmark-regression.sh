#!/usr/bin/env bash
set -euo pipefail

base_ref="${GITHUB_BASE_REF:-main}"
git fetch --no-tags origin "$base_ref"
tmp_root="$(mktemp -d)"
base_dir="$tmp_root/base"
base_output="$tmp_root/base.txt"
head_output="$tmp_root/head.txt"
trap 'git worktree remove --force "$base_dir" >/dev/null 2>&1 || true; rm -rf "$tmp_root"' EXIT

git worktree add --detach "$base_dir" "origin/$base_ref" >/dev/null

run_benchmark() {
  local directory="$1"
  local output="$2"
  (cd "$directory" && go test ./internal/knowledge -run '^$' -bench '^BenchmarkArchiveSearch$' -benchmem -count=6) >"$output"
}

run_benchmark "$base_dir" "$base_output"
run_benchmark "$PWD" "$head_output"

GOBIN="$tmp_root/bin" go install golang.org/x/perf/cmd/benchstat@latest
mkdir -p benchmark-results
"$tmp_root/bin/benchstat" -format csv "$base_output" "$head_output" | tee benchmark-results/benchstat.csv

python3 - benchmark-results/benchstat.csv <<'PY'
import re
import sys

threshold = 20.0
regressions = []
with open(sys.argv[1], encoding="utf-8") as report:
  lines = report
  for line in lines:
    fields = line.rstrip("\n").split(",")
    if not fields or not fields[0] or fields[0] in {"geomean", "name"}:
      continue
    if len(fields) < 6:
      continue
    match = re.fullmatch(r"([+-]?\d+(?:\.\d+)?)%", fields[5])
    if match and float(match.group(1)) > threshold:
      regressions.append((fields[0], fields[5]))
if regressions:
    print("benchmark regression threshold exceeded:", ", ".join(f"{name} {delta}" for name, delta in regressions), file=sys.stderr)
    raise SystemExit(1)
print(f"BENCHMARK_REGRESSION=PASS threshold={threshold}%")
PY
