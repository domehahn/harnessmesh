# HarnessMesh

**HarnessMesh is a collaboration fabric for coding-agent harnesses.**

It automates the workflow many developers still perform manually:

```text
Claude Code implements
        ↓
copy result to another model
        ↓
second model reviews
        ↓
copy feedback back to Claude
        ↓
Claude fixes
        ↓
review again
```

HarnessMesh turns that into a controlled peer loop:

```text
                    HarnessMesh
                        │
          ┌─────────────┴─────────────┐
          ▼                           ▼
   Claude Code                    Codex
   EXECUTOR                      REVIEWER
          │                           │
          │ implementation            │
          ├──────── context ─────────►│
          │                           │ inspect / review
          │◄──── structured findings ┤
          │                           │
          │ fixes                     │
          ├──────── context ─────────►│
          │                           │
          │◄──────── APPROVE ─────────┤
```

The harnesses remain responsible for their native capabilities: repository access,
shell execution, edits, tests, git, MCP, sandboxing, and agent loops. HarnessMesh
coordinates **communication between harnesses**.

It can optionally place [NVIDIA NeMo Switchyard](https://github.com/NVIDIA-NeMo/Switchyard)
under both harnesses as the model-routing plane.

> Independent community project. Not affiliated with or endorsed by OpenAI,
> Anthropic, or NVIDIA.

## Why HarnessMesh instead of another LLM router?

Switchyard already handles model selection, routing, translation, stage signals,
advisor gates, and sub-agent-aware routing. HarnessMesh deliberately targets a
different layer:

```text
                  Collaboration plane
                     HarnessMesh
                         │
           ┌─────────────┼─────────────┐
           │             │             │
        Codex        Claude Code    future harness
           │             │             │
           └─────────────┼─────────────┘
                         │
                    Routing plane
                     Switchyard
                         │
               ┌─────────┼─────────┐
               ▼         ▼         ▼
             local    efficient  capable
```

HarnessMesh v0.1 focuses on one high-value collaboration primitive:

**executor → independent reviewer → feedback → executor → re-review**

## Status

`v0.1.0` is an MVP.

Implemented:

- Claude Code executor or reviewer adapter
- Codex executor or reviewer adapter
- persistent harness sessions where the CLI exposes them
- structured peer-review protocol
- repository context projection
- git diff / status / branch / HEAD capture
- optional test-command capture
- material finding IDs and repeat detection
- `APPROVE`, `CHANGES_REQUIRED`, and `BLOCK`
- max-round and wall-time guards
- per-agent timeouts
- Claude Code budget cap support
- read-only reviewer mode
- machine-readable JSON + Markdown reports
- optional Switchyard endpoint injection
- GitHub-ready CI, CodeQL, Dependabot and security docs

Not implemented yet:

- both agents writing concurrently
- worktree-per-agent isolation
- debate / consensus policies
- MCP-native peer transport
- long-running daemon/API mode
- semantic context selection
- cost ledger across vendors
- learned collaboration policy

## Requirements

- Go 1.23+ to build
- Git
- at least the configured harness CLIs:
  - `claude`
  - `codex`
- valid authentication for those tools
- optional Switchyard

HarnessMesh does not convert consumer subscriptions into APIs and does not proxy
browser cookies or private web endpoints. Each harness and upstream must use its
normal supported authentication/entitlements.

## Quick start

```bash
git clone https://github.com/domehahn/harnessmesh.git
cd harnessmesh

./scripts/setup.sh
```

Fish:

```fish
./scripts/setup.fish
```

This creates:

```text
harnessmesh.json
bin/harnessmesh
```

Check the environment:

```bash
bin/harnessmesh doctor --config harnessmesh.json
```

Then run a collaboration:

```bash
bin/harnessmesh collaborate \
  --config harnessmesh.json \
  --repo /path/to/repository \
  --task "Implement the requested feature and add tests."
```

Or keep the task in a file:

```bash
bin/harnessmesh collaborate \
  --repo /path/to/repository \
  --task-file task.md
```

## Default collaboration

The example config uses:

```text
Claude Code = executor
Codex       = reviewer
```

The reviewer is read-only. Only the executor is expected to mutate the working
tree.

`configs/harnessmesh.example.json`:

```json
{
  "workflow": {
    "executor": "claude-executor",
    "reviewer": "codex-reviewer",
    "max_rounds": 3,
    "stop_on_repeat": true
  }
}
```

To reverse the roles, define a writable Codex agent and a read-only Claude agent,
then swap `executor` and `reviewer`.

## Collaboration lifecycle

Each round is:

```text
1. executor works task
2. HarnessMesh snapshots repo state
3. optional tests run
4. reviewer receives:
   - original task
   - executor summary
   - HEAD / branch / status
   - diff stat
   - bounded diff
   - test output
5. reviewer returns structured JSON
6. APPROVE -> stop
7. BLOCK -> stop
8. CHANGES_REQUIRED -> inject findings into executor session
9. repeat
```

The review format is intentionally evidence-oriented:

```json
{
  "verdict": "changes_required",
  "summary": "Concurrency handling is incomplete.",
  "findings": [
    {
      "id": "HM-RACE-001",
      "severity": "high",
      "file": "internal/auth/token.go",
      "line": 87,
      "claim": "Refresh token replacement is unsynchronized.",
      "evidence": "RefreshToken writes shared token state without the existing mutex.",
      "recommendation": "Protect the replacement and add a concurrent refresh test."
    }
  ],
  "questions": []
}
```

Stable finding IDs let HarnessMesh detect a loop where the same material findings
are returned in consecutive rounds.

## Context projection

HarnessMesh does **not** blindly copy an entire Claude or Codex transcript to the
peer.

It projects a bounded collaboration context:

```text
original task
executor completion summary
git HEAD
branch
git status
diff stat
git diff
optional test output
untracked filenames
```

Large diffs and test output are truncated middle-out, preserving the beginning
and the most recent/end context.

That keeps peer review focused and reduces model cost.

## Reports

Every run writes outside the target repository by default:

```text
~/.harnessmesh/runs/<repo-key>/<timestamp>/
├── report.json
└── report.md
```

Set `HARNESSMESH_STATE_DIR` to choose another state directory. This keeps
orchestration artifacts out of the repository being reviewed.

## Claude Code adapter

HarnessMesh uses Claude Code's non-interactive print mode:

```text
claude -p --output-format json ...
```

For executor sessions it can use a writable permission mode such as:

```text
acceptEdits
```

For review mode HarnessMesh disables Claude tools and requests structured output
with `--json-schema`.

Agent-specific limits can be configured:

```json
{
  "max_turns": 50,
  "max_budget_usd": 5.0,
  "timeout_minutes": 45
}
```

## Codex adapter

HarnessMesh uses:

```text
codex exec --json
```

It captures `thread.started`, persists the thread ID, and uses:

```text
codex exec ... resume <thread-id> -
```

for follow-up peer messages.

Reviewer mode uses:

```text
--sandbox read-only
--output-schema <schema>
```

The final message is captured with `--output-last-message`.

## Optional: NVIDIA NeMo Switchyard

HarnessMesh does not fork Switchyard.

Instead, Switchyard can sit underneath both harnesses:

```text
HarnessMesh
   │
   ├── Claude Code ──┐
   │                 ├── Switchyard ── model pool
   └── Codex ────────┘
```

The included example files are:

```text
configs/harnessmesh.switchyard.example.json
configs/switchyard.routes.toml
```

Start Switchyard using its own supported installation and point it at your
provider/model pool.

Then enable:

```json
{
  "switchyard": {
    "enabled": true,
    "base_url": "http://127.0.0.1:4000",
    "route_id": "switchyard"
  }
}
```

and set `"use_switchyard": true` on an agent.

For Claude, HarnessMesh sets `ANTHROPIC_BASE_URL`; for Codex it sets
`OPENAI_BASE_URL`. If your Switchyard deployment intentionally uses placeholder
client authentication while keeping upstream credentials server-side, set
`inject_placeholder_auth` explicitly. It is `false` by default.

## Tests

```bash
make check
```

or:

```bash
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/harnessmesh
```

## Safety model

The v0.1 workflow intentionally has **one writer**.

The reviewer is expected to be read-only. This avoids two autonomous harnesses
racing on the same working tree.

Before a writable/writable mode is added, HarnessMesh will use isolated git
worktrees and an explicit merge/reconciliation phase.

Other defaults:

- bounded collaboration rounds
- wall-clock limit
- no shell interpolation for configured test commands
- no secret logging
- reviewer findings require evidence
- repeated-finding loop detection
- report artifacts stay local by default

See [SECURITY.md](SECURITY.md).

## Roadmap

The next planned collaboration modes are:

```text
review
reverse-review
planner-executor
executor-verifier
debate
dual-review
evidence-resolution
```

The architectural target is:

```text
                           HarnessMesh

               ┌──────── Collaboration ────────┐
               │                                │
         Claude Code                         Codex
               │                                │
               ├──── messages / findings ───────┤
               ├──── evidence / artifacts ──────┤
               └──── decisions / state ─────────┘
                               │
                         Switchyard
                               │
                   model / provider routing
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and
[docs/PROTOCOL.md](docs/PROTOCOL.md).

## License

Apache License 2.0.
