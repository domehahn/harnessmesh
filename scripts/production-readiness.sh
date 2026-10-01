#!/usr/bin/env bash
set -u

# Authoritative deterministic production gate. This script never calls an
# OpenAI inference endpoint and never starts Codex. All provider execution in
# the selected tests uses fake backends, local httptest servers, or fixtures.
if [[ "${HARNESSMESH_ALLOW_LIVE_OPENAI_INFERENCE:-}" == "1" ]]; then
  echo "PRODUCTION_READY=FAIL"
  echo "Refusing deterministic gate while live OpenAI inference is explicitly enabled."
  exit 2
fi

root_dir="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root_dir"
tmp_root="${TMPDIR:-/tmp}"
tmp_root="${tmp_root%/}"
export GOCACHE="${GOCACHE:-${tmp_root}/harnessmesh-production-go-cache}"
mkdir -p "$GOCACHE"

failed=0
run_gate() {
  local name="$1"
  shift
  if "$@"; then
    echo "$name=PASS"
  else
    echo "$name=FAIL"
    failed=1
  fi
}

run_gate BUILD go build ./...
run_gate VET go vet ./...
run_gate UNIT go test ./...
run_gate RACE go test -race ./...
run_gate FUZZ go test ./internal/provider -run '^$' -fuzz FuzzResponsesRequestParsing -fuzztime=1x
run_gate CONTRACT go test ./internal/provider -run 'Test(SSELossless|Wire|SIWC|ServerModels|CodexModels|Input|CallOutput)'
run_gate AUTH go test ./internal/provider -run 'Test.*Auth|Test.*Authorization'
run_gate MODEL_CATALOG go test ./internal/provider -run 'Test.*Model'
run_gate WRITE_EDIT go test ./internal/provider -run '^TestE2E_FilesystemWriteEditDelete$' -count=1
run_gate RECOVERY go test ./internal/provider -run 'Test.*(Timeout|Cancel|Failure|Fallback|Shutdown|Disconnect)'
run_gate CONCURRENCY go test -race ./internal/provider -run 'Test.*(Concurrent|TwoRequests|Parallel|Load)'
run_gate OBSERVABILITY go test ./internal/provider -run 'Test.*(Diagnostic|Audit|Log|Redact)'
run_gate REDACTION go test ./internal/provider -run 'Test.*(Sensitive|Secret|Redact|Credential)'
run_gate ZERO_API_BILLING go test ./internal/provider ./internal/config -run 'Test.*(ZeroCredit|ZeroAPIBilling|Metered|Policy)'

if (( failed == 0 )); then
  echo "PRODUCTION_READY=PASS"
else
  echo "PRODUCTION_READY=FAIL"
fi
exit "$failed"
