# Orchestration SDKs

HarnessMesh ships dependency-light clients for the tenant dashboard API:

- Go: `sdk/go`
- TypeScript: `sdk/typescript`
- Python: `sdk/python`

All clients require the tenant API key and use the same API base URL:

```text
https://harnessmesh.example/api/v1/dashboard
```

The clients expose tenant-scoped reads for summary, sessions, jobs, approvals,
skills, marketplace data and observability. They also expose the orchestration
operations needed by external agents:

- session detail and deterministic replay
- job detail
- job retry and cancel
- approval detail
- approval approve/reject

Example in Go:

```go
client := harnessmesh.New("http://127.0.0.1:3001", "hm_live_...")
var job map[string]any
if err := client.Job(ctx, "job-123", &job); err != nil { /* handle */ }
if err := client.JobAction(ctx, "job-123", "retry", &job); err != nil { /* handle */ }
```

The SDKs never expose or log secret values. API keys should be loaded from the
calling agent's secret manager or environment, not committed to source code.
All mutation endpoints remain subject to the tenant's identity, role and
approval policy.
