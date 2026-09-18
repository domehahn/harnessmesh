# Human Supervision and Emergency Controls

HarnessMesh is built on the principle that **humans remain the ultimate authority** over multi-agent coding swarms. While agents operate autonomously within a Collaboration Space, human supervisors have real-time controls to observe, pause, modify, and terminate agent activity at any time.

## Human Control Plane

```text
┌─────────────────────────────────────────────────────────────┐
│                      Human Supervisor                       │
│    harnessmesh space pause / resume / stop / inspect        │
└──────────────────────────────┬──────────────────────────────┘
                               │
            ┌──────────────────┼──────────────────┐
            ▼                  ▼                  ▼
     ┌─────────────┐    ┌─────────────┐    ┌─────────────┐
     │ Space State │    │  Rate Limit │    │ Sensitive   │
     │ Controller  │    │  & Cooldown │    │ File Policy │
     └─────────────┘    └─────────────┘    └─────────────┘
```

## Space Lifecycle Interventions

### 1. Pausing a Space (`space pause`)
When a human supervisor notices unexpected behavior, a runaway thread, or wishes to manually inspect work in progress:
- The supervisor runs:
  ```bash
  harnessmesh space pause --space <space-id>
  ```
- The space state immediately transitions to `paused`.
- All agent publication attempts via `collaboration.publish` and `collaboration.reply` are rejected with `ParticipantPausedError`.
- No new event deliveries or background invocations are dispatched.

### 2. Resuming a Space (`space resume`)
After inspecting the code, resolving issues, or adjusting parameters:
- The supervisor runs:
  ```bash
  harnessmesh space resume --space <space-id>
  ```
- The space state transitions back to `active`.
- Agents can continue conversation from their existing thread cursors.

### 3. Emergency Stop (`space stop`)
To permanently halt all activity in a space:
- The supervisor runs:
  ```bash
  harnessmesh space stop --space <space-id>
  ```
- The space state transitions to `stopped`.
- This state is irreversible. Read operations remain permitted for auditing.

## Sensitive File Policy

To prevent unintended exfiltration of credentials or intellectual property:
- Any file path containing sensitive markers (such as `.env`, `*.pem`, `*.key`, `id_rsa`, `secrets/`, `credentials/`, `token`) is flagged by the `ActivationController`.
- Events touching sensitive paths are stripped or restricted from broadcast to external/cloud-based harnesses (e.g. Claude or Codex).
- Antigravity running locally on the developer machine can access local files, but context projections exported to external peers redact sensitive contents.

## Auditing and Inspection

Human supervisors can inspect all collaboration state directly from the terminal:

```bash
# View complete space activity
harnessmesh space show --space <space-id>

# View full causal conversation in any thread
harnessmesh thread show --space <space-id> --thread <thread-id>

# List all proposed and accepted decisions
harnessmesh decide list --space <space-id>

# Inspect all collected evidence artifacts
harnessmesh evidence <session-id>
```

