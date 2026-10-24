#!/bin/sh
set -eu

# Small, deployment-facing load probe. It intentionally does not claim to be
# a capacity benchmark; it provides a repeatable smoke signal before a real
# staging run and records HTTP status/latency for every request.
: "${HARNESSMESH_BRIDGE_URL:?set HARNESSMESH_BRIDGE_URL, e.g. http://127.0.0.1:8788}"
: "${HARNESSMESH_BRIDGE_TOKEN:?set HARNESSMESH_BRIDGE_TOKEN}"

requests=${HARNESSMESH_LOAD_REQUESTS:-100}
concurrency=${HARNESSMESH_LOAD_CONCURRENCY:-10}
case "$requests" in ''|*[!0-9]*) echo "HARNESSMESH_LOAD_REQUESTS must be numeric" >&2; exit 2 ;; esac
case "$concurrency" in ''|*[!0-9]*) echo "HARNESSMESH_LOAD_CONCURRENCY must be numeric" >&2; exit 2 ;; esac
[ "$requests" -gt 0 ] || { echo "request count must be positive" >&2; exit 2; }
[ "$concurrency" -gt 0 ] || { echo "concurrency must be positive" >&2; exit 2; }

base=${HARNESSMESH_BRIDGE_URL%/}
curl --fail --silent --show-error --max-time 10 "$base/readyz" >/dev/null
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

start=1
while [ "$start" -le "$requests" ]; do
	end=$((start + concurrency - 1))
	[ "$end" -le "$requests" ] || end=$requests
	for i in $(seq "$start" "$end"); do
		curl --silent --show-error --max-time 10 \
			-o /dev/null -w '%{http_code} %{time_total}\n' \
			-H "Authorization: Bearer $HARNESSMESH_BRIDGE_TOKEN" \
			"$base/api/v1/workspaces" >"$tmp/$i" 2>&1 &
	done
	wait
	start=$((end + 1))
done

awk '
  { total++; code[$1]++; latency+=$2; if ($2>max) max=$2; if ($1 !~ /^2/) failures++ }
  END {
    if (total == 0 || failures > 0) exit 1
    printf "requests=%d average_seconds=%.4f max_seconds=%.4f failures=%d\n", total, latency/total, max, failures
    for (c in code) printf "http_%s=%d ", c, code[c]
    print ""
  }
' "$tmp"/*
echo "STAGING_LOAD_SMOKE=PASS"
