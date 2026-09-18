# Changelog

All notable changes to this project will be documented in this file.

## [0.3.0] - 2026-09-19

### Added

- **Persistent Multi-Agent Collaboration Fabric**: Transformed HarnessMesh into a persistent, multi-channel, event-driven collaboration environment (`harnessmesh.collaboration/v1`).
- **Collaboration Spaces**:
  - Persistent space lifecycle (`active`, `paused`, `stopped`, `archived`) with human control plane.
  - Multi-agent participation with role and activity mode assignments.
  - Single-writer invariant enforcement per workspace/space.
- **Channels & Threads**:
  - Default structured channels created automatically: `general`, `architecture`, `security`, `testing`, `findings`, `decisions`.
  - Custom topic channels with configurable access and subscriptions.
  - Persistent threaded discussions with hierarchical causal tracking (`parent_id`, `root_id`) and participant read cursors.
- **Event Bus & Sliding-Window Coalescing**:
  - High-throughput pub/sub event bus supporting topic matching (`repository.*`, `decision.*`, `task.*`).
  - Sliding-window coalescing engine that buffers rapid filesystem changes and emits unified semantic events (e.g. `repository.changed`).
- **Participant Activity Modes & Offline Inboxes**:
  - Configurable participant modes: `active` (immediate notification), `passive` (inbox queueing), `on_demand` (direct mention or capability invocation), `paused`.
  - Durable inboxes per participant allowing offline agents to catch up on unread messages, events, and pending decisions.
  - Dynamic cooldown controller preventing rapid message floods.
- **Cycle & Causal Loop Protection**:
  - Graph-based causal loop detector analyzing thread chains and preventing agent reverberation loops (`CollaborationCycleDetectedError`).
  - Strict reentrancy limits with configurable max depth per conversation thread.
- **Verifiable Decisions**:
  - Formal decision management (`proposed`, `accepted`, `rejected`, `superseded`).
  - Evidence linkage binding test results, benchmark logs, and diffs to accepted decisions.
  - Direct integration with the `#decisions` channel.
- **9 New MCP Collaboration Tools (20 Tools Total)**:
  - `collaboration.publish`: Post messages to channels and threads with tagging and evidence.
  - `collaboration.reply`: Threaded replies to specific parent messages with causal integrity.
  - `collaboration.inbox`: Retrieve pending messages, events, and action items for an agent.
  - `collaboration.channels`: List and discover available channels and their topics.
  - `collaboration.thread`: Retrieve full thread histories with causal message trees.
  - `collaboration.subscribe`: Subscribe to channels and event topics.
  - `collaboration.unsubscribe`: Remove subscriptions.
  - `collaboration.decide`: Propose and accept formal architecture/design decisions with evidence.
  - `collaboration.status`: Inspect space health, active participants, channels, and lifecycle state.
- **CLI Management Suite**:
  - `harnessmesh space`: List, show, create, pause, resume, and stop spaces.
  - `harnessmesh channel`: List and create channels within a space.
  - `harnessmesh thread`: List threads, view thread conversations, and post replies.
  - `harnessmesh inbox`: Inspect agent inbox contents.
  - `harnessmesh subscriptions`: List and remove event subscriptions.
  - `harnessmesh decide`: Propose, list, and accept collaborative decisions.
- **SQLite Schema Migration v5**:
  - Added tables: `collaboration_spaces`, `space_participants`, `channels`, `threads`, `subscriptions`, `event_deliveries`, `decisions`, `participant_states`, `summaries`.
  - Non-destructive migration preserving existing session and peer conversation histories.
- **Antigravity Rule Integration**:
  - Updated `integrate antigravity` to inject complete collaboration instructions into `.agent/rules/harnessmesh.md`.

### Changed

- Updated MCP server version to `0.3.0` while maintaining full backward compatibility with all 11 v0.2 `peer.*` tools.
- Unified session store with space management so routing decisions and participant activities seamlessly bridge both models.

### Security

- **Sensitive File Policy**: Automatically redacts or prevents event broadcast of sensitive repository files (e.g. certificates, credentials, private keys) to untrusted or external cloud harnesses.
- **Human Emergency Controls**: Space pause/stop commands immediately block all agent publication and event delivery across the space.

---

## [0.2.0] - 2026-09-19

### Added

- **Generic N-Agent Collaboration Fabric**: Re-architected HarnessMesh from a point-to-point Claude-Codex loop into a generic, transport-independent collaboration fabric for N agents.
- **Dynamic Adapter Registry**: Introduced dynamic adapter registration (`agent.RegisterAdapter`) and uniform `Harness` interface (`ID`, `AdapterType`, `Capabilities`, `Health`, `StartSession`, `ResumeSession`, `CloseSession`, `Invoke`).
- **First-Class Adapters**:
  - **Claude Code** (`claude-code`, `claude`): Headless execution (`claude -p`), diagnostics extraction, and MCP project integration (`.mcp.json`).
  - **OpenAI Codex** (`codex`): Headless execution (`codex exec`), strict OpenAI Structured Outputs adherence, and unmasked error reporting.
  - **Google Antigravity** (`antigravity`, `agy`): Headless execution (`agy -p --dangerously-skip-permissions`), security review, and MCP integration (`.antigravity/mcp.json`).
  - **GitHub Copilot CLI** (`copilot-cli`, `copilot`): Non-interactive execution (`copilot -p --allow-all-tools`), verification role, and MCP integration (`.copilot/mcp.json`).
  - **Fake Adapter** (`fake`, `mock`): Deterministic in-memory testing harness for reliable CI execution without live cloud API requirements.
- **Capability-Based Peer Selection & Routing**:
  - Query participants by required capabilities (`security_review`, `performance_review`, `answer_questions`, `review`).
  - Deterministic peer selection policies: `first`, `round_robin`, `least_busy`.
- **Parallel Multi-Review & Conservative Deduplication**:
  - Concurrent multi-reviewer execution with bounded concurrency (`max_parallel_peers`).
  - Conservative finding deduplication linking matching file, line, and claims with `duplicate_of` and `related_findings`.
  - Reviewer error isolation preventing single-peer failures from aborting sessions.
- **10 MCP Stdio Tools**:
  - `peer.list`: Enumerate session participants and roles.
  - `peer.capabilities`: Query peers matching specific capabilities.
  - `peer.ask`: Direct targeted inquiries with capability fallback.
  - `peer.reply`: Causal responses to parent messages.
  - `peer.request_review`: Single or parallel multi-review requests.
  - `peer.submit_finding`: Publish structured defects.
  - `peer.submit_evidence`: Attach test logs, diffs, and compiler outputs.
  - `peer.challenge`: Disagree with findings and mark them as disputed.
  - `peer.resolve`: Resolve findings using verifiable evidence.
  - `peer.status`: Inspect session health and budget.
- **Durable SQLite Schema v2**:
  - Schema migrations adding `duplicate_of`, `related_findings_json`, `source_participant`, `source_adapter`, `evidence_refs_json`, and `writer_participant`.
- **Workspace Model**:
  - Repository canonicalization, git branch/HEAD tracking, and strict path traversal and symlink escape validation (`workspace.CheckPath`).
- **Configuration Migration & Profiles**:
  - Config migration tool (`harnessmesh config migrate`).
  - Ready-to-use profiles in `configs/`: `claude-codex.json`, `antigravity-codex.json`, `copilot-codex.json`, `antigravity-multi-review.json`, `codex-multi-review.json`.
- **CLI Commands**:
  - `agents list`, `agents show <name>`
  - `evidence <session-id>`
  - `config validate`, `config migrate`
  - `mcp install <claude|codex|antigravity|copilot>`
  - Enhanced `doctor` with harness adapter health and credential checks.

### Changed

- **Single-Writer Enforcement**: Enforced strictly at config validation and session creation time (exactly one writable agent per worktree).
- **Evidence-Driven Resolution**: Replaced implicit consensus with strict evidence-first resolution; no majority voting.
- **Diagnostic Transparency**: Adapter stderr and JSON errors are propagated in typed errors rather than returning generic exit codes.

### Security

- **Path Escape Rejection**: Symlink escapes outside the repository root are strictly blocked.
- **Secret Filtering**: Excludes credentials, private keys, environment files, and sensitive cloud configurations from context projections.
- **Reentrancy Ceilings**: Causal depth tracking prevents recursive peer loops.

---

## [0.1.0] - 2026-09-18

### Added

- Initial HarnessMesh release.
- Claude Code and Codex CLI adapters.
- Basic executor/reviewer collaboration loop.
- Structured review schema and findings.
- Local JSON and Markdown run reports.
- Optional NVIDIA NeMo Switchyard endpoint integration.
