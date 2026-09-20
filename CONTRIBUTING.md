# Contributing to HarnessMesh

Thanks for helping improve HarnessMesh. Contributions are welcome through issues, documentation changes, tests, adapters, protocol proposals, and pull requests.

Before starting substantial work, open an issue or discussion so the design and scope can be agreed on. Small fixes, tests, and documentation improvements can go directly into a pull request.

## Development

```bash
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/harnessmesh
git diff --check
```

When changing SQLite migrations, run the tests against a fresh database and an existing database created by the previous release. Changes to adapters should include a fake-harness test and document authentication, quota, timeout, and failure behavior.

## Design principles

Changes should preserve these boundaries:

1. HarnessMesh coordinates harnesses; it does not reimplement their tool loops.
2. Model routing belongs in a routing layer such as Switchyard.
3. Peer claims should carry evidence.
4. Autonomous loops require explicit budgets and stop conditions.
5. Multi-writer support requires isolation before concurrency.
6. Secrets and full transcripts should not appear in normal logs/reports.

## Pull requests

Please include:

- problem statement,
- protocol/config compatibility impact,
- tests,
- security implications,
- behavior when one peer fails.

Keep pull requests focused and use an imperative title. Pull request titles follow the conventional format:

`type(scope): short description`

Examples: `feat(mcp): add approval status tool`, `fix(store): preserve retry priority`, `docs: clarify remote setup`.

Use one of the repository labels defined in `.github/labels.yml`: `bug`, `enhancement`, `documentation`, `security`, `performance`, `adapter`, `protocol`, `good first issue`, or `help wanted`.

Maintainers can synchronize the labels after authenticating GitHub CLI with `gh auth login`:

```bash
./scripts/sync-github-labels.sh
```

## Commit and release conventions

Commits should be small enough to review and should not contain credentials, local databases, compressed archives, transcripts, build artifacts, or generated secrets. Releases use semantic version tags (`vMAJOR.MINOR.PATCH`); see [RELEASE.md](RELEASE.md).

## Design review

Protocol, storage, security, and autonomous-execution changes require explicit tests and documentation. Prefer backwards-compatible additions, typed errors, bounded resource usage, and durable recovery over implicit retries or silent data loss.
