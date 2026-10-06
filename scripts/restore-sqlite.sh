#!/usr/bin/env sh
set -eu

if [ "$#" -ne 2 ] || [ "$2" != "--confirm" ]; then
  echo "usage: harnessmesh-restore /backup/harnessmesh-YYYYmmddTHHMMSSZ --confirm" >&2
  exit 2
fi

source_dir="$1"
data_dir="${HARNESSMESH_DATA_DIR:-/data}"
source_db="$source_dir/harnessmesh.db"
source_knowledge="$source_dir/knowledge.hmkz"
target_db="$data_dir/harnessmesh.db"
target_knowledge="$data_dir/knowledge.hmkz"

if [ ! -f "$source_db" ]; then
  echo "backup database not found: $source_db" >&2
  exit 1
fi

if [ "${HARNESSMESH_RESTORE_SERVICES_STOPPED:-0}" != "1" ]; then
  echo "set HARNESSMESH_RESTORE_SERVICES_STOPPED=1 after stopping MCP/Bridge/Provider" >&2
  exit 1
fi

integrity="$(sqlite3 "$source_db" 'PRAGMA integrity_check;')"
if [ "$integrity" != "ok" ]; then
  echo "backup database failed integrity check: $integrity" >&2
  exit 1
fi

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
if [ -f "$target_db" ]; then
  cp "$target_db" "$data_dir/harnessmesh.db.pre-restore-$timestamp"
fi
if [ -f "$target_knowledge" ]; then
  cp "$target_knowledge" "$data_dir/knowledge.hmkz.pre-restore-$timestamp"
fi

cp "$source_db" "$target_db"
rm -f "$target_db-wal" "$target_db-shm"
if [ -f "$source_knowledge" ]; then
  cp "$source_knowledge" "$target_knowledge"
fi

echo "Restore completed from: $source_dir"
echo "Pre-restore copies are stored in: $data_dir"
