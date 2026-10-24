# Target Runtime Qualification

The repository cannot prove kernel and container-runtime policy from unit
tests. Before a production executor is admitted, qualify the target runtime
with the exact image digest.

## Required checks

1. Build or pull the exact production image and record its digest.
2. Run `HARNESSMESH_ISOLATION_IMAGE=<digest> make runtime-isolation-check`.
3. Confirm the command succeeds as UID 10001 with all capabilities dropped and
   `no-new-privileges` enabled.
4. Run a disposable staging task whose managed agent attempts an outbound
   connection; the connection must fail while the control-plane readiness and
   approved backend paths remain available.
5. Record the kernel/runtime version, seccomp profile, namespace settings,
   image digest, timestamp, and command output in the release evidence.

## Failure policy

If the namespace check fails, or the outbound-connectivity probe succeeds, the
runtime is not production-qualified. Do not compensate by removing
`cap_drop`, disabling `no-new-privileges`, running the agent as root, or
silently changing `network_mode` to `host`. Qualify a Linux/container runtime
that supports the required isolation and rerun the full production gate.

In the current Colima environment, the hardened non-root check fails because
Bubblewrap cannot create the namespace; a privileged root-only diagnostic pass
does not count as production evidence. This distinction is intentional: the
production service must remain non-root and capability-dropped.
