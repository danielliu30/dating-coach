#!/usr/bin/env bash
# Rotate and age out the persisted service logs in the app-logs volume
# (tee'd there by the compose api/worker/ml-analyzer commands). Rotation is
# copy-truncate: the running tee holds an O_APPEND fd, so truncating the live
# file is safe — the next line appends at offset 0.
#
# Usage:   scripts/rotate-logs.sh     (run from anywhere; cron-friendly,
#          e.g. daily next to scripts/backup.sh)
# Reads:   APP_LOGS_VOLUME (default dating-coach_app-logs — <compose
#          project>_app-logs if the project was renamed)
#          LOG_MAX_BYTES (default 10485760 = 10 MiB) — rotate a log when larger
#          LOG_KEEP (default 3) — rotated generations kept per service
#          (.1 is newest; .LOG_KEEP is dropped on the next rotation)
# Exit:    non-zero if docker or the rotation fails.
set -euo pipefail

APP_LOGS_VOLUME="${APP_LOGS_VOLUME:-dating-coach_app-logs}"
LOG_MAX_BYTES="${LOG_MAX_BYTES:-10485760}"
LOG_KEEP="${LOG_KEEP:-3}"
[[ "$LOG_MAX_BYTES" =~ ^[1-9][0-9]*$ && "$LOG_KEEP" =~ ^[1-9][0-9]*$ ]] \
    || { echo "rotate-logs: LOG_MAX_BYTES and LOG_KEEP must be positive integers" >&2; exit 2; }

docker run --rm \
    -e MAX_BYTES="$LOG_MAX_BYTES" \
    -e KEEP="$LOG_KEEP" \
    -v "$APP_LOGS_VOLUME":/logs \
    alpine sh -c '
set -e
for f in /logs/*.log; do
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
# Anything older than the kept generations (e.g. after lowering LOG_KEEP).
find /logs -name "*.log.*" -type f | while read -r old; do
    gen="${old##*.}"
    case "$gen" in (*[!0-9]*) continue;; esac
    [ "$gen" -le "$KEEP" ] || rm -f "$old"
done
'
