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

After selection, HarnessMesh builds an execution plan from the verified
`SKILL.md`. Explicit `[fn: tool.name]` markers define the deterministic order;
undeclared tools are ignored and remaining declared tools are appended in
stable lexical order. The plan digest, steps and estimated schema-token saving
are exposed through `GET /api/v1/dashboard/skills` and the console. The plan
is an execution contract, not an authorization bypass: MCP registry policy,
the action firewall, approvals and tenant identity remain authoritative.

Example manifest:

```json
{
  "name": "repository-review",
  "version": "1.0.0",
  "triggers": ["review repository", "security review"],
  "capabilities": ["review", "evidence"],
  "tools": ["peer.request_review", "peer.submit_finding"],
  "strict_order": true,
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

# Context-driven deterministic selection
harnessmesh mcp serve --skill-context "perform a security review" --skill-root plugins
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

Selection also learns from completed executions. Successful and failed calls
are counted per skill in the running registry. After at least three observed
runs, a skill with a success rate below 50% is excluded from context-driven
selection; otherwise its success rate is used as a deterministic routing
preference when candidates are otherwise equally relevant. The current
evaluation is exposed in `GET /api/v1/dashboard/skills` and the optimization
view. This is deliberately conservative: evaluation state is process-local
until a durable evaluation store is configured, and it never overrides tenant
policy, approvals, the action firewall or tool locks.

Example workflow markers:

```markdown
## Workflow
1. Fetch repository metadata: [fn: github.repo.get]
2. Inspect open issues: [fn: github.issues.list]
3. Produce the final report.
```

Set `strict_order` to `true` when every declared step must run in sequence.
MCP rejects skipped, repeated or out-of-order steps with an explicit policy
error; failed steps remain retryable. The default is `false` for compatibility
with interactive agents.

## Dashboard

When the embedded dashboard is enabled, `GET /api/v1/dashboard/skills` returns
the tenant-authenticated skill catalog. It contains names, versions, declared
tools, risk, verification state and the schema-token estimate, but never the
skill document contents or internal filesystem paths. The Settings view shows
the same metadata so operators can confirm which workflow surface is active.

`skills.require_signed` is intentionally separate from hash verification. When
enabled, HarnessMesh verifies an Ed25519 signature over the SHA-256 digest of
`SKILL.md`, optionally against the base64 public keys in
`HARNESSMESH_PLUGIN_TRUSTED_KEYS`. Unknown or untrusted keys are rejected
before activation. The built-in example skills are
hash-pinned and unsigned by default, so signed mode must be supplied with
organization-owned signed manifests.
