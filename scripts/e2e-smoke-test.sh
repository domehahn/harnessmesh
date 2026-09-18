#!/usr/bin/env sh
set -eu

echo "Running HarnessMesh v0.2.0 E2E Real Harness Smoke Test..."

if ! command -v claude >/dev/null 2>&1; then
    echo "claude binary not found in PATH; skipping real harness smoke test"
    exit 0
fi

if ! command -v codex >/dev/null 2>&1; then
    echo "codex binary not found in PATH; skipping real harness smoke test"
    exit 0
fi

export HARNESSMESH_E2E=1
go test -v -run TestRealHarnessSmoke ./internal/workflow/...
echo "HarnessMesh E2E smoke test: PASS"

