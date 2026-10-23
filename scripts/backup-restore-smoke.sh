#!/usr/bin/env sh
set -eu

root_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/harnessmesh-backup-smoke.XXXXXX")"
trap 'rm -rf "$test_dir"' EXIT INT TERM

db_path="$test_dir/source/harnessmesh.db"
knowledge_path="$test_dir/source/knowledge.hmkz"
backup_dir="$test_dir/backups"
restore_dir="$test_dir/restored"
rto_limit="${HARNESSMESH_RESTORE_RTO_SECONDS:-0}"
case "$rto_limit" in
	''|*[!0-9]*) echo "HARNESSMESH_RESTORE_RTO_SECONDS must be numeric" >&2; exit 2 ;;
esac
started_at=$(date +%s)

mkdir -p "$(dirname "$db_path")"
sqlite3 "$db_path" 'CREATE TABLE smoke(value TEXT); INSERT INTO smoke VALUES ("ok");'
printf 'knowledge-smoke\n' > "$knowledge_path"

HARNESSMESH_DB_PATH="$db_path" \
HARNESSMESH_KNOWLEDGE_PATH="$knowledge_path" \
HARNESSMESH_BACKUP_DIR="$backup_dir" \
  sh "$root_dir/scripts/backup-sqlite.sh" >/dev/null

backup_path="$(find "$backup_dir" -mindepth 1 -maxdepth 1 -type d -name 'harnessmesh-*' -print -quit)"
test -n "$backup_path"

HARNESSMESH_DATA_DIR="$restore_dir" \
HARNESSMESH_RESTORE_SERVICES_STOPPED=1 \
  sh "$root_dir/scripts/restore-sqlite.sh" "$backup_path" --confirm >/dev/null

test "$(sqlite3 "$restore_dir/harnessmesh.db" 'PRAGMA integrity_check;')" = ok
test "$(sqlite3 "$restore_dir/harnessmesh.db" 'SELECT value FROM smoke;')" = ok
test "$(cat "$restore_dir/knowledge.hmkz")" = "knowledge-smoke"
finished_at=$(date +%s)
rto_seconds=$((finished_at - started_at))
if [ "$rto_limit" -gt 0 ] && [ "$rto_seconds" -gt "$rto_limit" ]; then
	echo "BACKUP_RESTORE_SMOKE=FAIL rto_seconds=$rto_seconds limit_seconds=$rto_limit" >&2
	exit 1
fi
echo "BACKUP_RESTORE_SMOKE=PASS rto_seconds=$rto_seconds"
