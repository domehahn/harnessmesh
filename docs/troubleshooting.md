# Troubleshooting HarnessMesh v0.2.0

This guide covers diagnostic strategies and solutions for common operational issues in HarnessMesh v0.2.0.

---

## 1. Running System Diagnostics (`harnessmesh doctor`)

The primary diagnostic tool is `harnessmesh doctor`:

```bash
harnessmesh doctor [--config harnessmesh.json]
```

It verifies:
- All configured harness adapter binaries (Claude, Codex, Antigravity, Copilot CLI) and runs health probes.
- Model routing backend availability (e.g. Switchyard reachability).
- Git binary existence and repo context.
- SQLite database integrity and migration state.

---

## 2. Common Errors and Resolutions

### Error: "harness invocation failed ... exited with code 1"
- **Symptom**: An agent fails with exit code 1, but previously the reason was unclear.
- **Root Cause & Fix in v0.2.0**: HarnessMesh v0.2.0 captures and attaches CLI stderr to the error message and report. Check the `report.md` or terminal output for the detailed stderr trace.
- **Common causes**:
  - Missing authentication (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or GitHub login).
  - Unrecognized command-line argument in `extra_args`.
  - Stale worktree lock (`.git/index.lock`).

### Error: `SingleWriterViolationError`
- **Symptom**: `cannot create session: exactly one writable participant required`
- **Root Cause**: Two or more agents in `agents` are configured with `writable: true`, or an executor tried to invoke a review while another agent was modifying files.
- **Resolution**: Ensure only one agent is designated as `writable: true` in your active session profile. Reviewers must have `writable: false`.

### Error: `SwitchyardUnavailableError`
- **Symptom**: `switchyard backend "switchyard" unreachable: unexpected HTTP status 503` or connection refused.
- **Resolution**:
  - Check if Switchyard server is running: `curl http://127.0.0.1:4000/v1/models`.
  - Validate your routes file: `harnessmesh switchyard config validate`.
  - If Switchyard is not needed, set `"enabled": false` or use direct configuration (`configs/no-switchyard-example.json`).

### Error: `PeerDepthExceededError` / `BudgetExceededError`
- **Symptom**: Reentrancy or budget loop aborts the session.
- **Resolution**: Increase `collaboration.max_peer_depth` (default 2) or `collaboration.max_peer_calls` (default 10) in `harnessmesh.json` if complex multi-peer investigations genuinely require deeper delegation.

### Error: Schema validation failure in OpenAI Structured Outputs
- **Symptom**: `codex exited with code 1: invalid json schema`
- **Resolution**: OpenAI Structured Outputs requires `additionalProperties: false` on all object schemas and every property in `properties` must be listed in `required`. HarnessMesh v0.2.0 schemas strictly enforce this invariant.

### Error: `HarnessAuthenticationRequiredError` (OpenAI Codex)
- **Symptom**: `harness "openai-reviewer" requires authentication: codex CLI is not logged in`
- **Resolution**:
  - Run `codex login` in your terminal to authenticate via OpenAI.
  - Or export `OPENAI_API_KEY="sk-..."` in your environment.
  - Verify with `harnessmesh smoke-test antigravity-codex`.

### Antigravity Chat Does Not Invoke Peer
- **Symptom**: Antigravity in VS Code chat does not use `peer.converse` or `peer.ask`.
- **Resolution**:
  - Run `harnessmesh doctor` to check if the integration is active.
  - Run `harnessmesh integrate antigravity` to ensure `.agent/mcp_config.json` and `.agent/rules/harnessmesh.md` are installed.
  - Restart the Antigravity chat or reload the VS Code window (`Developer: Reload Window`).

---

## 3. Reviewing Session Logs & Reports

HarnessMesh stores external persistent session data in `~/.harnessmesh/`:

- **Database**: `~/.harnessmesh/mesh.db` (SQLite store containing sessions, messages, findings, evidence, challenges, and routing decisions).
- **Run Artifacts**: `~/.harnessmesh/runs/<session-id>/<timestamp>/`
  - `report.md`: Human-readable summary of verdicts, rounds, and participant actions.
  - `report.json`: Machine-readable execution trace.
  - `context_pack.json`: Code diffs and projected files delivered to peers.

Inspect findings, evidence, and conversational transcripts directly from the command line:

```bash
# Inspect session message envelopes and causality chain
harnessmesh session messages <session-id>

# Inspect structured findings
harnessmesh findings <session-id>

# Inspect attached evidence
harnessmesh evidence <session-id>
```

