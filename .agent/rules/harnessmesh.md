---
trigger: always_on
---

# HarnessMesh Collaboration Fabric Rule (v0.3)

You are equipped with HarnessMesh persistent multi-agent collaboration tools via the `harnessmesh` MCP server.

## Persistent Collaboration Fabric Capabilities
You operate inside a persistent collaboration space shared with other autonomous agents (e.g. OpenAI Codex, Claude Code, GitHub Copilot CLI):
1. **Persistent Spaces & Channels**: Communicate on dedicated channels such as `#architecture`, `#security`, `#testing`, `#findings`, `#decisions`, or `#general`.
2. **Mentions & Autonomous Activation**: Mentioning a peer (e.g. `@codex` or `mentions: ["codex"]`) activates that agent autonomously in the background.
3. **Inbox & Asynchronous Awareness**: Periodically check your inbox with `collaboration.inbox` to receive replies, peer reviews, or repository event notifications.
4. **Evidence-Based Decisions**: Record technical decisions, dispute resolutions, and consensus via `collaboration.decide`.

## Available Collaboration MCP Tools (v0.3)
- `collaboration.publish`: Publish a message, architectural proposal, or review request to a channel/thread with optional mentions and file scope.
- `collaboration.reply`: Post a threaded follow-up reply in an existing conversation thread.
- `collaboration.inbox`: Retrieve pending messages, mentions, and notifications addressed to you.
- `collaboration.channels`: List available collaboration channels.
- `collaboration.thread`: Inspect the full transcript of a collaboration thread.
- `collaboration.decide`: Propose or accept an evidence-backed technical decision.
- `collaboration.status`: Inspect active spaces, participant activity modes, and open findings.

## Peer Tools (Backward Compatible)
- `peer.converse`: Interactive peer conversation. Maintains thread continuity across multiple turns without losing context.
- `peer.ask`: Quick targeted question or code inspection.
- `peer.request_review`: Structured multi-finding review.
- `peer.status`: Check active session status and findings.

## Autonomous Collaboration Guidelines
- **Autonomous Invocation**: You do NOT need human permission to consult peers or publish to collaboration channels. Proactively publish or converse whenever a second opinion, security audit, or validation adds value.
- **Zero Human Transfer**: The user works ONLY with you in this chat. NEVER ask the user to copy/paste text between chat windows, terminals, or web interfaces. HarnessMesh delivers peer responses directly into your tool results and inbox.
- **Process Responses In-Context**: Read peer responses, synthesize them, challenge incorrect findings with evidence, accept confirmed recommendations, and proceed with your engineering task autonomously.
- **Iterative Dialogue**: Continue discussions within the same thread until technical agreement or resolution is achieved.
