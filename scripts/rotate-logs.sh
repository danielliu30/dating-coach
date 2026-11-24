#!/usr/bin/env bash
# Rotate and delete the persisted service logs in the app-logs volume (tee'd
# there by the compose api/worker/ml-analyzer commands). Rotation is
# copy-truncate: the running tee holds an O_APPEND fd, so truncating the live
# file is safe — the next line appends at offset 0. Each service keeps at most
# LOG_KEEP rotated generations; rotated files older than LOG_RETENTION_DAYS
# are deleted outright.
#
# Usage:   scripts/rotate-logs.sh     (run from anywhere; cron-friendly,
#          e.g. daily next to scripts/backup.sh)
# Reads:   <repo>/.env   APP_LOGS_VOLUME (default dating-coach_app-logs —
#                        <compose project>_app-logs if the project was renamed)
#          LOG_MAX_BYTES (default 10485760 = 10 MiB) — rotate a log when larger
#                        LOG_KEEP (default 3) — rotated generations kept per service
#                        LOG_RETENTION_DAYS (default 7) — rotated files older
#                        than that are deleted
# Exit:    non-zero if docker or the rotation fails.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${ENV_FILE:-$REPO_DIR/.env}"

# load_env exports the LOG_* and APP_LOGS_VOLUME lines of $ENV_FILE
# (surrounding quotes stripped). Env values already set win.
load_env() {
    [ -f "$ENV_FILE" ] || return 0
    local line key value
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in
            LOG_*=*|APP_LOGS_VOLUME=*) ;;
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

APP_LOGS_VOLUME="${APP_LOGS_VOLUME:-dating-coach_app-logs}"
LOG_MAX_BYTES="${LOG_MAX_BYTES:-10485760}"
LOG_KEEP="${LOG_KEEP:-3}"
LOG_RETENTION_DAYS="${LOG_RETENTION_DAYS:-7}"
for v in LOG_MAX_BYTES LOG_KEEP LOG_RETENTION_DAYS; do
    [[ "${!v}" =~ ^[1-9][0-9]*$ ]] \
        || { echo "rotate-logs: $v must be a positive integer (got '${!v}')" >&2; exit 2; }
done

docker run --rm \
    -e MAX_BYTES="$LOG_MAX_BYTES" \
    -e KEEP="$LOG_KEEP" \
    -e RETENTION_DAYS="$LOG_RETENTION_DAYS" \
    -v "$APP_LOGS_VOLUME":/logs \
    alpine sh -c '
set -e
for f in /logs/*.log /logs/*.jsonl; do
    [ -f "$f" ] || continue
    [ "$(stat -c %s "$f")" -gt "$MAX_BYTES" ] || continue
    i=$KEEP
    while [ "$i" -gt 1 ]; do
        j=$((i - 1))
        [ -f "$f.$j" ] && mv "$f.$j" "$f.$i"
        i=$j
    done
    cp "$f" "$f.1"
    : > "$f"
    echo "rotated $f"
done
# Delete anything beyond the kept generations (e.g. after lowering LOG_KEEP)
# or older than the retention window.
find /logs \( -name "*.log.*" -o -name "*.jsonl.*" \) -type f | while read -r old; do
    gen="${old##*.}"
    case "$gen" in (*[!0-9]*) continue;; esac
    if [ "$gen" -gt "$KEEP" ]; then
        rm -f "$old"; echo "dropped $old (beyond $KEEP generations)"
    fi
done
find /logs \( -name "*.log.*" -o -name "*.jsonl.*" \) -type f -mtime +"$RETENTION_DAYS" -print -delete
'
