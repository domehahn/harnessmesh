# GitHub Actions and Release Automation

HarnessMesh keeps pull-request checks deterministic and keeps deployment- or
provider-dependent checks explicit.

| Workflow | Trigger | Purpose |
| --- | --- | --- |
| `CI` | PR, `main`, merge queue, nightly | Go race/vet/fuzz, production gate, Compose, restore, Docker and frontend checks |
| `CodeQL` | PR, `main`, merge queue, weekly | Go static security analysis |
| `Dependency Review` | PR | Blocks newly introduced high-severity dependency findings |
| `Container Security` | PR, `main`, weekly | Builds and scans MCP/console images with Trivy and uploads SARIF |
| `License Compliance` | PR, `main`, weekly | Checks Go and production npm dependency licenses |
| `Documentation Links` | PR, `main`, weekly | Checks Markdown and documentation links |
| `Performance Benchmarks` | PR, weekly | Compares the knowledge benchmark against the base branch and fails above 20% regression |
| `PR Risk Labels` | PR | Labels large, security-sensitive, protocol and dependency changes |
| `Dependabot Auto Merge` | Dependabot PR | Enables squash auto-merge only for patch updates; required checks still gate the merge |
| `Release` | `v*.*.*` tag | CLI, SBOM, checksum, Cosign, provenance and GHCR images |
| `Release Install Matrix` | `v*.*.*` tag | Native `go install` smoke test on Linux, macOS and Windows |
| `Staging Qualification` | Manual | Protected-environment runtime, restore, load and simulation qualification |

The `CI` and `CodeQL` workflows include the `merge_group` event so required
checks are reported when a pull request is processed by GitHub's merge queue.
The merge-queue rule itself must be enabled in the repository's branch/ruleset
settings; GitHub currently does not expose that rule through the repository
Rules REST/GraphQL update APIs.

## Required repository settings

- Keep `main-quality-gates` requiring `test` and `analyze`.
- Keep the `main-review-policy` bypass for `domehahn` if maintainer merges are
  intended to require no review while still requiring CI.
- Create a protected GitHub Environment named `staging` and add the
  `HARNESSMESH_STAGING_BRIDGE_TOKEN` secret before running qualification.
- Enable the merge queue for `main` in Settings → Rules → `main-quality-gates`.
