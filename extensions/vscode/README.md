# HarnessMesh for VS Code

A thin VS Code UI over the local HarnessMesh collaboration-plane bridge (`internal/bridge`, REST + WebSocket). This extension performs **no LLM reasoning of any kind** - it never calls the OpenAI API, never invokes Codex, and never talks to ChatGPT directly. It only reads and writes collaboration state (messages, tasks, reviews, findings) through the bridge, the same way ChatGPT does over MCP. See [docs/chatgpt-integration.md](../../docs/chatgpt-integration.md) for the full architecture.

## What it does

- Sidebar views for **Participants**, **Tasks**, **Reviews**, **Findings**.
- Status bar item showing bridge connection state.
- Commands: Connect, Disconnect, Select Workspace, Show Collaboration Status, Show Inbox, Show Tasks, Show Reviews, Show Findings, Send Message, Set Bridge Token.
- Live updates via the bridge's WebSocket event stream, with reconnect-with-backoff and event-id deduplication.

## Domain mapping (read this before it's confusing)

HarnessMesh does not have a separate "Task" type distinct from what it already tracks. This extension's **Tasks** view maps directly onto HarnessMesh's existing **MeshCommit change transaction** model (`protocol.MeshChange`): id, status (`draft` → `prepared` → `under_verification` → `committable`/`verified` → `committed`/`aborted`), author, title, intent. This is intentional - HarnessMesh reuses its evidence-gated change-transaction model rather than inventing a parallel, competing task abstraction.

Likewise, **Reviews** and **Findings** are the same underlying resource (`protocol.FindingPayload`) served by the same bridge endpoint (`GET /api/v1/findings`, aliased at `GET /api/v1/reviews`) - a "review" is simply a finding submitted in a review context.

## Setup

1. Start the bridge from the repository you want to collaborate on:

   ```bash
   harnessmesh bridge serve --repo . --config harnessmesh.json --token <a-strong-token> --listen 127.0.0.1:8788
   ```

2. In VS Code, run **HarnessMesh: Set Bridge Token** and paste the same token. It is stored via VS Code's `SecretStorage` API, never in plaintext `settings.json`.

3. Run **HarnessMesh: Connect to Bridge**. The status bar should show `HarnessMesh: Connected`.

4. Configure `harnessmesh.bridgeUrl` (default `http://127.0.0.1:8788`) if you're running the bridge on a non-default address, and `harnessmesh.autoConnect` if you want it to connect automatically on VS Code startup (only takes effect once a token is already stored).

## Security notes

- The bridge client runs entirely in the extension host process (Node), not in a webview - there is no CORS/CSP surface here, and no page origin the bridge's strict origin allowlist needs to know about.
- The token is never written to `settings.json` or any file this extension controls; it lives only in VS Code's OS-backed secret store for this machine/profile.
- This extension can never cause a repository write or command execution on its own - all such actions go through the bridge to the configured writable/managed executor (normally Claude Code), which enforces HarnessMesh's single-writer invariant independently of this UI.

## Development

```bash
npm install
npm run compile   # or: npm run watch
```

Press F5 in VS Code (with this folder open) to launch an Extension Development Host.
