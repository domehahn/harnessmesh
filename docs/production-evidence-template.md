# Production Evidence Record

Copy this file into the release evidence store as
`<release>-<environment>-<date>.md`. Do not put credentials, tokens, private
keys, customer data, or raw provider responses into the record. Store only
fingerprints, references, redacted output, and measured results.

## Release identity

- Release/tag:
- Commit:
- Environment:
- Operator:
- Date/time (UTC):
- HarnessMesh image digest:
- Kubernetes/Compose manifest revision:

## Deterministic repository gate

- Command: `make production-readiness`
- Result: `PRODUCTION_READY=PASS`
- CI run/artifact URL:
- Retention expiry:

## Runtime isolation

- Host/kernel:
- Container runtime/version:
- Seccomp profile:
- Isolation image digest:
- Command: `HARNESSMESH_ISOLATION_IMAGE=<digest> make runtime-isolation-check`
- Result:
- Outbound-connectivity probe result:
- Control-plane/backend connectivity result:

## Identity and secret rotation

- Identity provider and realm:
- Client/config revision:
- Tenant/role mapping result:
- MFA policy result:
- Rotation timestamp:
- Old credential rejected: yes/no
- New credential accepted: yes/no
- Revocation without restart verified: yes/no
- Vault/KMS version or audit-event reference:

## Recovery and database

- Database/provider:
- Backup identifier/checksum reference:
- Dataset size/class (no customer data):
- Restore command/reference:
- RPO observed:
- RTO observed:
- Integrity/readiness checks:
- PITR/failover result:

## Load, chaos, and alerts

- Load scenario/report reference:
- Throughput:
- p50/p95/p99 latency:
- Error rate:
- Queue/dead-letter result:
- Chaos scenarios passed/deferred:
- Alert delivery test reference:
- Incident exercise reference:

## API/client lifecycle

- Client fixture version:
- Compatibility result:
- Migration result:
- Rollback version and result:

## Approval

- Findings:
- Exceptions and expiry:
- On-call owner:
- Release decision: approve / reject
- Reviewer:
- Review timestamp:
