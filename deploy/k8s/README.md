# Kubernetes / k3d / k3s

This directory contains a Kustomize baseline for a small production-like
installation. It uses one replica and a PVC-backed SQLite store so it works
on k3d/k3s without pretending that the current Engine is horizontally
scalable. It expects an externally managed HashiCorp Vault in production.

Create the runtime secret out of band (never commit it):

```sh
kubectl -n harnessmesh create secret generic harnessmesh-runtime \
  --from-literal=mcp-token="$(openssl rand -hex 32)" \
  --from-literal=admin-token="$(openssl rand -hex 32)" \
  --from-literal=vault-token="$VAULT_TOKEN" \
  --from-literal=postgres-password="$HARNESSMESH_POSTGRES_PASSWORD"
```

Configure `VAULT_ADDR` in `configmap.yaml` for the reachable Vault service,
initialize the KV-v2 mount, then run `make k8s-validate` and `make k8s-apply`.
For a hyperscaler use its managed PostgreSQL only after the Postgres-backed
Engine wiring is enabled, and use a Vault-compatible secret service or an
operator. The included manifests are a safe single-replica control-plane
baseline, not an HA database operator.
