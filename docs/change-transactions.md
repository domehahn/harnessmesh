# MeshChange Transactions & Lifecycle State Machine

## Transaction Lifecycle

Every code change in HarnessMesh is tracked as a first-class `MeshChange` transaction through distinct, deterministic states:

```
 [draft]
    |
    v
 [prepared] <-----------------+ (invalidation / retry)
    |                         |
    v                         |
 [under_verification] --------+
    |
    +-----> [blocked] (missing evidence or failed proof)
    |
    +-----> [verified] / [committable] (all required proofs satisfied)
               |
               v
           [committed] (terminal)
               
 [aborted] (terminal, can transition from any non-committed state)
```

### Lifecycle States

| State | Description | Permitted Next States |
| :--- | :--- | :--- |
| `draft` | Change proposed by designated writer, tree fingerprinted, initial proof policy resolved. | `prepared`, `aborted` |
| `prepared` | Working tree state frozen into `CurrentTreeHash`, diff computed, obligations locked. | `under_verification`, `aborted` |
| `under_verification` | Verification proofs are being collected, executed, or reviewed. | `blocked`, `committable`, `aborted` |
| `blocked` | One or more required proof obligations have failed, are missing evidence, or became stale. | `under_verification`, `aborted` |
| `committable` | All required proof obligations have valid, un-invalidated evidence with status `passed`. | `committed`, `blocked` (if modified), `aborted` |
| `committed` | Final Git commit created; locked in history and archived into Knowledge store. | (terminal) |
| `aborted` | Explicitly abandoned or cancelled by operator or agent. | (terminal) |

---

## MCP Tooling Interface

Agents interact with change transactions using the MCP `change.*` tools:

- `change.create`:
  ```json
  {
    "title": "Migrate authentication handler to token validation",
    "intent": "Enhance OAuth introspection security",
    "space_id": "space_prod",
    "author": "codex"
  }
  ```
- `change.prepare`:
  ```json
  {
    "change_id": "chg_1790064643526737000"
  }
  ```
- `change.status`:
  Inspect status, tree hashes, obligations, and gate results.
- `change.diff`:
  Inspect affected paths and before/after file hashes.
- `change.verify`:
  Trigger automated execution of a specific obligation.
- `change.evidence`:
  Submit structured verification evidence.
- `change.commit_status`:
  Query gate status and missing obligations.
- `change.abort`:
  Abort an in-flight change transaction.

---

## CLI Commands

```bash
# Propose a change
harnessmesh change create --title "Add user auth" --intent "Support bearer tokens"

# List active change transactions
harnessmesh change list

# Inspect change details and proof obligations
harnessmesh change show <change_id>

# Prepare change for verification
harnessmesh change prepare <change_id>

# Run automated verification proofs
harnessmesh change verify <change_id>

# Submit independent peer review evidence
harnessmesh change evidence <change_id> --obligation <obl_id> --type peer_review --result passed --source claude

# Evaluate change gate
harnessmesh change gate <change_id>

# Commit verified change
harnessmesh change commit <change_id> --message "Merge user auth"

# Abort change
harnessmesh change abort <change_id> --reason "Superseded by PR #42"
```

