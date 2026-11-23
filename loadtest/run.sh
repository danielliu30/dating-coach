#!/usr/bin/env bash
# run.sh — run the load-test suite at the standard profile against the
# gateway stack. Bring the stack up first:
#
#   docker compose --env-file loadtest/.env \
#     -f docker-compose.yml -f loadtest/docker-compose.override.yml \
#     --profile gateway up -d --build
#
# then:  loadtest/run.sh
#
# Env overrides:
#   LT_BASE=http://localhost   gateway origin
#   LT_VUS=10,20,50            stage VU list
#   LT_RPS=150                 aggregate actions/sec per stage
#   LT_DURATION=30s            per stage
#   LT_COMPONENTS="sanity auth chat booking analysis e2e"
set -euo pipefail
cd "$(dirname "$0")"

BASE="${LT_BASE:-http://localhost}"
VUS="${LT_VUS:-10,20,50}"
RPS="${LT_RPS:-150}"
DUR="${LT_DURATION:-30s}"
COMPONENTS="${LT_COMPONENTS:-sanity auth chat booking analysis e2e}"

curl -fsS "$BASE/healthz" >/dev/null || { echo "api unhealthy at $BASE/healthz"; exit 1; }
curl -fsS "$BASE/ml/healthz" >/dev/null || { echo "ml-analyzer unhealthy at $BASE/ml/healthz"; exit 1; }

go build ./...

run() {
	local c="$1"
	shift
	echo "== lt-$c $* =="
	go run "./cmd/lt-$c" -base "$BASE" "$@" || echo "lt-$c exited with $?"
}

for c in $COMPONENTS; do
	case "$c" in
	sanity)
		run sanity
		;;
	e2e)
		for j in book chat analyze; do
			run e2e -journey "$j" -vus "$VUS" -rps "$RPS" -duration "$DUR"
		done
		;;
	*)
		run "$c" -vus "$VUS" -rps "$RPS" -duration "$DUR"
		;;
	esac
done

(python summarize.py || python3 summarize.py) 2>/dev/null ||
	echo "(no python — reports are in out/)"
