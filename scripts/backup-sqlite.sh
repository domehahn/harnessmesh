#!/usr/bin/env sh
set -eu

db_path="${HARNESSMESH_DB_PATH:-/data/harnessmesh.db}"
knowledge_path="${HARNESSMESH_KNOWLEDGE_PATH:-/data/knowledge.hmkz}"
backup_dir="${HARNESSMESH_BACKUP_DIR:-/backup}"

if [ ! -f "$db_path" ]; then
  echo "database not found: $db_path" >&2
  exit 1
fi

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
target="$backup_dir/harnessmesh-$timestamp"
mkdir -p "$target"

# sqlite3 .backup is safe while the service is live and includes a consistent
# database snapshot without copying an in-flight WAL file directly.
sqlite3 "$db_path" ".timeout 10000" ".backup '$target/harnessmesh.db'"

if [ -f "$knowledge_path" ]; then
  cp "$knowledge_path" "$target/knowledge.hmkz"
fi

cat > "$target/manifest.txt" <<EOF
created_at=$timestamp
database=$db_path
knowledge=$knowledge_path
EOF

echo "Backup created: $target"
