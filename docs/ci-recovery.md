# CI recovery with MeshCommit

Run the reference scenario from the HarnessMesh source checkout:

```sh
go test ./internal/workflow -run '^TestCIRecoveryReference$' -count=1 -v
```

The scenario creates a temporary Git repository and detached worktree. It
records an actual failed Go check, applies a fixture patch, runs the same
check on the new tree and reopens the SQLite store with a new engine. The
new engine can read the failed and passing evidence and identify the remaining
review obligation. The gate opens after the test operator submits independent
review evidence. The scenario uses a scripted patch and operator identity
and requires only Go and Git.

## Apply the workflow to a GitHub Actions failure

Use a dedicated worktree for the failed revision and keep the MeshCommit
database outside it. Export the completed run metadata with the GitHub CLI:

```sh
gh run view RUN_ID --json headSha,url,status,jobs > /tmp/failed-run.json
```

Match the CI job's Go version, check command, build tags and relevant
environment. The built-in `unit_tests` proof runs `go test ./...`. For a
different check, submit evidence from the trusted runner for that check.

1. Run `harnessmesh change create --title "Repair failed CI check" --github-run /tmp/failed-run.json --github-job "test" --author executor --json`
   from the worktree. Omit `--github-job` when only one job failed.
   If the job checked out a different revision from the run head (for example,
   a pull-request merge ref), use that checkout's commit with `--base`.
   HarnessMesh verifies that worktree HEAD matches the selected checkout.
   Start with a clean worktree to reproduce the original failure. Record the
   change ID and the proof obligation IDs.
2. Use `harnessmesh change verify --obligation TEST_ID --json CHANGE_ID`
   on the failing checkout to capture the original result. Verify that the
   test actually failed for the expected reason.
3. Repair the issue in the same worktree. Run
   `harnessmesh change prepare CHANGE_ID` and then rerun the same verification
   command. Check the proof result and tree hash in the returned evidence.
4. For a handoff, share the worktree location, change ID and original CI URL
   with the reviewer. With the same database, run
   `harnessmesh change show CHANGE_ID --json` for the current tree, evidence
   and pending obligations. The reviewer checks the diff and the rerun before
   recording a decision.
5. A trusted local reviewer can record acceptance with
   `harnessmesh change evidence --obligation REVIEW_ID --type independent_review --result passed --source REVIEWER_ID --notes "Reviewed the diff and the CI rerun" CHANGE_ID`.
   Over MCP, the evidence uses the caller's authenticated participant
   identity. Check `harnessmesh change gate CHANGE_ID` before committing.
   GitHub branch protection and deployment approvals still apply.

Keep the same worktree path and database for each command. Set
`HARNESSMESH_DB_PATH` to an absolute SQLite path before the first command if
you need a state file dedicated to this recovery.

## Measure the value of an independent reviewer

Run the same verified tasks in both arms. Use one executor in the baseline
and an executor plus an independent reviewer in the candidate arm. Keep the
model, total budget, checkout, task input and outcome checks fixed. Store one
JSONL row per tenant and task in each arm. Assign unique task IDs to repeated
trials and use a fresh worktree for each arm.

Use verifier outcomes for `success`, policy logs for `policy_ok` and review
records for `human_approved`. Record end-to-end elapsed time as
`wall_time_ms`, total measured provider cost in millionths of a US dollar as
`cost_microusd`, and the count of independently confirmed findings as
`verified_findings`. Omit measurements you could not collect. For example:

```json
{"tenant":"pilot","task_id":"ci-calc-01","input":"Repair the failing calculator check","success":true,"policy_ok":true,"human_approved":true,"wall_time_ms":12000,"cost_microusd":4200,"verified_findings":0}
```

```sh
harnessmesh eval compare --baseline single-agent.jsonl --candidate executor-reviewer.jsonl
```

The report counts paired outcomes, scores both cohorts and reports a metric
mean only when the cohort has a measurement for every task. Collect multiple
tasks before using these results to change routing policy.

## Approve a plan before launching the writer

Keep the plan file and approval database in an operator-controlled directory
outside the worktree. The local operator launches the writer through HarnessMesh
and supplies the reviewer identity. Keep direct write access restricted to
that managed executor. The launch gate checks approval at the CLI boundary;
write access granted outside this boundary is governed by the host.

Create a change before applying the repair. Prepare a JSON specification:

```json
{
  "title": "Repair calculator CI check",
  "intent": "Restore the expected addition behavior",
  "non_goals": ["Change the public calculator API"],
  "scope": ["calc.go"],
  "max_changed_files": 1,
  "max_changed_lines": 5,
  "never_cut": ["calc_test.go"],
  "verification_commands": ["go test ./..."],
  "decision_references": [{"path": "docs/decisions/0001-calculator.md", "revision": "HEAD"}],
  "acceptance_criteria": ["The addition regression test passes on the repaired tree."],
  "acceptance_proofs": {"The addition regression test passes on the repaired tree.": ["unit_tests"]}
}
```

Use a decision file that exists at the stated revision. HarnessMesh resolves
each reference to its Git commit and includes it in the displayed plan.

```sh
harnessmesh change plan show --change CHANGE_ID --spec /path/to/plan.json
harnessmesh change plan approve --change CHANGE_ID --spec /path/to/plan.json \
  --digest REVIEWED_DIGEST --reviewer REVIEWER_ID
harnessmesh change plan run --change CHANGE_ID --spec /path/to/plan.json \
  -- codex exec 'Implement the approved plan supplied on stdin.'
harnessmesh change handoff --change CHANGE_ID --plan-digest REVIEWED_DIGEST
```

An updated plan, a moved ADR revision or a different worktree tree hash needs
approval for its own digest. The launcher passes the approved snapshot on
stdin to the executable selected by the operator. Its verification commands
and acceptance criteria remain visible to the reviewer. MeshCommit's existing
proof policy governs which checks may run and which proofs are required.

The handoff includes the current fingerprint, open obligation IDs, last recorded
gate and evidence receipts showing source, adapter, command, tree and artifact
hash when available. Check the gate's tree hash against the live fingerprint
when the worktree has changed since the last gate evaluation.

## Compare code-impact hints

Use a known change and list the relevant source and test files in an expected
JSON array. The pilot compares the existing textual references with exact Go
identifiers and tests in affected package directories. Supply another
analyzer's paths, such as a local GitNexus result, as a JSON array of
repository-relative paths to score all three against the same expected files:

```sh
harnessmesh code impact-pilot --repo . --query Add --expected /path/to/expected.json \
  --external /path/to/gitnexus-files.json
```

Review the matches, misses and extra paths. Exact identifiers can coincide
across scopes. Use the result to prioritize review and run the required
verification suite before closing the change.
