#!/usr/bin/env bash
# Restore a dump written by scripts/backup.sh into the running compose
# `postgres` container, replacing the current contents of the database.
#
# Usage:   scripts/restore.sh <dump.sql.gz | s3://bucket/key.sql.gz> [--yes] [--no-start]
#          (an s3:// source is downloaded with the same BACKUP_S3_ENDPOINT /
#          AWS_* settings backup.sh uses)
# Reads:   <repo>/.env   POSTGRES_USER / POSTGRES_DB (default datingcoach),
#                        BACKUP_S3_ENDPOINT, AWS_*
# Effect:  stops `api` and `worker` (so nothing writes mid-restore), replays
#          the dump in a single transaction (the dump carries DROP ... IF
#          EXISTS for every object, so existing data is replaced, including
#          the schema_migrations table), re-runs the `migrate` service so the
#          schema matches the currently checked-out release, then starts
#          api/worker again.
#          --no-start skips migrate and leaves api/worker stopped: use it when
#          rolling back across a breaking migration, then deploy the older
#          image tag (its migrate/api/worker come up with the old schema).
# Exit:    non-zero if the dump fails to apply (the transaction is rolled
#          back and the database is left as it was) or migrate fails; in the
#          latter case api/worker stay stopped so the current release never
#          runs against a schema it does not understand.
#
# Rollback caveat: the `migrate` service only ever runs `up`. To roll the app
# back across a breaking migration, restore a dump taken before that
# migration FIRST (with --no-start), then redeploy the older image tag;
# otherwise the old code meets the new schema.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${ENV_FILE:-$REPO_DIR/.env}"

usage() { sed -n '2,27p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 2; }

src=""
assume_yes=0
no_start=0
for arg in "$@"; do
    case "$arg" in
        --yes) assume_yes=1 ;;
        --no-start) no_start=1 ;;
        -*) usage ;;
        *) [ -z "$src" ] || usage; src="$arg" ;;
    esac
done
[ -n "$src" ] || usage

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
restart_services=0
# cleanup removes a downloaded temp dump and, when the restore did not change
# the schema contract (rolled back, or migrate ran clean), restarts
# api/worker; runs on every exit path.
cleanup() {
    [ -n "$tmp_dump" ] && rm -f "$tmp_dump"
    [ "$restart_services" -eq 1 ] && compose start api worker
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

# Stop writers; until the replay commits the schema is unchanged, so an abort
# can safely bring them back.
compose stop api worker
restart_services=1

# The dump's DROP ... IF EXISTS only covers objects that exist in the dump, so
# the public schema is recreated first to drop anything added since; all of
# it runs in one transaction with ON_ERROR_STOP, so a broken dump rolls back
# completely instead of leaving a half-replaced schema.
{ echo 'SET client_min_messages = warning; DROP SCHEMA public CASCADE; CREATE SCHEMA public;'; gunzip -c "$dump"; } \
    | compose exec -T postgres psql -q -v ON_ERROR_STOP=1 --single-transaction -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null
restart_services=0   # committed: the schema now belongs to the dump, not necessarily to the current release
echo "restored $src into $POSTGRES_DB"

if [ "$no_start" -eq 1 ]; then
    echo "api/worker left stopped (--no-start); deploy the matching release to bring them back"
    exit 0
fi

# The dump may predate migrations the checked-out release needs; bring the
# schema forward before the current api/worker touch it.
compose run --rm --no-deps migrate
restart_services=1
