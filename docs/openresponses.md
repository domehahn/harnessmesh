# OpenResponses compatibility

HarnessMesh exposes a bounded Responses API provider gateway at
`POST /v1/responses`. The gateway is designed to be usable by Codex custom
providers and can also be checked against the open-source
[OpenResponses specification](https://github.com/openresponses/openresponses).

OpenResponses is a wire contract and compliance suite. It does not replace
HarnessMesh's collaboration plane, MCP server, VS Code bridge, routing,
budgets, audit trail, persistence, or provider policy. The intended boundary
is:

```text
OpenResponses contract/tests
            |
            v
HarnessMesh /v1/responses provider gateway
            |
            +-- local OpenAI-compatible backend
            +-- Bedrock
            +-- ChatGPT subscription backend
            +-- Codex-compatible adapter
```

## Compatibility scope

The relevant contract areas are:

- request and response item shapes;
- response lifecycle and semantic SSE events;
- streamed text and function-call argument deltas;
- tool calls and tool-call output replay;
- multi-turn continuation with `previous_response_id`;
- model discovery through `/v1/models`;
- authenticated error responses and malformed-request handling.

HarnessMesh may retain additional Codex-specific fields and headers. Clients
that deserialize standard JSON should ignore those extensions. Codex remains a
separate compatibility profile: OpenResponses compliance must not be treated
as proof that every Codex UI request or Codex-specific catalog dialect is
supported.

## Legacy Chat Completions adapter

For older clients HarnessMesh also exposes an authenticated
`POST /v1/chat/completions` compatibility route. It translates ordinary
messages and function tools into the canonical `/v1/responses` pipeline and
translates the final response back to the Chat Completions shape. Policy,
routing, audit, fallback, and credit isolation therefore remain in one
execution path.

The adapter intentionally supports non-streaming requests only. Streaming
clients should use `/v1/responses` with `"stream": true`; it emits semantic
Responses events and is the stable contract for Codex and OpenResponses.
Unsupported legacy streaming requests fail explicitly with `501`, rather than
silently buffering or changing their event semantics.

## Run the upstream compliance suite

The upstream suite is intentionally not part of the default production gate.
It is an external developer tool, may evolve independently, and should be
pinned to a reviewed OpenResponses commit or release in CI.

1. Check out OpenResponses at the version you want to validate:

   ```bash
   git clone https://github.com/openresponses/openresponses.git ../openresponses
   cd ../openresponses
   git checkout <reviewed-commit-or-tag>
   cd ../harnessmesh
   ```

2. Start a HarnessMesh provider with a deterministic local or test backend:

   ```bash
   export HARNESSMESH_PROVIDER_TOKEN="$(openssl rand -hex 32)"
   harnessmesh provider serve \
     --config configs/codex-provider.example.json \
     --listen 127.0.0.1:8789 \
     --token "$HARNESSMESH_PROVIDER_TOKEN"
   ```

3. Record the reviewed checkout commit and run the compatibility suite:

   ```bash
   export OPENRESPONSES_COMMIT="$(git -C ../openresponses rev-parse HEAD)"
   OPENRESPONSES_DIR=../openresponses \
   OPENRESPONSES_COMMIT="$OPENRESPONSES_COMMIT" \
   OPENRESPONSES_API_KEY="$HARNESSMESH_PROVIDER_TOKEN" \
     make openresponses-compliance
   ```

For release or CI validation, pin the exact reviewed commit. The helper
refuses to continue when the checkout does not match it:

```bash
OPENRESPONSES_DIR=../openresponses \
OPENRESPONSES_COMMIT=<reviewed-40-character-commit> \
OPENRESPONSES_API_KEY="$HARNESSMESH_PROVIDER_TOKEN" \
  make openresponses-compliance
```

The helper invokes the upstream documented command:

```bash
bun run test:compliance \
  --base-url http://127.0.0.1:8789/v1 \
  --api-key "$HARNESSMESH_PROVIDER_TOKEN"
```

Use another endpoint with `OPENRESPONSES_BASE_URL`. The upstream CLI currently
accepts the API key as a command-line argument, so run this on a trusted build
runner and do not use a shared multi-user host. The key is never committed by
HarnessMesh. The helper requires a Git checkout and prints the exact tested
commit for build evidence.

## CI policy

For a CI job, pin the OpenResponses checkout and set `OPENRESPONSES_COMMIT`
before running the helper after starting the provider. Do not fetch `main` at
test time and do not enable metered OpenAI/Codex backends for this test. A
compatibility failure should block a release only after the OpenResponses
version, expected feature set, and provider backend have been reviewed.

The regular `make production-readiness` gate remains deterministic and does
not require Bun, network access, an OpenResponses checkout, or live model
inference. The existing Go contract tests continue to cover HarnessMesh's
Codex-specific behavior.

## Compatibility decision rule

When a test exposes a mismatch:

1. fix the standard Responses behavior if it is safe for existing Codex
   clients;
2. add a focused Go regression test and update the compatibility matrix;
3. preserve a Codex-specific adapter where the wire shapes intentionally
   differ, especially for model catalogs or client-only metadata;
4. document any unsupported OpenResponses feature instead of silently
   accepting and dropping it.
