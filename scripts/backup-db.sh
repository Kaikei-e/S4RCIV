#!/usr/bin/env bash
# Local periodic pg_dump of the S4RCIV database — an interim floor while a real
# offsite/PITR backup (pgBackRest + WAL archiving, shipped off this host) is
# still an open decision (where "offsite" is is an operator choice, not one this
# script can make). Until that exists, this turns "no backup at all" for the
# append-only ground truth into "a same-host point-in-time dump exists". It does
# NOT protect against losing this host/disk — copy BACKUP_DIR elsewhere too
# (rclone/rsync/etc.) once an offsite destination is chosen.
#
# Usage:
#   ./scripts/backup-db.sh                    # dump now into ./backups, keep last 14
#   BACKUP_DIR=/mnt/nas/s4rciv KEEP=30 ./scripts/backup-db.sh
#
# Automate with cron, e.g. nightly at 03:10:
#   10 3 * * * cd /path/to/S4rCiv && ./scripts/backup-db.sh >> backups/backup.log 2>&1
#
# Restore (always into a NEW, empty database — never over the live one):
#   docker compose exec -T db psql -U "$POSTGRES_USER" -c 'CREATE DATABASE restore_check'
#   gunzip -c backups/s4rciv_<timestamp>.sql.gz | \
#     docker compose exec -T db psql -U "$POSTGRES_USER" -d restore_check -v ON_ERROR_STOP=1
set -euo pipefail
cd "$(dirname "$0")/.."

# compose の .env はシェル構文ではない（クォート無しの括弧等を含む）ので
# source せず、必要な 2 変数だけ抽出する。
if [ -f .env ]; then
  POSTGRES_USER="${POSTGRES_USER:-$(grep -E '^POSTGRES_USER=' .env | head -1 | cut -d= -f2-)}"
  POSTGRES_DB="${POSTGRES_DB:-$(grep -E '^POSTGRES_DB=' .env | head -1 | cut -d= -f2-)}"
fi
POSTGRES_USER="${POSTGRES_USER:-s4rciv}"
POSTGRES_DB="${POSTGRES_DB:-s4rciv}"

BACKUP_DIR="${BACKUP_DIR:-backups}"
KEEP="${KEEP:-14}"
mkdir -p "$BACKUP_DIR"

TS="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="$BACKUP_DIR/${POSTGRES_DB}_${TS}.sql.gz"

# `docker compose exec` runs pg_dump inside the db container, connecting over
# the local unix socket (trusted there, same as scripts/set-api-ro-password.sh's
# psql exec) — no password needed and nothing sensitive crosses the host shell.
# --format=plain (not -Fc) so restore needs only psql, never pg_restore.
docker compose exec -T db \
  pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=plain | gzip > "$OUT"

echo "wrote $OUT ($(du -h "$OUT" | cut -f1))"

# Prune: keep only the most recent KEEP dumps in BACKUP_DIR.
mapfile -t old < <(ls -1t "$BACKUP_DIR/${POSTGRES_DB}"_*.sql.gz 2>/dev/null | tail -n +"$((KEEP + 1))")
if [ "${#old[@]}" -gt 0 ]; then
  rm -f -- "${old[@]}"
fi
