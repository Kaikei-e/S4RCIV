#!/usr/bin/env bash
# Enables login for the api_ro role (migration 20260610000018) using the
# password in secrets/api_db_password.txt. Run once after the migration has
# been applied, and again whenever the password is rotated:
#
#   ./scripts/set-api-ro-password.sh
#
# To (re)generate the secret first:
#   openssl rand -base64 32 > secrets/api_db_password.txt
#   docker run --rm -v "$PWD/secrets:/s" postgres:18.6-trixie \
#     sh -c 'chown 65532:65532 /s/api_db_password.txt && chmod 600 /s/api_db_password.txt'
#
# The SQL travels over psql stdin (not argv), so the password never appears in
# `ps` output or shell history on either side.
set -euo pipefail
cd "$(dirname "$0")/.."

# compose の .env はシェル構文ではない（クォート無しの括弧等を含む）ので
# source せず、必要な 2 変数だけ抽出する。
if [ -f .env ]; then
  POSTGRES_USER="${POSTGRES_USER:-$(grep -E '^POSTGRES_USER=' .env | head -1 | cut -d= -f2-)}"
  POSTGRES_DB="${POSTGRES_DB:-$(grep -E '^POSTGRES_DB=' .env | head -1 | cut -d= -f2-)}"
fi

SECRET_FILE="secrets/api_db_password.txt"
# The secret is owned by uid 65532 (the api container user) with mode 600, so
# read it through a root-in-namespace container instead of the host user.
PW="$(docker run --rm -v "$PWD/secrets:/s:ro" \
  postgres:18.6-trixie@sha256:06cad38a5d9f5d24b4d83d86def30795d5e4b757fedbf5281172b576dedcd941 \
  sh -c "tr -d '\n' < /s/$(basename "$SECRET_FILE")")"
if [ -z "$PW" ]; then
  echo "error: $SECRET_FILE is missing or empty" >&2
  exit 1
fi
# Escape single quotes for the SQL literal (base64 output has none, but be safe).
PW_ESCAPED="${PW//\'/\'\'}"

docker compose exec -T db \
  psql -U "${POSTGRES_USER:-s4rciv}" -d "${POSTGRES_DB:-s4rciv}" -v ON_ERROR_STOP=1 <<SQL
ALTER ROLE api_ro LOGIN PASSWORD '${PW_ESCAPED}';
SQL

echo "api_ro login enabled / password updated."
