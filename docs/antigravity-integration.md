# Google Antigravity & OpenAI Codex Peer Integration

HarnessMesh enables Google Antigravity in VS Code to autonomously collaborate with an OpenAI Codex peer in the background.

The developer works **exclusively** in the VS Code Antigravity chat. Whenever Antigravity requires a second opinion, architecture check, code review, debugging help, or validation, it queries the Codex peer through HarnessMesh MCP tools. The peer's response is delivered directly into Antigravity's active context without human copy-pasting.

---

## Interaction Flow

```text
User
  │
  ▼
Antigravity (VS Code Chat)
  │
  │ autonomous peer tool call (peer.converse)
  ▼
HarnessMesh MCP Server (`stdio`)
  │
  │ maps session to persistent thread (`codex exec resume <thread_id> -`)
  ▼
OpenAI Codex Peer
  │
  │ structured advice / review / question / approval
  ▼
HarnessMesh Collaboration Engine
  │
  │ tool result text returned into active agent context
  ▼
Antigravity continues working & applies recommendations
  │
  ▼
User receives completed solution in VS Code
```

---

## One-Command Setup

To integrate HarnessMesh into your current workspace:

```bash
# Preview proposed changes
harnessmesh integrate antigravity --dry-run

# Apply configuration
harnessmesh integrate antigravity --config configs/antigravity-openai-peer.json --repo .
```

### What `integrate antigravity` Configures

1. **MCP Configuration** (`.agent/mcp_config.json`):
   Registers the `harnessmesh` MCP server. Existing MCP servers (e.g. DevTools, GitHub tools) are preserved.
   ```json
   {
     "mcpServers": {
       "harnessmesh": {
         "command": "/path/to/bin/harnessmesh",
         "args": [
           "mcp",
           "serve",
           "--caller",
           "antigravity",
           "--config",
           "/path/to/configs/antigravity-openai-peer.json",
           "--repo",
           "/path/to/workspace"
         ]
       }
     }
   }
   ```

2. **Collaboration Rule** (`.agent/rules/harnessmesh.md`):
   Installs an `always_on` Antigravity rule that instructs the agent:
   - When to autonomously invoke `peer.converse` and `peer.ask`.
   - Never to ask the user to copy/paste text between chat windows or terminals.
   - How to process the peer response directly from the tool context.
   - How to follow up across multiple turns.

---

## Collaboration Configuration (`configs/antigravity-openai-peer.json`)

```json
{
  "version": 2,
  "workspace": {
    "single_writer": true
  },
  "collaboration": {
    "max_peer_rounds": 5,
    "max_conversation_turns": 6,
    "max_peer_depth": 2,
    "max_peer_calls": 15,
    "peer_selection_policy": "first"
  },
  "agents": {
    "antigravity-main": {
      "adapter": "antigravity",
      "role": "executor",
      "roles": ["executor", "main"],
      "writable": true,
      "command": "agy"
    },
    "openai-reviewer": {
      "adapter": "codex",
      "role": "reviewer",
      "roles": [
        "peer",
        "reviewer",
        "architecture",
        "correctness",
        "security",
        "debugging"
      ],
      "writable": false,
      "command": "codex",
      "mode": "read-only"
    }
  },
  "capability_routing": {
    "review": ["openai-reviewer"],
    "architecture": ["openai-reviewer"],
    "security": ["openai-reviewer"],
    "correctness": ["openai-reviewer"],
    "debugging": ["openai-reviewer"],
    "second_opinion": ["openai-reviewer"]
  }
}
```

---

## Key Features

### 1. Persistent Thread Continuity
When Antigravity initiates conversation via `peer.converse`, HarnessMesh maps the session to an OpenAI Codex thread ID. In follow-up turns, HarnessMesh resumes the exact same thread (`codex exec resume <thread_id> -`), ensuring full context retention across iterative discussions without replaying history.

### 2. Bounded Loops
To guarantee predictable execution and budget safety, conversations are bounded by:
- `max_conversation_turns`: limits back-and-forth exchanges (default: 6 turns).
- `max_peer_depth`: prevents circular reentrancy.
- `max_peer_calls`: limits total tool invocations per session.

### 3. Single-Writer Invariant
Antigravity holds the exclusive write lock on the repository workspace. Codex operates in read-only mode (`mode: read-only`, `writable: false`), eliminating concurrent write conflicts.

---

## Verification & Diagnostics

### Run Health Checks
```bash
harnessmesh doctor --config configs/antigravity-openai-peer.json
```
Output verifies:
- Antigravity MCP integration and rule installation.
- Codex CLI executable and authentication health.
- Git repository and SQLite persistence store.

### Run End-to-End Smoke Test
```bash
harnessmesh smoke-test antigravity-codex --config configs/antigravity-openai-peer.json
```

### Inspect Conversation History
```bash
# View active and recent collaboration sessions
harnessmesh session list

# View full transcript with causation IDs and latency
harnessmesh session messages <session-id>
```

