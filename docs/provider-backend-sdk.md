# Provider Backend SDK

Provider backends are registered through `provider.RegisterBackendType`. A
backend factory receives only its named static configuration and returns an
`InferenceBackend`; policy evaluation still happens in `Registry.Resolve` and
`FallbackChain` for every request.

This allows internal plugins and future separately packaged providers to add
OpenAI-compatible, Bedrock, self-hosted or organization-specific backends
without modifying the central routing switch. Plugin factories must preserve
credential separation, bounded timeouts, usage reporting and zero-API-billing
policy checks.
