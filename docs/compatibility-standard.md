# HarnessMesh Compatibility Standard

`harnessmesh-compatible-v1` is the first machine-readable profile for an agent
runtime that wants to interoperate with HarnessMesh. A certified runtime must
support MCP and A2A discovery, signed evidence, the Action Firewall, replay,
OpenTelemetry export and trusted marketplace admission.

The profile is intentionally capability-based. It does not require a specific
model, cloud or framework. Implementations can validate their declaration with
the `internal/certification` package and publish the result alongside their
Agent Card and plugin manifest.

SDKs are shipped under `sdk/go`, `sdk/typescript` and `sdk/python`. They expose
the authenticated tenant dashboard projections without embedding provider
credentials or bypassing tenant policy.

Certification is a compatibility claim, not a security guarantee. Production
operators must still verify keys, policies, sandboxing, identity configuration,
restore evidence and provider limits in their own environment.
