# Agent Action Firewall

HarnessMesh evaluates every normalized side effect before execution. The
`internal/assurance` action firewall is provider-neutral and can be used by MCP,
A2A, browser, provider and external-agent adapters.

An action must carry authoritative tenant, agent and operation identity. The
firewall evaluates prompt/tool content, declarative policy, risk and explicit
data flows. Cross-tenant flows are denied unless the caller marks them as
allowed through a reviewed policy path. A decision includes a stable action
digest, findings and a human-readable `blocked_why` explanation.

```go
decision, err := assurance.EvaluateAction(ctx, policy, assurance.Action{
    Tenant: "tenant-a",
    Agent: "reviewer",
    Operation: "mcp.tool.call",
    Tool: "repository.read",
}, []assurance.DataFlow{{
    Source: "tenant-a.repository",
    Destination: "reviewer",
    DataClass: "source",
}})
```

The firewall is not an authorization replacement: identity, tenant membership,
approval and sandbox enforcement remain mandatory. It is the common decision
format used to explain and audit those controls.
