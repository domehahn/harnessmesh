#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOURCE_DIR="${ROOT}/docs/wiki"
WIKI_REMOTE="${WIKI_REMOTE:-git@github.com:domehahn/harnessmesh.wiki.git}"

if [[ ! -d "${SOURCE_DIR}" ]]; then
  echo "Wiki source directory not found: ${SOURCE_DIR}" >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

echo "Publishing ${SOURCE_DIR} -> ${WIKI_REMOTE}"

if ! git clone "${WIKI_REMOTE}" "${TMP_DIR}/wiki"; then
  cat >&2 <<'EOF'
Unable to clone the GitHub Wiki repository.

If the repository Wiki has never had a page, initialize it once in GitHub:
  Repository -> Wiki -> Create the first page

Then re-run this script.
EOF
  exit 1
fi

find "${TMP_DIR}/wiki" -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +
cp -R "${SOURCE_DIR}/." "${TMP_DIR}/wiki/"

cd "${TMP_DIR}/wiki"
git add --all

if git diff --cached --quiet; then
  echo "Wiki is already up to date."
  exit 0
fi

git commit -m "docs: publish HarnessMesh wiki"
git push origin HEAD

echo "Wiki published successfully."
