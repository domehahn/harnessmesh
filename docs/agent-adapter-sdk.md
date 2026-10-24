# Agent Adapter SDK

HarnessMesh adapters are registered through `internal/agent.RegisterAdapter`
and may publish credential-free metadata with
`RegisterAdapterWithDescriptor`. A descriptor identifies the adapter, version,
managed/external boundary and supported capabilities. Factories receive the
tenant-independent static configuration; runtime authorization and secret
resolution remain explicit control-plane responsibilities.

Built-in adapters are discoverable with `agent.RegisteredAdapters()` and
`agent.RegisteredAdapterDescriptors()`. External integrations should expose
the same metadata through their MCP/Bridge registration rather than embedding
provider credentials in a descriptor.

An adapter must implement the `Harness` contract, including health, session
lifecycle, invocation and bounded close behavior. Managed adapters are always
subject to command/path/network sandbox policy. External participants must be
registered as external and cannot be invoked as local subprocesses.
