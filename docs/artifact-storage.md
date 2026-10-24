# Artifact storage boundary

Large evidence, reports and generated artifacts must not be placed into
PostgreSQL rows or tenant-visible logs. `internal/artifacts.Store` defines the
boundary for filesystem, S3 or R2 implementations.

`artifacts.LocalStore` is the safe single-node backend: it enforces tenant
directories, rejects traversal, writes through a private temporary file and
atomic rename, and exposes checksum verification through `artifacts.Verify`.

For multi-instance production, provide an S3-compatible implementation with
server-side encryption, tenant-prefixed keys, retention policies and access
logging. The application must not silently fall back to local disk.
