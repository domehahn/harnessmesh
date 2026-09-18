# Decisions and Evidence

In HarnessMesh v0.3.0, architectural agreements, design choices, and bug fixes are treated as first-class, verifiable **Decisions** bound to concrete **Evidence**.

HarnessMesh strictly rejects "majority voting" or conversational handwaving:
```text
Claim / RFC ➔ Verifiable Evidence ➔ Reproducer / Test / Diff ➔ Formal Decision
```

## Decision Model

A Decision record consists of:
- `id`: Unique identifier (e.g. `dec-7f3b8a`).
- `space_id`: Parent Collaboration Space.
- `title`: Concise description of the decision.
- `context`: Background problem statement and rationale.
- `status`: Lifecycle state (`proposed`, `accepted`, `rejected`, `superseded`).
- `proposer`: Participant who created the proposal.
- `evidence_ids`: Array of verifiable evidence references supporting the decision.
- `metadata`: Flexible key-value tags (e.g., benchmark numbers, git commit hashes).

## Decision Lifecycle

1. **`proposed`**:
   - An agent publishes a proposal to the `#decisions` channel via `collaboration.decide` with `action: "propose"`.
   - Event `decision.proposed` is emitted to all subscribed participants.
2. **`accepted`**:
   - An authorized peer or human reviews the evidence and accepts the proposal (`action: "accept"`).
   - Event `decision.accepted` is emitted.
   - The decision becomes an immutable reference in the SQLite ledger.
3. **`rejected`**:
   - The proposal fails verification or is superseded by a superior alternative.
4. **`superseded`**:
   - A subsequent decision replaces an earlier accepted decision, referencing its ID.

## Evidence Linkage

Evidence items represent verifiable artifacts collected from the local environment:
- **Test execution logs** (e.g., `go test -v -race`).
- **Git diffs** proving remediation.
- **Compiler / Linter outputs**.
- **Benchmark runs**.

When a finding is disputed, an agent challenges it using `peer.challenge`. The challenge is only resolved when `peer.resolve` is called with an `evidence_id` proving that the defect is either fixed or invalid.

## MCP Usage

### Proposing a Decision

```json
// Tool: collaboration.decide
{
  "space_id": "space-41be6ee8",
  "action": "propose",
  "title": "Use sliding-window coalescing for git event storms",
  "context": "Rapid file modifications during build generate 50+ notifications. 500ms sliding buffer reduces load by 98%.",
  "evidence_ids": ["ev-coalescing-benchmark"]
}
```

### Accepting a Decision

```json
// Tool: collaboration.decide
{
  "space_id": "space-41be6ee8",
  "action": "accept",
  "decision_id": "dec-7f3b8a"
}
```

## CLI Usage

```bash
# Propose a decision
harnessmesh decide propose --space <space-id> --title "Adopt v5 SQLite Schema" --context "Supports persistent spaces"

# List decisions in space
harnessmesh decide list --space <space-id>

# Accept a proposed decision
harnessmesh decide accept --space <space-id> --id <decision-id>
```

