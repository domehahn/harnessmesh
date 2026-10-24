# HarnessMesh Helm chart

The chart deploys MCP and durable workers and expects an existing Kubernetes
Secret. It never renders secret values from `values.yaml`.

```sh
helm lint deploy/helm/harnessmesh
helm template harnessmesh deploy/helm/harnessmesh \
  --set existingSecret=harnessmesh-runtime
helm upgrade --install harnessmesh deploy/helm/harnessmesh \
  --namespace harnessmesh --create-namespace
```

The referenced Secret must contain `mcp-token`, `admin-token`, `vault-token`
and `postgres-dsn` (or configure different keys in values). Use a managed
PostgreSQL service and managed Vault for production.
