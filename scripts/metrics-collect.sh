#!/bin/sh
# Metrics collector loop: appends JSONL snapshots to the app-logs volume.
#  - metrics-stats.jsonl    per-task docker stats (cpu/mem/net/pids)
#  - metrics-services.jsonl service replica counts
#  - metrics-requests.jsonl per-edge-bucket request counts aggregated from the
#    nginx access log (delta since the previous sample; tracked by byte offset
#    so rotation resets it rather than double-counting)
# Socket is mounted read-only; log rotation bounds the files.
set -u

OFF=/logs/.metrics-access.offset
while true; do
    ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    docker stats --no-stream --format '{{json .}}' \
        | sed "s/^{/{\"ts\":\"$ts\",/" >> /logs/metrics-stats.jsonl
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
