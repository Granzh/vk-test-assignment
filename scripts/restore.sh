#!/bin/sh
# Restores a dump made by backup.sh into an EMPTY database.
#
#   PGHOST=... PGUSER=bot PGPASSWORD=... PGDATABASE=bot scripts/restore.sh backups/bot_20260905T030000Z.dump
#
# It refuses to touch a database that already has tables; create a fresh one
# (see docs/deploy.md) instead of overwriting live data.
set -eu

[ $# -eq 1 ] || { echo "usage: $0 <dump file>" >&2; exit 2; }
dump="$1"
[ -f "$dump" ] || { echo "no such file: $dump" >&2; exit 2; }

tables="$(psql -At -c "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'")"
if [ "$tables" != "0" ]; then
  echo "refusing to restore: database ${PGDATABASE:-} already has $tables tables" >&2
  exit 1
fi
pg_restore --no-owner --exit-on-error --dbname="${PGDATABASE:-bot}" "$dump"
echo "restored $dump"
