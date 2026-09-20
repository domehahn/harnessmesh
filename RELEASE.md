# Release process

HarnessMesh uses Semantic Versioning. Public protocol, MCP, configuration, and storage compatibility changes must be reflected in the version and changelog.

## Before a release

```bash
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
go build ./cmd/harnessmesh
docker build .
```

Update `VERSION` and `CHANGELOG.md`, verify examples and migration behavior, and review the generated release notes. The release workflow also creates checksums, an SBOM, and a Cosign signature.

## Tagging

Tags must be annotated and match `VERSION`:

```bash
version="$(tr -d '[:space:]' < VERSION)"
git tag -a "v${version}" -m "Release v${version}"
git push origin "v${version}"
```

Only maintainers should create release tags. Never include databases, knowledge archives, credentials, transcripts, or local configuration in a release commit.
