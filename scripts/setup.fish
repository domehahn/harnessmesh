#!/usr/bin/env fish
set -l root (realpath (dirname (status filename))/..)

if not test -f "$root/harnessmesh.json"
    cp "$root/configs/harnessmesh.example.json" "$root/harnessmesh.json"
    echo "Created $root/harnessmesh.json"
else
    echo "$root/harnessmesh.json already exists"
end

mkdir -p "$root/bin"
go build -o "$root/bin/harnessmesh" "$root/cmd/harnessmesh"
or exit 1

echo
echo "Built: $root/bin/harnessmesh"
echo "Next:"
echo "  $root/bin/harnessmesh doctor --config $root/harnessmesh.json"
