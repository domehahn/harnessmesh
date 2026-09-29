# Proof Obligations & Policy Engine

## Overview

A `ProofObligation` represents an invariant that must be proven before a change transaction can be committed. Proof obligations are not opinions; they specify exact verification requirements, such as automated test executions, static code analysis, or independent peer reviews.

---

## Configuration Policy Rules

In `harnessmesh.json`:

```json
{
  "change_control": {
    "enabled": true,
    "default_proofs": ["unit-tests", "independent-review"],
    "rules": [
      {
        "paths": ["internal/auth/**", "pkg/crypto/**"],
        "require": ["security_review", "unit_tests"]
      },
      {
        "paths": ["api/v1/**", "internal/protocol/**"],
        "require": ["architecture_review"]
      },
      {
        "paths": ["cmd/**"],
        "require": ["build"]
      }
    ],
    "auto_commit": false
  }
}
```

### Supported Obligation Types

| Canonical Type | Aliases | Description | Default Command |
| :--- | :--- | :--- | :--- |
| `unit_tests` | `unit_test`, `unittest` | Unit test execution | `go test ./...` |
| `race_detector` | `race`, `race_detect` | Concurrency race detection | `go test -race ./...` |
| `build` | `compile` | Code compilation | `go build ./...` |
| `lint` | `vet`, `analysis` | Static code analysis | `go vet ./...` |
| `independent_review` | `peer_review`, `review` | Peer review by independent agent | (requires human or peer agent) |
| `security_review` | `security_audit`, `security`| Specialized security inspection | (requires security reviewer) |
| `architecture_review`| `architecture` | Architectural consistency check | (requires architecture reviewer) |

---

## Policy Downgrade Protection

When a change transaction is prepared, `ResolveProofPolicy` evaluates the affected paths against the configured rules and produces an immutable snapshot stored in `proof_policy_json` on the change transaction.

Once locked:
- Changes to `harnessmesh.json` do not affect in-flight changes.
- Agents cannot delete, skip, or alter required obligations.
- Weakening policies requires operator authorization.

