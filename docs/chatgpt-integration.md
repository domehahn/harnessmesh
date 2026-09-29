# ChatGPT Integration

This document describes how a user's normal ChatGPT conversation participates in HarnessMesh as a collaboration peer, alongside Claude Code and any other configured harness, without ever consuming OpenAI API credits or Codex quota.

## Architecture

```text
                     ┌─────────────────────┐
                     │      ChatGPT         │
                     │ (normal ChatGPT      │
                     │  conversation does    │
                     │  all reasoning)       │
                     └──────────┬───────────┘
                                │  Remote MCP (Streamable HTTP)
                                ▼
                     ┌──────────────────────────┐
                     │       HarnessMesh         │
                     │  collaboration control    │
                     │  plane - transports       │
                     │  messages, tasks,         │
                     │  reviews, findings,       │
                     │  evidence, artifacts.     │
                     │                           │
                     │  NO model inference.      │
                     │  NO OpenAI API calls.     │
                     │  NO Codex invocation.     │
                     └────────────┬──────────────┘
                                  │
                 ┌────────────────┼─────────────────┐
                 ▼                ▼                 ▼
          REST /api/v1     WebSocket /events/ws   MCP stdio
                 │                │                 │
                 └────────────────┼─────────────────┘
                                  ▼
                          VS Code Extension
                                  │
                                  ▼
                            Claude Code
                                  │
                                  ▼
                             Repository
```

HarnessMesh is a **collaboration control plane**: a session broker, MCP server, event fabric, evidence store, and policy boundary. It is explicitly **not** an LLM proxy, model router, OpenAI API wrapper, ChatGPT API emulator, or Codex gateway. ChatGPT's own reasoning happens entirely inside the user's normal ChatGPT session; HarnessMesh only carries collaboration *state* (messages, tasks, reviews, findings, evidence, artifacts, decisions) between ChatGPT and the rest of the mesh.

## Prerequisites

- A running HarnessMesh instance with a git repository.
- A ChatGPT plan that supports custom/remote MCP connectors ("Developer Mode" / custom connectors). OpenAI's supported plans and exact feature name change over time - check OpenAI's current documentation for which plans allow adding a custom remote MCP server, and separately, which allow write-capable tools versus read-only tools. HarnessMesh does not hard-code an assumption here: its own tool surface is plan-neutral, and the ChatGPT-facing participant is read/propose-oriented (never write) regardless of plan.
- Network reachability from ChatGPT's servers to your HarnessMesh `/mcp` endpoint (see [Remote connectivity](#remote-connectivity)).

## Participant model

HarnessMesh models ChatGPT as a first-class participant with a fixed, restrictive capability set:

```json
{
  "chatgpt-browser": {
    "execution_mode": "external",
    "adapter": "mcp-remote",
    "role": "peer",
    "writable": false,
    "mode": "passive"
  }
}
```

| Capability | Allowed for `chatgpt-browser` |
| :--- | :--- |
| Read collaboration state (messages, inbox, threads) | Yes |
| Read bounded repository context | Yes |
| Read artifacts / evidence / findings | Yes |
| Submit reviews, findings, evidence | Yes |
| Challenge / resolve findings | Yes |
| Create bounded tasks (MeshCommit change proposals) | No - see [Single writer](#single-writer-invariant) |
| Publish collaboration messages | Yes |
| Write repository files | **Never** |
| Execute commands | **Never** |
| Be invoked directly by HarnessMesh (autonomous execution) | **Never** - see [Passive semantics](#passive-semantics) |

An example full configuration is in [`configs/chatgpt-claude.json`](../configs/chatgpt-claude.json).

### Single-writer invariant

At most one participant may be `writable: true`, enforced both at config-validation time (`internal/config`) and at runtime. MeshCommit's `change.create` and commit path additionally re-check that the acting participant resolves to a writable, managed (`execution_mode: managed`) agent - an external participant can never open or commit a change transaction, even if it somehow authenticates with a valid token. This means ChatGPT can *propose* work (via a message, a finding, or a review comment) but cannot itself open the transaction that would eventually touch the repository; only the configured writable executor (normally Claude Code, via the bridge) can do that.

### Passive semantics

HarnessMesh cannot inject a new message into an already-open ChatGPT conversation just because a background event happened - there is no such mechanism in the ChatGPT product, and HarnessMesh does not attempt to fake one. Instead:

1. Claude Code finishes work and publishes a result (evidence/finding/message), mentioning `@chatgpt-browser`.
2. HarnessMesh queues this in `chatgpt-browser`'s inbox (`collaboration.inbox`).
3. The next time the user invokes the HarnessMesh ChatGPT app/tool in a ChatGPT conversation, ChatGPT pulls the inbox and sees the update.

This pull model is reflected directly in the participant's `mode: "passive"` setting and the fact that HarnessMesh never attempts to autonomously invoke an `execution_mode: external` harness (`internal/agent`'s `ErrExternalParticipantCannotBeInvoked`, enforced in `Engine.Ask`/`Converse`).

## Credit isolation

This is the single most important guarantee of this integration:

```text
OpenAI API credits   = untouched
Codex usage/quota    = untouched
```

**How it is enforced, not just claimed:**

1. **Structurally.** `execution_mode: external` agents are never built into the invocable harness map (`cmd/harnessmesh/main.go`'s `mcp serve`/`bridge serve` skip them entirely before any adapter is constructed). There is no code path from the ChatGPT participant to `exec.Command("codex", ...)` or an HTTP call to `api.openai.com`.
2. **At config-load time.** `internal/config` validation rejects, outright, any `execution_mode: external` agent configured with a metered adapter (`openai`, `openai-api`, `codex`) or model-routing backend, via `internal/creditguard.CheckParticipant`. This fails closed: an unset `credit_isolation` defaults to `"strict"`.
3. **At runtime, explicitly.** `HARNESSMESH_CHATGPT_CREDIT_ISOLATION=strict` (the default) is the fail-closed switch. Setting it to `off` is only for operators with a deliberate, non-ChatGPT-bridge reason to run something unusual, and is never the default.
4. **Empirically, via telemetry.** `internal/agent`'s `OpenAIAdapter` and `CodexAdapter` unconditionally increment `internal/creditguard`'s call counters the moment they would make a real network/subprocess call - regardless of why. These are exposed as `harnessmesh_metered_backend_calls_total{backend="openai-api"|"codex"}` on `/metrics`. Note this counter is process-global: if the same process also runs a legitimate, separately-configured managed OpenAI/Codex executor, its normal traffic will also increment it. Treat it as an operational sanity signal, not as per-request proof - the actual isolation guarantee is #1-#3 above plus the end-to-end test in #5.
5. **Proven by an automated end-to-end test.** `internal/mcp`'s `TestStreamableHTTP_ScenarioF_FullLifecycleWithCreditIsolationProof` runs a full ChatGPT-bridge task lifecycle (inbox pull, finding submission, denied `change.create`) over the real Streamable HTTP transport with `OPENAI_API_KEY` set and a tripwire `codex` binary on `PATH`, and asserts the metered-call counters stay at zero and the tripwire file is never created.

`knowledge.search`/`knowledge.context` and every other tool reachable by `chatgpt-browser` operate over HarnessMesh's own local/provider-neutral index and never implicitly require an external embedding API call, even if `OPENAI_API_KEY` happens to be present in the environment for an unrelated, separately-configured managed agent.

### Usage accounting - read this carefully

- **OpenAI API: NOT USED** by this integration path. No metered API calls are made on ChatGPT's behalf.
- **Codex: NOT USED** by this integration path.
- **ChatGPT: normal ChatGPT product usage applies.** Using the ChatGPT app/connector consumes your normal ChatGPT plan usage (message/session limits, rate limits, etc.), exactly like any other ChatGPT conversation or connector use. HarnessMesh does not make ChatGPT itself free or unlimited, and does not change your ChatGPT billing in any way - it only guarantees that using this integration does **not** additionally burn separate, metered OpenAI API credits or Codex quota.

## Developer mode / remote MCP setup

1. Generate a bearer token and print setup instructions:

   ```bash
   harnessmesh integrate chatgpt --config configs/chatgpt-claude.json --repo .
   ```

   This writes/validates the `chatgpt-browser` participant config and prints the endpoint, token, and capability summary - it never writes any ChatGPT-side configuration file (ChatGPT has none to write; you paste the endpoint/token into ChatGPT's own connector UI).

2. Start the remote MCP server:

   ```bash
   harnessmesh mcp serve --listen 127.0.0.1:8787 --token "$HARNESSMESH_MCP_TOKEN"
   ```

3. In ChatGPT's developer/connector settings, add a custom remote MCP server pointing at your publicly reachable `/mcp` endpoint (see [Remote connectivity](#remote-connectivity) - `127.0.0.1` is not reachable from ChatGPT's servers on its own).

4. Verify readiness at any time:

   ```bash
   harnessmesh doctor --config configs/chatgpt-claude.json
   ```

   This checks: participant role/writability, single-writer status, credit isolation, and whether the local bridge/token are configured.

### Local development

For local iteration, run the server on `127.0.0.1` and use `harnessmesh doctor` plus direct `curl`/MCP-client testing against `http://127.0.0.1:8787/mcp`. ChatGPT itself cannot reach `127.0.0.1` on your machine, so exercising the actual ChatGPT connector requires a tunnel (below) even during development, unless you are testing purely against the local stdio/HTTP surface with another MCP client.

### Remote connectivity

HarnessMesh does not implement its own tunneling. Use one of:

- **OpenAI's Secure MCP Tunnel** mechanism, if and where currently supported for your ChatGPT plan - check OpenAI's current documentation, since this is a fast-moving product surface.
- A standard reverse proxy (nginx, Caddy, Cloudflare Tunnel, etc.) terminating TLS in front of HarnessMesh.

### Production deployment

```text
ChatGPT
   │
 HTTPS
   ▼
Reverse Proxy / Tunnel  (TLS termination)
   │
   ▼
HarnessMesh MCP (/mcp)
```

HarnessMesh's `mcp serve` supports native TLS (`--tls-cert`/`--tls-key`) for simple deployments, but a dedicated reverse proxy or tunnel is the recommended production topology. Never expose plaintext remote MCP over the public internet.

## Tool permissions and write-confirmation behavior

`chatgpt-browser`'s effective permissions (enforced in `internal/collaboration` and `internal/mcp`, not just by convention):

```text
ALLOW  collaboration.read, collaboration.publish
ALLOW  artifact/evidence read, evidence submit
ALLOW  finding submit / challenge / resolve
ALLOW  review submit
DENY   repository.write
DENY   process.execute
DENY   change.create / change.prepare / change.* commit path
DENY   secret file read (same context-projection filtering as every other participant)
```

Because ChatGPT can never reach a write-capable tool for this participant, there is no separate "write confirmation" UI step to implement on the HarnessMesh side - the write path simply does not exist for this role. If ChatGPT's own connector UI asks the user to confirm a tool call before invoking it (a ChatGPT-side behavior, not a HarnessMesh one), that confirmation still governs read/propose-oriented tools as ChatGPT's client sees fit.

## Troubleshooting

| Symptom | Likely cause | Fix |
| :--- | :--- | :--- |
| ChatGPT connector fails to add the server | `/mcp` not reachable from the public internet | Set up a reverse proxy/tunnel; verify with `curl https://<your-endpoint>/healthz` |
| `401 unauthorized` from `/mcp` | Missing/incorrect bearer token | Re-check the token pasted into ChatGPT's connector config matches `HARNESSMESH_MCP_TOKEN` |
| `ChatGPTCreditIsolationViolation` at startup | `chatgpt-browser` (or another `execution_mode: external` agent) is configured with `adapter: "openai"`/`"openai-api"`/`"codex"` | Use `adapter: "mcp-remote"` for external participants; this is enforced at config-load time by design |
| ChatGPT can see updates but Claude Code doesn't react | The bridge (`harnessmesh bridge serve`) isn't running, or the VS Code extension isn't connected | Run `harnessmesh doctor`; check the bridge's `/healthz` and the extension's status bar |
| ChatGPT tries to open a change transaction and gets denied | Expected - see [Single writer](#single-writer-invariant) | Have ChatGPT publish a message/finding instead; the writable executor opens the change |
| `429 rate limit exceeded` | Default rate limit (120 req/min) exceeded | Raise `HARNESSMESH_MCP_RATE_LIMIT` if legitimately needed |

## Limitations

- No push-into-active-conversation delivery; ChatGPT is pull-based (see [Passive semantics](#passive-semantics)) - this is a ChatGPT product constraint, not a HarnessMesh design choice.
- Which ChatGPT plans support custom remote MCP connectors, and whether they distinguish read-only vs. write-capable tool support, is set by OpenAI and changes over time; check OpenAI's current documentation rather than assuming this document's snapshot is current.
- No local HarnessMesh-implemented tunneling; a reverse proxy or OpenAI's own supported tunnel mechanism is required for a ChatGPT-reachable endpoint.
- OAuth support is bearer-token plus RFC 7662 introspection against an external authorization server; there is no local OAuth authorization-code/dynamic-client-registration server. If your deployment requires that, front HarnessMesh with an OAuth-capable reverse proxy.
