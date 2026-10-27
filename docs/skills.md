# HarnessMesh Skills

Skills are the workflow layer above MCP. MCP still performs the actual tool
transport; a selected skill limits which tools are visible and callable.

## Lifecycle

1. HarnessMesh discovers only `skill.json` metadata.
2. A caller selects a skill by name or trigger text.
3. HarnessMesh loads the matching `SKILL.md` lazily.
4. The hash is verified when `skills.require_hash` or
   `HARNESSMESH_SKILL_REQUIRE_HASH=1` is enabled.
5. `tools/list` exposes only the tools declared by the skill.
6. `tools/call` enforces the same allowlist, so hiding a tool is not the
   security boundary by itself.

Example manifest:

```json
{
  "name": "repository-review",
  "version": "1.0.0",
  "triggers": ["review repository", "security review"],
  "capabilities": ["review", "evidence"],
  "tools": ["peer.request_review", "peer.submit_finding"],
  "risk": "medium",
  "sha256": "..."
}
```

Skills must not be treated as authorization by themselves. The MCP registry,
tenant policy, approval workflow, tool lock and security gate remain enforced.
For high-risk skills, use signed marketplace artifacts and require an explicit
approval before activation.

## Selection

```bash
harnessmesh mcp serve --skill harnessmesh-review --skill-root plugins
```

Or configure it:

```json
{
  "skills": {
    "enabled": true,
    "root": "plugins",
    "selected": "harnessmesh-review",
    "require_hash": true
  }
}
```

The selected skill's metadata is small enough to use for routing. The full
Markdown document is loaded only after a match. HarnessMesh records the
selected skill, version, hash status and estimated schema-token reduction in
the process log; downstream telemetry can attach that information to the
session trace.

## Dashboard

When the embedded dashboard is enabled, `GET /api/v1/dashboard/skills` returns
the tenant-authenticated skill catalog. It contains names, versions, declared
tools, risk, verification state and the schema-token estimate, but never the
skill document contents or internal filesystem paths. The Settings view shows
the same metadata so operators can confirm which workflow surface is active.

`skills.require_signed` is intentionally separate from hash verification. A
deployment that requires signed marketplace artifacts must reject unsigned
manifests in its registry/approval policy; the built-in example skills are
hash-pinned and unsigned by default.
