#!/usr/bin/env bash
# Restore a dump written by scripts/backup.sh into the running compose
# `postgres` container, replacing the current contents of the database.
#
# Usage:   scripts/restore.sh <dump.sql.gz | s3://bucket/key.sql.gz> [--yes]
#          (an s3:// source is downloaded with the same BACKUP_S3_ENDPOINT /
#          AWS_* settings backup.sh uses)
# Reads:   <repo>/.env   POSTGRES_USER / POSTGRES_DB (default datingcoach),
#                        BACKUP_S3_ENDPOINT, AWS_*
# Effect:  stops `api` and `worker` (so nothing writes mid-restore), replays
#          the dump in a single transaction (the dump carries DROP ... IF
#          EXISTS for every object, so existing data is replaced, including
#          the schema_migrations table), then starts api/worker again.
# Exit:    non-zero if the dump fails to apply; the transaction is rolled
#          back and the database is left as it was, api/worker are restarted.
#
# Rollback caveat: the `migrate` service only ever runs `up`. To roll the app
# back across a breaking migration, restore a dump taken before that
# migration FIRST, then redeploy the older image tag; otherwise the old code
# meets the new schema.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${ENV_FILE:-$REPO_DIR/.env}"

usage() { sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 2; }

src="${1:-}"
[ -n "$src" ] || usage
assume_yes=0
[ "${2:-}" = "--yes" ] && assume_yes=1

# load_env exports the POSTGRES_*, BACKUP_* and AWS_* lines of $ENV_FILE
# (surrounding quotes stripped); values already in the environment win.
load_env() {
    [ -f "$ENV_FILE" ] || return 0
    local line key value
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in
            POSTGRES_*=*|BACKUP_*=*|AWS_*=*) ;;
            *) continue ;;
        esac
        key="${line%%=*}"
        value="${line#*=}"
        value="${value%\"}"; value="${value#\"}"
        value="${value%\'}"; value="${value#\'}"
        [ -n "${!key+x}" ] || export "$key=$value"
    done < "$ENV_FILE"
}
load_env

POSTGRES_USER="${POSTGRES_USER:-datingcoach}"
POSTGRES_DB="${POSTGRES_DB:-datingcoach}"
BACKUP_S3_ENDPOINT="${BACKUP_S3_ENDPOINT:-}"

compose() {
    if [ -f "$ENV_FILE" ]; then
        docker compose --project-directory "$REPO_DIR" --env-file "$ENV_FILE" "$@"
    else
        docker compose --project-directory "$REPO_DIR" "$@"
    fi
}

tmp_dump=""
stopped=0
# cleanup removes a downloaded temp dump and restarts api/worker if this
# script stopped them; runs on every exit path.
cleanup() {
    [ -n "$tmp_dump" ] && rm -f "$tmp_dump"
    [ "$stopped" -eq 1 ] && compose start api worker
    return 0
}
trap cleanup EXIT

dump="$src"
case "$src" in
    s3://*)
        tmp_dump="$(mktemp --suffix=.sql.gz)"
        dump="$tmp_dump"
        endpoint_args=()
        [ -n "$BACKUP_S3_ENDPOINT" ] && endpoint_args=(--endpoint-url "$BACKUP_S3_ENDPOINT")
        aws "${endpoint_args[@]}" s3 cp --only-show-errors "$src" "$dump"
        ;;
esac
[ -f "$dump" ] || { echo "restore: no such dump: $dump" >&2; exit 1; }
gzip -t "$dump"

if [ "$assume_yes" -ne 1 ]; then
    printf 'This REPLACES database %s (user %s) in the running postgres container with %s.\nType "restore" to continue: ' "$POSTGRES_DB" "$POSTGRES_USER" "$src"
    read -r answer
    [ "$answer" = "restore" ] || { echo "aborted"; exit 1; }
fi

# Stop writers, restore, and (via cleanup) always bring them back.
compose stop api worker
stopped=1

# The dump's DROP ... IF EXISTS only covers objects that exist in the dump, so
# the public schema is recreated first to drop anything added since; all of
# it runs in one transaction with ON_ERROR_STOP, so a broken dump rolls back
# completely instead of leaving a half-replaced schema.
{ echo 'SET client_min_messages = warning; DROP SCHEMA public CASCADE; CREATE SCHEMA public;'; gunzip -c "$dump"; } \
    | compose exec -T postgres psql -q -v ON_ERROR_STOP=1 --single-transaction -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null
echo "restored $src into $POSTGRES_DB"
