# Codex Provider Gateway

This document describes how to use the official Codex VS Code extension / Codex CLI with HarnessMesh as a **custom model provider**, so the actual model inference is served by a backend of your choosing - a local model, AWS Bedrock, or (if you explicitly disable zero-credit mode) the real OpenAI API - instead of OpenAI's own Codex inference.

## Architecture

```text
VS Code
  │
  ▼
Official Codex VS Code Extension
  │
  │ Codex-configured custom model provider
  │ wire_api = "responses"
  ▼
HarnessMesh Provider Gateway (this document)
  │
  ├── openai-compatible backend  (vLLM, Ollama, LM Studio, llama.cpp, ...)
  ├── bedrock backend            (AWS Bedrock ConverseStream)
  ├── openai-api backend         (the real OpenAI API - denied by default)
  ├── codex backend              (Codex CLI as inference - denied by default)
  └── chatgpt-subscription       (official ChatGPT plan usage via Sign in with ChatGPT, see below)
       │
       ▼
Model inference
```

This is a **separate plane** from HarnessMesh's collaboration engine (MCP, the VS Code collaboration bridge, ChatGPT-as-peer - see [docs/chatgpt-integration.md](chatgpt-integration.md)). The provider gateway performs no collaboration operations and shares no server-side state with them; it is a pure model-inference transport. Both planes can run in the same HarnessMesh process/config, or independently.

**The Codex extension itself is never forked.** It talks to HarnessMesh exactly the way it would talk to any other custom model provider, via `~/.codex/config.toml`'s `model_providers` table.

## Core goal: the zero-credit request path

```text
Codex VS Code Extension
    ↓
HarnessMesh
    ↓
your configured non-Codex/non-OpenAI-API backend
    ↓
HarnessMesh
    ↓
Codex VS Code Extension
```

With this path (the default), entering a prompt in the Codex extension costs:

```text
OpenAI API usage  = 0
Codex inference usage = 0
```

The Codex extension is used purely as: the IDE frontend, agent runtime, repo-context UI, and diff/review/apply UX. HarnessMesh replaces the model-provider endpoint entirely - Codex has no idea (and no way to tell) that a local model, not OpenAI's, is answering.

## Verified Codex provider contract

Verified against current OpenAI/Codex documentation as of **2026-09-29** (`developers.openai.com/codex/config-reference`, redirects to `learn.chatgpt.com/docs/config-file/config-reference`; `developers.openai.com/api/reference/resources/responses/streaming-events`):

- `wire_api = "responses"` is the **only supported value** (and the default when omitted) - `chat`/Chat Completions support for custom providers was deprecated.
- `model_providers.<id>` fields: `name`, `base_url`, `wire_api`, one of `env_key` / `experimental_bearer_token` (discouraged) / an `auth` command table, plus `http_headers`, `env_http_headers`, `query_params`, `request_max_retries` (default 4), `stream_idle_timeout_ms` (default 300000), `stream_max_retries` (default 5), `supports_websockets`, `supports_standalone_web_search`, `requires_openai_auth`.
- `model_provider` (top-level) selects which `model_providers` entry is active and **cannot be overridden in project-scoped config files** - only in the user-scope `~/.codex/config.toml`.
- Convention: `base_url` ends in `/v1`; Codex POSTs to `{base_url}/responses`.
- Reserved provider IDs that cannot be reused for a custom provider: `openai`, `ollama`, `lmstudio`. HarnessMesh uses the id `harnessmesh`.

Because product documentation for a fast-moving CLI can change, re-verify against the current docs before relying on details not covered above.

## Setup

1. Start (or point at) a local inference backend, e.g. an OpenAI-compatible server:

   ```bash
   # any of: vLLM, Ollama (--api openai-compatible mode), LM Studio, llama.cpp server
   ```

2. Configure HarnessMesh's provider gateway in `harnessmesh.json` (a full example, including a fallback backend and an explicit `denied_backend_types`, is at [`configs/codex-provider.example.json`](../configs/codex-provider.example.json)):

   ```json
   {
     "version": 2,
     "agents": {
       "placeholder": { "kind": "fake", "role": "executor", "writable": true }
     },
     "provider": {
       "enabled": true,
       "listen": "127.0.0.1:8789",
       "default_backend": "local",
       "backends": {
         "local": {
           "type": "openai-compatible",
           "base_url": "http://127.0.0.1:8000/v1",
           "model": "your-local-model-id"
         }
       }
     }
   }
   ```

   `zero_credit_mode` is not set here, so it defaults to `true` (fail closed). The `agents.placeholder` entry is required by `internal/config`'s "at least one agent must be configured" invariant even for a provider-only deployment - `provider serve` never reads `cfg.Agents` itself, but the check is deliberately not relaxed, since a zero-agent config would otherwise let MeshCommit's single-writer enforcement silently fail open if the same file were ever reused for `mcp serve`/`bridge serve`.

3. Start the gateway:

   ```bash
   export HARNESSMESH_PROVIDER_TOKEN="$(openssl rand -hex 32)"
   harnessmesh provider serve --config harnessmesh.json
   ```

4. Generate the Codex `config.toml` block:

   ```bash
   harnessmesh integrate codex-provider --scope user --listen 127.0.0.1:8789 --model harnessmesh-local
   ```

   This writes (merging into any existing `~/.codex/config.toml`, backing it up first):

   ```toml
   model = "harnessmesh-local"
   model_provider = "harnessmesh"

   [model_providers.harnessmesh]
   name = "HarnessMesh"
   base_url = "http://127.0.0.1:8789/v1"
   wire_api = "responses"
   env_key = "HARNESSMESH_PROVIDER_TOKEN"
   ```

   Set the env var Codex will read the bearer token from:

   ```bash
   export HARNESSMESH_PROVIDER_TOKEN="<the same token from step 3>"
   ```

5. Open VS Code, open the Codex extension, and start a conversation. Verify readiness first:

   ```bash
   harnessmesh provider doctor --config harnessmesh.json
   ```

### Project-scoped config

`--scope project` writes `./.codex/config.toml` inside a repository instead. Per Codex's documented behavior, `model_provider` cannot be set at project scope, so only the `model_providers.harnessmesh` table is written; the top-level `model`/`model_provider` selection must still happen in the user-scope file (or be passed via Codex's own CLI flags/env, outside HarnessMesh's control).

## Backends

| Type | What it is | Metered under zero-credit mode? |
| :--- | :--- | :--- |
| `openai-compatible` | A local/self-hosted server speaking OpenAI's Chat Completions wire (vLLM, Ollama, LM Studio, llama.cpp) | No |
| `bedrock` | AWS Bedrock via the official AWS SDK v2 `ConverseStream` API, using the standard AWS credential chain | No (add to `denied_backend_types` if you want it forbidden too) |
| `openai-api` | The real, metered OpenAI API | **Yes - denied by default** |
| `codex` | The Codex CLI itself, invoked as an inference engine (reuses the existing, tested `internal/agent` Codex adapter) | **Yes - denied by default** |
| `chatgpt-subscription` | The user's ChatGPT plan entitlement, via OpenAI's official "Sign in with ChatGPT" mechanism - see below | No (metered API/Codex-exec); **yes**, shares the ChatGPT-plan usage allowance with Codex on bundled plans - read the caveat below |

No backend is ever selected merely because credentials happen to exist in the environment (`OPENAI_API_KEY` present does not make the `openai-api` backend reachable) - selection is always explicit, via `default_backend` or an operator-set `X-HarnessMesh-Backend` header, and always re-checked against policy at request time.

### The `chatgpt-subscription` backend: official ChatGPT plan usage

As of research conducted 2026-09-30, OpenAI officially and publicly documents a mechanism for exactly this: **"Sign in with ChatGPT"** (SIWC), specifically its **"ChatGPT plan usage in your open-source app"** capability.

**Official sources:**
- [developers.openai.com/siwc](https://developers.openai.com/siwc) - overview of the three SIWC integration types. Quote: *"ChatGPT plan usage is available to all open-source partners and selected private clients"* (paid/remotely-hosted apps are the ones gated behind a separate interest-form process - a locally-hosted, open-source gateway like HarnessMesh is squarely the "open-source and locally hosted apps" case, not that one).
- [developers.openai.com/siwc/token-sharing-open-source](https://developers.openai.com/siwc/token-sharing-open-source) - *"ChatGPT plan usage is an optional capability within Sign in with ChatGPT. ... your open-source app can request permission to use the user's ChatGPT plan for eligible Responses API requests."*
- [developers.openai.com/siwc/token-sharing-open-source/sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in) - the concrete OAuth/OIDC flow (PKCE, endpoints, scopes) HarnessMesh's `internal/provider/chatgpt_siwc.go` implements.
- [github.com/openai/sign-in-with-chatgpt-devkit](https://github.com/openai/sign-in-with-chatgpt-devkit) - the official devkit, targeting *"developers building open-source apps that run on a user's own machine"*.

**How it works:** an OAuth 2.0 + OIDC flow with PKCE against `auth.openai.com` grants an access token scoped `chatgpt.tokens.use.direct` (among others) for `resource=https://api.openai.com/v1`. That token authenticates a normal `POST https://api.openai.com/v1/responses` request - the exact wire protocol this gateway already speaks - but billed against the signed-in user's ChatGPT plan usage allowance, not their separate metered OpenAI API balance.

**Setup:**

```bash
harnessmesh provider auth chatgpt
# prints a real auth.openai.com URL; open it, sign in, approve ChatGPT plan usage
```

Then configure it as a backend:

```json
{
  "provider": {
    "backends": {
      "chatgpt": { "type": "chatgpt-subscription" }
    }
  }
}
```

**The caveat - read this before relying on it:** [help.openai.com's "ChatGPT Work and Codex" article](https://help.openai.com/en/articles/20001275-chatgpt-work-and-codex) states that on plans where these features are bundled, *"Codex, ChatGPT Work, ChatGPT for Excel, and Workspace Agents use a shared allowance and credit pool."* This backend never invokes the Codex CLI (`internal/creditguard.BackendCodex` stays at zero, proven by `internal/provider/e2e_test.go`'s `TestE2E_ChatGPTSubscriptionBackend_ZeroOpenAIAPIAndZeroCodex`) and never touches metered OpenAI API billing (`BackendOpenAIAPI` also stays at zero, same test) - both are real, tested guarantees. But it is **not** a usage-free, Codex-independent lane: on Plus/Pro, using it draws from the same allowance your Codex usage draws from. HarnessMesh has no visibility into OpenAI's server-side billing routing beyond what these documents state - this is not something HarnessMesh's own tests can independently verify end-to-end, since doing so would require a real ChatGPT account exercising a real browser consent flow, which no CI environment (or this implementation's own test environment) has.

**What was not, and could not be, exercised:** the real `auth.openai.com` browser consent screen, with a real ChatGPT account, approving ChatGPT-plan-usage scope, followed by a real `api.openai.com/v1/responses` call billed to that account. Every piece up to that point (PKCE generation, the authorize URL's exact parameters, the loopback callback server, the token-exchange HTTP contract, the Responses-API request/stream-parsing logic) is implemented against the documented spec and tested against fake local servers - see `internal/provider/chatgpt_siwc_test.go` and `backend_subscription_test.go`. Running `harnessmesh provider auth chatgpt` and completing the browser flow yourself is the only way to close that last gap; this documentation will not claim it was done here.

## Zero-credit mode

```json
{
  "provider": {
    "zero_credit_mode": true
  }
}
```

This is the default (equivalent to omitting the field). In strict mode:

**Forbidden:** the `openai-api` backend, the `codex` backend, and any `fallback.order` entry or `default_backend` resolving to either - rejected at config-load time, not just at runtime.

**Allowed:** `openai-compatible` (local) backends, `bedrock`, and the collaboration-plane's MCP/ChatGPT-peer path (entirely separate - see [docs/chatgpt-integration.md](chatgpt-integration.md)).

A denied request returns:

```json
{"error": {"code": "metered_backend_denied", "message": "MeteredBackendDenied: requested provider \"openai\" is forbidden by zero-credit policy: ...", "type": "policy_error"}}
```

### No silent fallback

If the configured `default_backend` fails and `fallback.enabled` is true, HarnessMesh tries the next backend in `fallback.order` - but only entries that pass the same zero-credit/allow-deny check. A `fallback.order` containing `codex` or `openai-api` is rejected at config-load time; even if it somehow weren't, `Registry.FallbackChain()` filters it out before ever trying it. If every allowed backend fails, the request returns an error - it never silently escalates to a forbidden backend.

### Explicit Codex peer vs. automatic Codex fallback

These are different things and both are correctly preserved:

- **Explicit, user-initiated Codex peer review** (`peer.request_review(peer="codex")` on the collaboration plane, or Codex reviewing code as a configured `execution_mode: managed` agent) remains fully available - that is a deliberate collaboration action, not the provider gateway silently substituting Codex for a failed local model.
- **Automatic provider-gateway fallback to Codex** (Codex extension prompt → HarnessMesh provider → silently invoke the Codex CLI to answer it) is what zero-credit mode forbids, and what the `codex` backend type's denial-by-default and the no-silent-fallback behavior above both exist to prevent.

## Provider capabilities

`GET /v1/status` (bearer-authenticated) reports each configured backend's advertised capabilities (`streaming`, `tools`, `parallel_tool_calls`, `reasoning`, `structured_outputs`, `vision`, context/output token limits). HarnessMesh does not pretend a backend supports something it doesn't - an `openai-compatible` backend that can't stream, for example, would report `streaming: false` and the gateway would fail clearly rather than fake it.

## Tool calling

The gateway fully translates tool/function calls in both directions: Codex's `tools`/`tool_choice` request fields are passed to the backend, and the backend's tool-call deltas (arguments streamed incrementally as JSON fragments) are re-emitted as the Responses-API `response.function_call_arguments.delta`/`.done` and `response.output_item.added`/`.done` events, exactly as a real Responses-API server would. A `function_call_output` input item on Codex's next turn (the tool's result) is translated back into the backend's expected tool-result message shape. Malformed tool-call output from a backend is treated as a stream error, not silently dropped or collapsed into plain text.

## Security

- **Separate auth from MCP/the bridge.** The provider gateway has its own bearer token (`provider.token` / `HARNESSMESH_PROVIDER_TOKEN`), independent of the MCP token and the VS Code bridge token. A provider-gateway token never grants collaboration or admin operations - there are none reachable from this server at all.
- **Loopback by default.** `provider.listen` defaults to `127.0.0.1:8789`. Binding `0.0.0.0` requires an explicit, deliberate config change.
- **Fails closed on missing auth.** `provider serve` refuses to start without a token.
- **Rate limiting, payload size limits, request timeouts, and concurrency bounds** are enforced on every request.
- **Self-recursion guard.** If a configured backend's `base_url` points back at the gateway's own listen host:port (including via `localhost`/`127.0.0.1` spellings), `NewRegistry` refuses to start - this prevents an infinite Codex → HarnessMesh → "backend" → HarnessMesh loop from a copy-paste misconfiguration.
- **No secret leakage.** Authorization headers, backend API keys, and AWS credentials are never logged; the audit trail (`AuditRecord`) never includes prompts by default.

## Observability

`GET /metrics` (Prometheus text format) exposes:

```text
harnessmesh_provider_requests_total
harnessmesh_provider_errors_total
harnessmesh_provider_streams_active
harnessmesh_zero_credit_policy_denials_total
harnessmesh_metered_backend_calls_total{backend="openai-api"|"codex"}
harnessmesh_provider_backend_calls_total{backend="<name>"}
harnessmesh_provider_backend_failures_total{backend="<name>"}
```

`GET /v1/status` (bearer-authenticated) exposes the same counters plus the resolved policy and per-backend capabilities as JSON, for a dashboard or a quick `curl` check. Neither endpoint ever includes secrets.

## CLI

```text
harnessmesh provider serve [--config <path>] [--listen <addr>] [--token <token>]
harnessmesh provider doctor [--config <path>]
harnessmesh integrate codex-provider [--scope user|project] [--listen <addr>] [--model <id>] [--dry-run] [--check]
```

`harnessmesh doctor` (no subcommand) also includes the provider-gateway diagnostics section.

## Manual VS Code verification

Automated tests exercise the full request lifecycle against a fake local backend (see `internal/provider/e2e_test.go`), but only a human running the real Codex extension can confirm the IDE-side UX. Steps:

1. `harnessmesh provider serve --config harnessmesh.json` (with a real local backend configured and reachable).
2. `harnessmesh integrate codex-provider --scope user --listen 127.0.0.1:8789`, then `export HARNESSMESH_PROVIDER_TOKEN=...`.
3. Open VS Code in a repository.
4. Open the Codex extension panel.
5. Confirm the selected model/provider shown in the extension matches `harnessmesh` / your configured model id.
6. Submit a prompt.
7. Confirm HarnessMesh's `provider serve` process logs/metrics show the request (`GET /v1/status` request count increments).
8. Confirm your local backend process shows the inbound request.
9. Confirm the response streams incrementally into the Codex UI (not a single delayed dump).
10. Check `curl -s http://127.0.0.1:8789/metrics | grep openai-api` - should read `0`.
11. Confirm no `codex` CLI process was spawned (`ps aux | grep codex` shows only the Codex extension's own process, not a child invocation from HarnessMesh).

## Troubleshooting

| Symptom | Likely cause | Fix |
| :--- | :--- | :--- |
| Codex extension shows an error adding the provider | `wire_api` isn't `"responses"`, or the config.toml syntax is stale | Regenerate with `harnessmesh integrate codex-provider`; re-verify against current Codex docs if this guide's snapshot is outdated |
| `401` from the gateway | `HARNESSMESH_PROVIDER_TOKEN` doesn't match `provider.token`, or wasn't exported before starting Codex | Re-export and restart the Codex extension/CLI session |
| `403 metered_backend_denied` | Expected - you (or Codex, via a stray header) requested `openai-api`/`codex` while `zero_credit_mode` is on | Configure a non-metered `default_backend`; only disable zero-credit mode deliberately |
| `503` from `/readyz` | No allowed backend is currently healthy | Check the backend's own health/logs; `harnessmesh provider doctor` reports the specific failure |
| Request hangs / times out | Backend unreachable, or `request_timeout_ms`/backend timeout too low for the model's actual latency | Check backend logs; raise `provider.request_timeout_ms` and the backend's own `timeout_sec` |
| "would create an infinite request loop" at startup | A backend's `base_url` points at the gateway's own listen port | Point it at the actual inference server's port, not the gateway's |

## Usage accounting

```text
OpenAI API:  NOT USED by the zero-credit provider path.
Codex:       NOT USED by the zero-credit provider path.
```

If you explicitly set `zero_credit_mode: false` and configure an `openai-api` or `codex` backend, normal OpenAI API billing / Codex CLI usage applies to those requests - exactly as if you'd called them directly. HarnessMesh does not change OpenAI's or Codex's own billing model; it only guarantees that the zero-credit path never reaches them.

## The two modes, explicitly

**Mode A - Codex native backend:** Codex extension configured with its default OpenAI provider. Codex/OpenAI quota and billing apply as normal. HarnessMesh is not involved in this mode at all.

**Mode B - Codex extension + HarnessMesh custom provider (this document):** Codex extension configured with `model_provider = "harnessmesh"`. Whichever backend HarnessMesh's `default_backend` resolves to actually serves the request. In zero-credit mode, that backend can never be the OpenAI API or Codex itself.
