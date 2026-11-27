---
name: harnessmesh-mcp-auth-review
description: Review MCP authentication and authorization changes.
---

# Review MCP authentication and authorization changes

1. Trace where the caller identity comes from. For each transport, check that the server derives the tenant and participant from verified credentials and applies tool authorization before invoking the tool.
2. Review token scope, audience, expiry, and secret handling. Check that missing credentials, an unknown tenant, or a denied tool result in a refusal without forwarding credentials to an external server.
3. Check existing tests for anonymous calls, cross-tenant access, and authenticated calls without the required tool permission. Cite the code or test for each finding and mark assumptions as unverified.
