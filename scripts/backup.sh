#!/bin/sh
# Dumps the bot database in PostgreSQL's custom format (restorable with
# pg_restore, compressed) and removes dumps older than KEEP_DAYS.
#
# Used by the "backup" service of docker-compose.prod.yml, or by hand:
#   PGHOST=localhost PGUSER=bot PGPASSWORD=... PGDATABASE=bot BACKUP_DIR=./backups scripts/backup.sh
set -eu

BACKUP_DIR="${BACKUP_DIR:-/backups}"
KEEP_DAYS="${KEEP_DAYS:-14}"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
file="$BACKUP_DIR/bot_${stamp}.dump"

mkdir -p "$BACKUP_DIR"
# Write to a temporary name first: a half-written dump must never look like a backup.
pg_dump --format=custom --no-owner --file="$file.partial"
mv "$file.partial" "$file"
echo "backup written: $file ($(du -h "$file" | cut -f1))"

find "$BACKUP_DIR" -name 'bot_*.dump' -type f -mtime +"$KEEP_DAYS" -print -delete
