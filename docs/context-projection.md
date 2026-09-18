# Context Projection

HarnessMesh avoids forwarding entire raw agent transcripts between harnesses. Instead, it creates bounded, repository-centric context projections tailored specifically to the peer's task.

## Projection Structure

A projected context snapshot includes:
1. **Repository Identity**: Root directory, current commit HEAD, and active branch.
2. **Repository Status**: Filtered output of `git status --short` (excluding secrets).
3. **Diff Stat & Bounded Diff**: Unstaged and staged diffs bounded by `max_diff_chars`.
4. **Scoped File Contents**: Explicitly requested files bounded by `max_file_chars` and `max_files`.
5. **Test Results**: Output and exit code of configured test commands bounded by `max_test_output_chars`.
6. **Active Findings & Evidence**: Relevant session findings and verifiable evidence payloads.

## Middle-Out Truncation

When diffs or test outputs exceed configured size limits, HarnessMesh performs deterministic middle-out truncation, preserving the start and end of the content while inserting a clear marker:

```text
...<HarnessMesh middle truncation>...
```

Projections always include metadata detailing whether truncation occurred:

```json
{
  "truncated": true,
  "original_chars": 120000,
  "included_chars": 40000
}
```

