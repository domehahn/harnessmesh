#!/usr/bin/env sh
set -eu

python3 - <<'PY'
import json
from pathlib import Path

path = Path("docs/api/openapi.json")
data = json.loads(path.read_text())
assert data.get("openapi", "").startswith("3."), "OpenAPI document must use OpenAPI 3"
paths = data.get("paths", {})
required = {
    "/api/v1/dashboard/summary",
    "/api/v1/dashboard/optimization",
    "/api/v1/dashboard/agents",
    "/api/v1/dashboard/spaces",
    "/api/v1/dashboard/simulation",
}
missing = sorted(required - paths.keys())
if missing:
    raise SystemExit("missing required OpenAPI paths: " + ", ".join(missing))
for name, definition in paths.items():
    if not isinstance(definition, dict):
        raise SystemExit(f"invalid path definition: {name}")
    for method, operation in definition.items():
        if method.lower() in {"get", "post", "put", "patch", "delete"} and "responses" not in operation:
            raise SystemExit(f"operation has no responses: {method.upper()} {name}")
print(f"OPENAPI=PASS paths={len(paths)}")
PY
