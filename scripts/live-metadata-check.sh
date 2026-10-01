#!/usr/bin/env bash
set -u
root_dir="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root_dir"
tmp_root="${TMPDIR:-/tmp}"
tmp_root="${tmp_root%/}"
export GOCACHE="${GOCACHE:-${tmp_root}/harnessmesh-live-metadata-go-cache}"
mkdir -p "$GOCACHE"
codex_bin="${CODEX_BIN:-$(command -v codex || true)}"
if [[ -z "$codex_bin" ]]; then echo "LIVE_MODEL_METADATA=FAIL"; exit 1; fi
version="$("$codex_bin" --version 2>/dev/null | sed -n 's/^codex-cli //p' | tail -1)"
if [[ -z "$version" ]]; then echo "LIVE_MODEL_METADATA=FAIL"; exit 1; fi
run_dir="$(mktemp -d "${tmp_root}/harnessmesh-live-metadata.XXXXXX")"
provider_pid=""
app_pid=""
cleanup() {
  if [[ -n "$app_pid" ]] && kill -0 "$app_pid" 2>/dev/null; then kill "$app_pid" 2>/dev/null || true; fi
  if [[ -n "$provider_pid" ]] && kill -0 "$provider_pid" 2>/dev/null; then kill "$provider_pid" 2>/dev/null || true; fi
}
trap cleanup EXIT
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
token="metadata-only-test-token"
config="$run_dir/harnessmesh.json"
printf '%s
' '{"version":2,"agents":{"placeholder":{"kind":"fake","role":"executor","writable":true}},"provider":{"enabled":true,"token":"'"$token"'","listen":"127.0.0.1:'"$port"'","default_backend":"local","backends":{"local":{"type":"openai-compatible","base_url":"http://127.0.0.1:9/v1"}}}}' >"$config"
binary="$run_dir/harnessmesh"
if ! go build -trimpath -o "$binary" ./cmd/harnessmesh; then echo "LIVE_MODEL_METADATA=FAIL"; exit 1; fi
HARNESSMESH_METADATA_ONLY_TEST=1 HARNESSMESH_PROVIDER_TOKEN="$token" "$binary" provider serve --metadata-only --config "$config" --listen "127.0.0.1:$port" >"$run_dir/provider.log" 2>&1 &
provider_pid=$!
ready=0
for _ in $(seq 1 40); do
  if curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.1
done
if [[ "$ready" != 1 ]]; then cat "$run_dir/provider.log"; echo "LIVE_MODEL_METADATA=FAIL"; exit 1; fi
codex_home="$run_dir/codex-home"
mkdir -m 700 "$codex_home"
printf '%s
' 'model_provider = "harnessmesh"' 'model = "gpt-5.6-luna"' '' '[model_providers.harnessmesh]' 'name = "HarnessMesh"' 'base_url = "http://127.0.0.1:'"$port"'/v1"' 'wire_api = "responses"' 'env_key = "HARNESSMESH_PROVIDER_TOKEN"' >"$codex_home/config.toml"
mkfifo "$run_dir/app.stdin"
HARNESSMESH_PROVIDER_TOKEN="$token" CODEX_HOME="$codex_home" "$codex_bin" app-server --stdio <"$run_dir/app.stdin" >"$run_dir/app.stdout" 2>"$run_dir/app.stderr" &
app_pid=$!
exec 3>"$run_dir/app.stdin"
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"harnessmesh-live-metadata-check","version":"1"}}}' >&3
printf '%s\n' '{"jsonrpc":"2.0","method":"initialized","params":{}}' >&3
printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"thread/start","params":{"cwd":"'"$run_dir"'","model":"gpt-5.6-luna","approvalPolicy":"never","sandbox":"read-only"}}' >&3
printf '%s\n' '{"jsonrpc":"2.0","id":3,"method":"model/list","params":{"includeHidden":false}}' >&3
sleep 2
HARNESSMESH_PROVIDER_TOKEN="$token" CODEX_HOME="$codex_home" "$codex_bin" debug models >"$run_dir/debug-models.json" 2>"$run_dir/debug-models.stderr" || true
codex_catalog_ok=0
if rg -q 'gpt-5.6-luna' "$run_dir/debug-models.json"; then codex_catalog_ok=1; fi
http_code="$(curl -sS -o "$run_dir/models.json" -w '%{http_code}' "http://127.0.0.1:$port/v1/models?client_version=$version" -H "Authorization: Bearer $token" -H "User-Agent: codex_vscode/$version (live-metadata-check)")"
schema_ok=0
if [[ "$http_code" == 200 ]] && python3 - "$run_dir/models.json" <<'PY'
import json, sys
d=json.load(open(sys.argv[1], encoding="utf-8"))
assert isinstance(d.get("models"), list) and d["models"]
assert any(m.get("slug") == "gpt-5.6-luna" for m in d["models"])
PY
then schema_ok=1; fi
structured_ok=0
if python3 - "$run_dir/provider.log" "$version" <<'PY'
import json, sys
path, version = sys.argv[1:]
for line in open(path, encoding="utf-8", errors="replace"):
    try: d=json.loads(line)
    except Exception: continue
    if d.get("route") == "/v1/models" and d.get("method") == "GET" and "codex" in d.get("client", "").lower() and d.get("client_version") == version and d.get("status") == 200 and d.get("request_id") and "duration_ms" in d:
        raise SystemExit(0)
raise SystemExit(1)
PY
then structured_ok=1; fi
metrics="$(curl -sS "http://127.0.0.1:$port/metrics")"
responses_requests="$(printf '%s\n' "$metrics" | sed -n 's/^harnessmesh_provider_responses_requests_total //p' | tail -1)"
[[ -n "$responses_requests" ]] || responses_requests=999
# Measure all provider backend calls. In metadata-only mode /v1/responses is
# rejected before backend resolution, so this must stay at zero. An absent
# backend series is correctly measured as zero rather than assumed to be zero.
upstream_inference_requests="$(printf '%s\n' "$metrics" | awk '/^harnessmesh_provider_backend_calls_total\{/{sum += $2} END {print sum+0}')"
if rg -q 'sk-|metadata-only-test-token|Authorization: Bearer' "$run_dir/provider.log" 2>/dev/null; then structured_ok=0; fi
if kill -0 "$app_pid" 2>/dev/null && rg -q '"id"[[:space:]]*:[[:space:]]*1' "$run_dir/app.stdout" 2>/dev/null; then app_initialized=1; else app_initialized=0; fi
if rg -qi 'failed to refresh available models|failed to decode models response' "$run_dir/app.stderr" "$run_dir/app.stdout" 2>/dev/null; then decode_errors=1; else decode_errors=0; fi
echo "CODEX_VERSION=$version"
echo "REQUESTED=/v1/models?client_version=$version"
echo "HTTP=$http_code"
echo "CODEX_INITIALIZED=$([[ "$app_initialized" == 1 ]] && echo PASS || echo FAIL)"
echo "CODEX_CATALOG_DECODER=$([[ "$codex_catalog_ok" == 1 ]] && echo PASS || echo FAIL)"
echo "MODEL_DECODE_ERRORS=$decode_errors"
echo "responses_requests=$responses_requests"
echo "upstream_inference_requests=$upstream_inference_requests"
if [[ "$schema_ok" == 1 && "$structured_ok" == 1 && "$app_initialized" == 1 && "$codex_catalog_ok" == 1 && "$decode_errors" == 0 && "$responses_requests" == 0 && "$upstream_inference_requests" == 0 ]]; then
  echo "LIVE_MODEL_METADATA=PASS"
  exit 0
fi
echo "LIVE_MODEL_METADATA=FAIL"
exit 1
