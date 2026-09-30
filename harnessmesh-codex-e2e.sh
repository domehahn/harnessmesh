#!/usr/bin/env bash
set -Eeuo pipefail

# HarnessMesh + Codex VS Code end-to-end diagnostic harness for macOS.
# Safe by default:
# - does NOT print provider/OAuth tokens
# - does NOT kill normal VS Code processes
# - does NOT modify ~/.codex/config.toml
# - uses isolated CODEX_HOME, user-data-dir and shared-data-dir
#
# Usage:
#   chmod +x harnessmesh-codex-e2e.sh
#   ./harnessmesh-codex-e2e.sh [repo]
#
# Optional env:
#   HM_MODEL=gpt-5.6-luna
#   HM_LISTEN=127.0.0.1:8789
#   HM_CODEX_HOME=$HOME/.codex-harnessmesh
#   HM_USER_DATA=$HOME/.vscode-harnessmesh-user-data
#   HM_SHARED_DATA=$HOME/.vscode-harnessmesh-shared
#   HM_SKIP_BUILD=1
#   HM_SKIP_VSCODE=1
#   HM_DIRECT_SIWC=1

REPO="${1:-$HOME/dev/workspace/harnessmesh}"
MODEL="${HM_MODEL:-gpt-5.6-luna}"
LISTEN="${HM_LISTEN:-127.0.0.1:8789}"
BASE_URL="http://${LISTEN}/v1"
CODEX_HOME_DIR="${HM_CODEX_HOME:-$HOME/.codex-harnessmesh}"
USER_DATA_DIR="${HM_USER_DATA:-$HOME/.vscode-harnessmesh-user-data}"
SHARED_DATA_DIR="${HM_SHARED_DATA:-$HOME/.vscode-harnessmesh-shared}"
TOKEN_FILE="$HOME/.harnessmesh/provider-token"
SIWC_AUTH_FILE="$HOME/.harnessmesh/chatgpt-siwc-auth.json"
RUN_ID="$(date +%Y%m%d-%H%M%S)"
RUN_DIR="/tmp/harnessmesh-e2e-${RUN_ID}"
PROVIDER_LOG="$RUN_DIR/provider.log"
VSCODE_LOG="$RUN_DIR/vscode-main.log"
CURL_TOOL_LOG="$RUN_DIR/tool-required.sse"
CURL_NS_LOG="$RUN_DIR/namespace-required.sse"
DIRECT_SIWC_LOG="$RUN_DIR/direct-siwc.sse"
REPORT="$RUN_DIR/report.txt"

mkdir -p "$RUN_DIR"

PASS=0
FAIL=0
WARN=0

say()  { printf '\n==> %s\n' "$*"; }
pass() { PASS=$((PASS+1)); printf 'PASS  %s\n' "$*" | tee -a "$REPORT"; }
fail() { FAIL=$((FAIL+1)); printf 'FAIL  %s\n' "$*" | tee -a "$REPORT"; }
warn() { WARN=$((WARN+1)); printf 'WARN  %s\n' "$*" | tee -a "$REPORT"; }

cleanup() {
  # Only stop the provider instance started by this script.
  if [[ -n "${PROVIDER_PID:-}" ]] && kill -0 "$PROVIDER_PID" 2>/dev/null; then
    kill "$PROVIDER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

die() {
  echo "ERROR: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

need jq
need curl
need python3
need rg
need plutil
need osascript
need pgrep

[[ -d "$REPO" ]] || die "repo not found: $REPO"
[[ -f "$TOKEN_FILE" ]] || die "provider token file not found: $TOKEN_FILE"
[[ -f "$SIWC_AUTH_FILE" ]] || die "SIWC auth file not found: $SIWC_AUTH_FILE"

cd "$REPO"

say "Run directory"
echo "$RUN_DIR"

say "Repository / commit"
git status --short || true
git log -1 --oneline || true

if [[ "${HM_SKIP_BUILD:-0}" != "1" ]]; then
  say "Build HarnessMesh"
  go build -trimpath -o bin/harnessmesh ./cmd/harnessmesh
  mkdir -p "$HOME/.local/bin"
  cp ./bin/harnessmesh "$HOME/.local/bin/harnessmesh"
  pass "HarnessMesh build completed"
fi

HARNESSMESH_BIN="${HARNESSMESH_BIN:-$(command -v harnessmesh || true)}"
[[ -n "$HARNESSMESH_BIN" ]] || HARNESSMESH_BIN="$HOME/.local/bin/harnessmesh"
[[ -x "$HARNESSMESH_BIN" ]] || die "harnessmesh binary not found/executable"

# Read, trim and export without printing.
HARNESSMESH_PROVIDER_TOKEN="$(tr -d '\r\n' < "$TOKEN_FILE")"
export HARNESSMESH_PROVIDER_TOKEN
export HARNESSMESH_SIWC_DEBUG=1

if [[ -z "$HARNESSMESH_PROVIDER_TOKEN" ]]; then
  die "provider token is empty"
fi
pass "Provider token loaded (value not printed)"

say "Stop only existing HarnessMesh provider processes"
pkill -f 'harnessmesh provider serve' 2>/dev/null || true
sleep 0.5

say "Start provider with redacted SIWC diagnostics"
"$HARNESSMESH_BIN" provider serve \
  --config harnessmesh.json \
  --listen "$LISTEN" \
  >"$PROVIDER_LOG" 2>&1 &
PROVIDER_PID=$!
sleep 1

if kill -0 "$PROVIDER_PID" 2>/dev/null; then
  pass "Provider process is alive (pid=$PROVIDER_PID)"
else
  fail "Provider exited during startup"
  tail -100 "$PROVIDER_LOG" || true
  exit 1
fi

# Readiness is proven with an actual HTTP probe against the provider's own
# unauthenticated /healthz endpoint, bounded and polled - NOT with
# `lsof | rg 'harnessmesh'`, which produced a false negative here even
# though the provider was already fully working (provider doctor,
# inference, and function calling all passed immediately afterwards in the
# same run). Process-name matching via lsof is inherently unreliable
# (platform-dependent process-name truncation/formatting in lsof's output,
# timing relative to the listener actually being bound, etc.) and must
# never gate a false FAIL on an otherwise-healthy provider.
HEALTHZ_READY=0
for _ in $(seq 1 20); do
  if curl -sS -o /dev/null -w '%{http_code}' "http://${LISTEN}/healthz" 2>/dev/null | rg -q '^200$'; then
    HEALTHZ_READY=1
    break
  fi
  sleep 0.25
done
if [[ "$HEALTHZ_READY" == "1" ]]; then
  pass "Provider is listening on $LISTEN (healthz probe succeeded)"
else
  fail "Provider is not listening on $LISTEN (healthz probe never returned 200 within 5s)"
fi

say "Provider doctor"
if "$HARNESSMESH_BIN" provider doctor --config harnessmesh.json | tee "$RUN_DIR/provider-doctor.txt"; then
  pass "provider doctor passed"
else
  fail "provider doctor failed"
fi

say "Basic Responses smoke test"
cat >"$RUN_DIR/basic.json" <<JSON
{
  "model": "$MODEL",
  "input": "Say exactly: HARNESSMESH_SIWC_OK",
  "store": false,
  "stream": true
}
JSON

curl -N -sS \
  "$BASE_URL/responses" \
  -H "Authorization: Bearer $HARNESSMESH_PROVIDER_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @"$RUN_DIR/basic.json" \
  >"$RUN_DIR/basic.sse"

if rg -q 'event: response\.completed' "$RUN_DIR/basic.sse" &&
   rg -q 'HARNESSMESH_SIWC_OK' "$RUN_DIR/basic.sse"; then
  pass "Basic SIWC inference through HarnessMesh completed"
else
  fail "Basic SIWC inference failed"
  tail -80 "$RUN_DIR/basic.sse" || true
fi

say "Forced top-level function-call test"
jq -n --arg model "$MODEL" '{
  model: $model,
  instructions: "You are a tool-using agent. You must use the provided tool.",
  tools: [{
    type: "function",
    name: "read_repo_file",
    description: "Read one repository file by path.",
    parameters: {
      type: "object",
      properties: {path: {type: "string"}},
      required: ["path"],
      additionalProperties: false
    }
  }],
  tool_choice: "required",
  input: [{role: "user", content: "Read README.md using the available tool."}],
  store: false,
  stream: true
}' >"$RUN_DIR/tool-required.json"

curl -N -sS \
  "$BASE_URL/responses" \
  -H "Authorization: Bearer $HARNESSMESH_PROVIDER_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @"$RUN_DIR/tool-required.json" \
  >"$CURL_TOOL_LOG"

if rg -q '"type":"function_call"' "$CURL_TOOL_LOG" &&
   rg -q 'event: response\.function_call_arguments\.done' "$CURL_TOOL_LOG" &&
   rg -q 'event: response\.completed' "$CURL_TOOL_LOG"; then
  pass "Forced function_call streamed through HarnessMesh"
else
  fail "Forced function_call test failed"
  cat "$CURL_TOOL_LOG"
fi

say "Check SSE sequence_number monotonicity"
python3 - "$CURL_TOOL_LOG" <<'PY'
import json, re, sys
path = sys.argv[1]
nums=[]
for line in open(path, encoding="utf-8"):
    if line.startswith("data: "):
        try:
            obj=json.loads(line[6:])
        except Exception:
            continue
        if isinstance(obj, dict) and isinstance(obj.get("sequence_number"), int):
            nums.append(obj["sequence_number"])
if not nums:
    print("NO_SEQUENCE_NUMBERS")
    raise SystemExit(2)
ok = all(b > a for a,b in zip(nums, nums[1:]))
print("sequence_numbers:", nums)
raise SystemExit(0 if ok else 1)
PY
case $? in
  0) pass "SSE sequence numbers are strictly increasing" ;;
  *) fail "SSE sequence numbers are not strictly increasing" ;;
esac

say "Forced namespace-contained function test"
jq -n --arg model "$MODEL" '{
  model: $model,
  instructions: "You must use the provided repository tool. Do not answer directly.",
  input: [
    {
      type: "additional_tools",
      role: "developer",
      tools: [{
        type: "namespace",
        name: "repo",
        description: "Repository operations",
        tools: [{
          type: "function",
          name: "read_file",
          description: "Read a repository file",
          parameters: {
            type: "object",
            properties: {path: {type: "string"}},
            required: ["path"],
            additionalProperties: false
          }
        }]
      }]
    },
    {role: "user", content: "Use the repository tool to read go.mod."}
  ],
  tool_choice: "required",
  store: false,
  stream: true
}' >"$RUN_DIR/namespace-required.json"

curl -N -sS \
  "$BASE_URL/responses" \
  -H "Authorization: Bearer $HARNESSMESH_PROVIDER_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @"$RUN_DIR/namespace-required.json" \
  >"$CURL_NS_LOG"

if rg -q '"type":"function_call"' "$CURL_NS_LOG" &&
   rg -q 'event: response\.completed' "$CURL_NS_LOG"; then
  pass "Namespace-contained function produced a tool call"
else
  fail "Namespace tool invocation failed"
  cat "$CURL_NS_LOG"
fi

say "Provider diagnostics so far"
cat "$PROVIDER_LOG"

# Optional: compare direct SIWC against HarnessMesh.
if [[ "${HM_DIRECT_SIWC:-0}" == "1" ]]; then
  say "Direct SIWC comparison (OAuth token value will NOT be printed)"
  ACCESS_TOKEN="$(python3 - "$SIWC_AUTH_FILE" <<'PY'
import json, sys
print(json.load(open(sys.argv[1]))["access_token"])
PY
)"
  curl -N -sS \
    "https://api.openai.com/v1/responses" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" \
    --data-binary @"$RUN_DIR/tool-required.json" \
    >"$DIRECT_SIWC_LOG"
  unset ACCESS_TOKEN

  if rg -q '"type":"function_call"' "$DIRECT_SIWC_LOG" &&
     rg -q 'event: response\.completed' "$DIRECT_SIWC_LOG"; then
    pass "Direct SIWC forced function_call completed"
  else
    fail "Direct SIWC forced function_call failed"
  fi
fi

say "Probe /v1/models because Codex logs previously showed model-refresh decode errors"
HTTP_CODE="$(curl -sS -o "$RUN_DIR/models.body" -w '%{http_code}' \
  "$BASE_URL/models" \
  -H "Authorization: Bearer $HARNESSMESH_PROVIDER_TOKEN" || true)"
echo "HTTP $HTTP_CODE" | tee "$RUN_DIR/models.status"
if [[ "$HTTP_CODE" == "200" ]] && jq -e . "$RUN_DIR/models.body" >/dev/null 2>&1; then
  pass "/v1/models returned valid JSON"
else
  warn "/v1/models is not Codex-compatible yet (HTTP $HTTP_CODE); see $RUN_DIR/models.body"
fi

if [[ "${HM_SKIP_VSCODE:-0}" == "1" ]]; then
  warn "VS Code test skipped (HM_SKIP_VSCODE=1)"
else
  say "Resolve Visual Studio Code executable"
  APP="$(osascript -e 'POSIX path of (path to application id "com.microsoft.VSCode")' | tr -d '\r\n')"
  EXECUTABLE="$(plutil -extract CFBundleExecutable raw -o - "$APP/Contents/Info.plist")"
  VSCODE_BIN="$APP/Contents/MacOS/$EXECUTABLE"
  [[ -x "$VSCODE_BIN" ]] || die "VS Code executable not found: $VSCODE_BIN"
  echo "VS Code: $VSCODE_BIN"

  mkdir -p "$CODEX_HOME_DIR" "$USER_DATA_DIR" "$SHARED_DATA_DIR"

  say "Validate isolated Codex config"
  if [[ -f "$CODEX_HOME_DIR/config.toml" ]]; then
    rg -n '^(model|model_provider)|^\[model_providers\.harnessmesh|base_url|wire_api|auth' \
      "$CODEX_HOME_DIR/config.toml" || true
  else
    fail "Missing isolated Codex config: $CODEX_HOME_DIR/config.toml"
  fi

  say "Launch isolated VS Code"
  env CODEX_HOME="$CODEX_HOME_DIR" \
    "$VSCODE_BIN" \
    --new-window \
    --user-data-dir "$USER_DATA_DIR" \
    --shared-data-dir "$SHARED_DATA_DIR" \
    "$REPO" \
    >"$VSCODE_LOG" 2>&1 &
  VSCODE_LAUNCH_PID=$!

  # Wait for an actual process carrying this user-data-dir, since the launcher PID
  # can legitimately fork/detach.
  sleep 5
  if pgrep -f -- "--user-data-dir[ =]$USER_DATA_DIR" >/dev/null 2>&1; then
    pass "Isolated VS Code instance detected"
  else
    fail "Isolated VS Code instance was not detected after launch"
    echo "--- vscode-main.log ---"
    tail -120 "$VSCODE_LOG" || true
    echo "--- newest VS Code logs ---"
    NEWEST="$(ls -1dt "$USER_DATA_DIR/logs"/* 2>/dev/null | head -1 || true)"
    if [[ -n "$NEWEST" ]]; then
      rg -n -i 'error|fatal|crash|agentHostClient|codex|proxy|byok' "$NEWEST" | tail -200 || true
    fi
  fi

  cat <<'EOF'

MANUAL VS CODE STEP
===================
In the newly opened isolated Codex window, create a NEW chat and paste exactly:

Du musst für diese Aufgabe tatsächlich mindestens ein verfügbares lokales Repository-Tool aufrufen.

Lies go.mod aus dem aktuellen Workspace mit einem Tool.

Antworte erst NACH dem Tool-Aufruf ausschließlich mit der dort gefundenen Go-Version.

Gib keine Ankündigung aus.
Rate nicht.
Wenn du kein Tool aufrufst, ist die Aufgabe nicht erfüllt.

Then wait until Codex either:
- returns a final answer,
- visibly errors,
- or stops making progress.

Return here and press ENTER.
EOF
  read -r

  say "Inspect latest Codex session"
  SESSION="$(find "$CODEX_HOME_DIR/sessions" -type f -name '*.jsonl' -print0 2>/dev/null | \
    xargs -0 ls -t 2>/dev/null | head -1 || true)"

  if [[ -z "$SESSION" ]]; then
    fail "No Codex session JSONL found under $CODEX_HOME_DIR/sessions"
  else
    echo "Latest session: $SESSION" | tee -a "$REPORT"

    SESSION_MATCHES="$RUN_DIR/session-tool-events.txt"
    rg \
      '"type":"(function_call|function_call_output|custom_tool_call|custom_tool_call_output)"|tool_call|task_complete|agent_message|item_started|item_completed' \
      "$SESSION" >"$SESSION_MATCHES" || true
    cat "$SESSION_MATCHES"

    HAS_CALL=0
    HAS_OUTPUT=0
    HAS_COMPLETE=0
    rg -q '"type":"(function_call|custom_tool_call)"' "$SESSION" && HAS_CALL=1 || true
    rg -q '"type":"(function_call_output|custom_tool_call_output)"' "$SESSION" && HAS_OUTPUT=1 || true
    rg -q '"type":"task_complete"' "$SESSION" && HAS_COMPLETE=1 || true

    # Collect Codex extension / AgentHost diagnostics BEFORE finalizing the
    # tool-loop classification, so the stream-lifecycle signal can gate it:
    # the log line "OutputTextDelta without active item" means the SSE
    # stream consumer itself was already in an invalid state for this
    # turn. When that is true, a lack of observed tool-call events is NOT
    # trustworthy evidence about the model's actual tool-choice behavior -
    # it must be reported as a distinct stream-lifecycle defect, never
    # silently folded into "no tool call".
    STREAM_LIFECYCLE_INVALID=0
    NEWEST="$(ls -1dt "$USER_DATA_DIR/logs"/* 2>/dev/null | head -1 || true)"
    if [[ -n "$NEWEST" ]]; then
      rg -n -i \
        'agentHostClient|byok|proxy|codex|tool|function_call|timeout|error|failed|exception|model' \
        "$NEWEST" >"$RUN_DIR/vscode-relevant.log" || true
      tail -250 "$RUN_DIR/vscode-relevant.log" || true

      if rg -q 'OutputTextDelta without active item' "$RUN_DIR/vscode-relevant.log" "$NEWEST" 2>/dev/null; then
        STREAM_LIFECYCLE_INVALID=1
      fi
      if rg -q 'failed to refresh available models:.*failed to decode models response' "$RUN_DIR/vscode-relevant.log"; then
        warn "Codex model refresh compatibility error observed"
      fi
      if rg -q 'routePattern=/(wham|settings).*status=401|httpStatus=401' "$RUN_DIR/vscode-relevant.log"; then
        warn "Codex ChatGPT UI-side 401s observed in isolated profile"
      fi
    else
      warn "No VS Code log directory found"
    fi

    if [[ "$HAS_CALL" == "1" && "$HAS_OUTPUT" == "1" && "$HAS_COMPLETE" == "1" ]]; then
      pass "REAL CODEX TOOL LOOP: function/custom call + output + task_complete observed"
      CODEX_CLASS="A_FULL_TOOL_LOOP"
    elif [[ "$HAS_CALL" == "1" && "$HAS_OUTPUT" == "0" ]]; then
      fail "REAL CODEX TOOL LOOP: model emitted tool call, but no tool output returned"
      CODEX_CLASS="B_CALL_WITHOUT_OUTPUT"
    elif [[ "$STREAM_LIFECYCLE_INVALID" == "1" ]]; then
      fail "REAL CODEX TOOL LOOP: SSE stream lifecycle was invalid for this turn (Codex logged 'OutputTextDelta without active item') - tool-choice behavior cannot be evaluated until the lifecycle is valid"
      CODEX_CLASS="STREAM_LIFECYCLE_INVALID"
    else
      warn "REAL CODEX TOOL LOOP: no tool call observed; model likely completed text-only under auto tool choice"
      CODEX_CLASS="C_NO_TOOL_CALL"
    fi
    echo "Codex classification: $CODEX_CLASS" | tee -a "$REPORT"
  fi

  say "Provider request-shape diagnostics after real Codex turn"
  rg -n 'siwc-request-diag' "$PROVIDER_LOG" || true
fi

say "FINAL SUMMARY"
{
  echo
  echo "PASS=$PASS"
  echo "WARN=$WARN"
  echo "FAIL=$FAIL"
  echo "Run directory: $RUN_DIR"
  echo "Provider log: $PROVIDER_LOG"
  echo "Report: $REPORT"
} | tee -a "$REPORT"

if [[ "$FAIL" -gt 0 ]]; then
  echo
  echo "One or more tests failed. Preserve $RUN_DIR for diagnosis."
  exit 1
fi

echo
echo "All mandatory automated checks passed. Any WARN items remain follow-up work."
