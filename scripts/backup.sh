#!/usr/bin/env bash
# Dump the compose `postgres` database, gzip it into BACKUP_DIR, prune old
# dumps and (optionally) upload the new one to an S3-compatible bucket.
#
# Usage:   scripts/backup.sh            (run from anywhere; cron-friendly)
# Reads:   <repo>/.env   POSTGRES_USER / POSTGRES_DB (default datingcoach)
#                        BACKUP_DIR (default <repo>/backups)
#                        BACKUP_RETENTION_DAYS (default 14; local dumps older than that are deleted)
#                        BACKUP_S3_URI (e.g. s3://my-bucket/dating-coach; empty = local only)
#                        BACKUP_S3_ENDPOINT (optional; Backblaze/R2/MinIO endpoint URL)
#                        AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_DEFAULT_REGION
# Output:  $BACKUP_DIR/<db>-YYYYmmddTHHMMSSZ.sql.gz  (path printed on stdout);
#          a consistent snapshot as of the moment pg_dump starts, so the
#          recovery point is that timestamp (commits after it are not included).
# Exit:    non-zero if pg_dump, gzip or the upload fails; a failed upload
#          leaves the local file in place.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${ENV_FILE:-$REPO_DIR/.env}"

# load_env exports the POSTGRES_*, BACKUP_* and AWS_* lines of $ENV_FILE
# (surrounding quotes stripped). Values already set in the environment win,
# so a cron line can override any of them.
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
BACKUP_DIR="${BACKUP_DIR:-$REPO_DIR/backups}"
BACKUP_RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-14}"
BACKUP_S3_URI="${BACKUP_S3_URI:-}"
BACKUP_S3_ENDPOINT="${BACKUP_S3_ENDPOINT:-}"

# compose runs docker compose against the repo's compose project regardless of
# the caller's working directory.
compose() {
    if [ -f "$ENV_FILE" ]; then
        docker compose --project-directory "$REPO_DIR" --env-file "$ENV_FILE" "$@"
    else
        docker compose --project-directory "$REPO_DIR" "$@"
    fi
}

mkdir -p "$BACKUP_DIR"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
out="$BACKUP_DIR/${POSTGRES_DB}-${stamp}.sql.gz"
tmp="$out.part"
trap 'rm -f "$tmp"' EXIT

# --clean --if-exists makes the dump re-runnable into a non-empty database
# (restore.sh relies on it). No password: the official image trusts local
# socket connections inside the container.
compose exec -T postgres pg_dump --clean --if-exists -U "$POSTGRES_USER" "$POSTGRES_DB" | gzip -9 > "$tmp"
mv "$tmp" "$out"
echo "$out"

# -mtime +N matches files older than N+1 whole days, so N-1 keeps exactly the
# last BACKUP_RETENTION_DAYS daily dumps.
find "$BACKUP_DIR" -maxdepth 1 -name "${POSTGRES_DB}-*.sql.gz" -type f -mtime +"$((BACKUP_RETENTION_DAYS - 1))" -print -delete

if [ -n "$BACKUP_S3_URI" ]; then
    endpoint_args=()
    [ -n "$BACKUP_S3_ENDPOINT" ] && endpoint_args=(--endpoint-url "$BACKUP_S3_ENDPOINT")
    aws "${endpoint_args[@]}" s3 cp --only-show-errors "$out" "${BACKUP_S3_URI%/}/$(basename "$out")"
fi
