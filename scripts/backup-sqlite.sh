#!/usr/bin/env sh
set -eu
umask 077

db_path="${HARNESSMESH_DB_PATH:-/data/harnessmesh.db}"
knowledge_path="${HARNESSMESH_KNOWLEDGE_PATH:-/data/knowledge.hmkz}"
backup_dir="${HARNESSMESH_BACKUP_DIR:-/backup}"

if [ ! -f "$db_path" ]; then
  echo "database not found: $db_path" >&2
  exit 1
fi

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
target="$backup_dir/harnessmesh-$timestamp"
staging="$backup_dir/.harnessmesh-$timestamp.tmp"
mkdir -p "$staging"
trap 'rm -rf "$staging"' EXIT

# sqlite3 .backup is safe while the service is live and includes a consistent
# database snapshot without copying an in-flight WAL file directly.
sqlite3 "$db_path" ".timeout 10000" ".backup '$staging/harnessmesh.db'"

if [ -f "$knowledge_path" ]; then
  cp "$knowledge_path" "$staging/knowledge.hmkz"
fi

sqlite3 "$staging/harnessmesh.db" 'PRAGMA integrity_check;' | grep -qx ok

cat > "$staging/manifest.txt" <<EOF
created_at=$timestamp
database=$db_path
knowledge=$knowledge_path
EOF

if [ -f "$staging/knowledge.hmkz" ]; then
  (cd "$staging" && sha256sum harnessmesh.db knowledge.hmkz) > "$staging/SHA256SUMS"
else
  (cd "$staging" && sha256sum harnessmesh.db) > "$staging/SHA256SUMS"
fi
mv "$staging" "$target"
trap - EXIT

echo "Backup created: $target"
