# Contributing

Thanks for contributing to HarnessMesh.

## Development

```bash
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/harnessmesh
```

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
