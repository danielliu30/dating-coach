#!/bin/sh
# Metrics collector loop: appends JSONL snapshots to the app-logs volume (the
# metrics data store). The metering service reads and aggregates these files —
# this writer is the only component touching the docker socket.
#  - metrics-stats.jsonl    per-task docker stats (cpu/mem/net/pids/started)
#  - metrics-services.jsonl service replica counts
#  - metrics-requests.jsonl per-edge-bucket request counts aggregated from the
#    nginx access log (delta since the previous sample; tracked by byte offset
#    so rotation resets it rather than double-counting)
# Socket is mounted read-only; log rotation bounds the files.
set -u

STACK="${STACK_NAME:-dating-coach}"
OFF=/logs/.metrics-access.offset
while true; do
    ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    # started comes from inspect: docker stats has no start-time field.
    # docker stats has no --filter; list stack containers via docker ps.
    docker ps -q --filter "name=$STACK" \
        | xargs -r docker stats --no-stream --format '{{json .}}' \
        | while IFS= read -r line; do
            name=$(printf '%s' "$line" | sed -n 's/.*"Name":"\([^"]*\)".*/\1/p')
            started=$(docker inspect -f '{{.State.StartedAt}}' "$name" 2>/dev/null)
            printf '%s\n' "$line" | sed "s/^{/{\"ts\":\"$ts\",\"started\":\"$started\",/"
        done >> /logs/metrics-stats.jsonl
    docker service ls --format '{{json .}}' 2>/dev/null \
        | sed "s/^{/{\"ts\":\"$ts\",/" >> /logs/metrics-services.jsonl

    size=$(stat -c %s /logs/metrics-access.log 2>/dev/null || echo 0)
    prev=0
    [ -f "$OFF" ] && prev=$(cat "$OFF")
    [ "$size" -lt "$prev" ] && prev=0   # rotated/truncated: start over
    if [ "$size" -gt "$prev" ]; then
        tail -c +$((prev + 1)) /logs/metrics-access.log | awk -v ts="$ts" '
            match($0, /"uri":"[^"]*"/)    { u = substr($0, RSTART + 7, RLENGTH - 7) }
            match($0, /"status":"[0-9]+"/) { s = substr($0, RSTART + 10, RLENGTH - 10) + 0 }
            {
                b = u ~ /^\/api\// ? "api" : (u ~ /^\/metrics/ ? "metrics" : "web")
                req[b]++
                c = int(s / 100)
                if (c == 2) s2[b]++; else if (c == 4) s4[b]++; else if (c == 5) s5[b]++
            }
            END {
                for (b in req)
                    printf "{\"ts\":\"%s\",\"bucket\":\"%s\",\"requests\":%d,\"s2xx\":%d,\"s4xx\":%d,\"s5xx\":%d}\n",
                           ts, b, req[b], s2[b], s4[b], s5[b]
            }' >> /logs/metrics-requests.jsonl
        echo "$size" > "$OFF"
    fi
    sleep "${METRICS_INTERVAL:-15}"
done
