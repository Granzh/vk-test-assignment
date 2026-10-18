#!/bin/sh
# Runs backup.sh every BACKUP_INTERVAL_HOURS (default 24), forever. A failed
# backup is reported on stderr and retried on the next round.
set -u

interval=$(( ${BACKUP_INTERVAL_HOURS:-24} * 3600 ))
while true; do
  /scripts/backup.sh || echo "backup FAILED at $(date -u +%FT%TZ)" >&2
  sleep "$interval"
done
