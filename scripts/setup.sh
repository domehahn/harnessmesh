#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

if [ ! -f "$ROOT/harnessmesh.json" ]; then
  cp "$ROOT/configs/harnessmesh.example.json" "$ROOT/harnessmesh.json"
  echo "Created $ROOT/harnessmesh.json"
else
  echo "$ROOT/harnessmesh.json already exists"
fi

mkdir -p "$ROOT/bin"
go build -o "$ROOT/bin/harnessmesh" "$ROOT/cmd/harnessmesh"

echo
echo "Built: $ROOT/bin/harnessmesh"
echo "Next:"
echo "  $ROOT/bin/harnessmesh doctor --config $ROOT/harnessmesh.json"
