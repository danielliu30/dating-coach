#!/usr/bin/env bash
# Automated rolling deploy of the dating-coach stack onto a single-node
# Docker Swarm. Polls origin/main; when it moves, fast-forwards a dedicated
# deploy checkout, rebuilds images locally, runs the migrate gate, then
# `docker stack deploy` rolls each service (start-first + healthz for
# stateless, stop-first for stateful singletons — see deploy/stack.yml).
#
# Correctness ordering:
#   1. git fetch / ff-merge main   (dirty or non-main checkout -> skip, fail closed)
#   2. docker compose build        (images built locally — published :latest
#                                   lags the self-host Dockerfiles)
#   3. migrate gate                (blocking `docker run` on the stack net;
#                                   nonzero exit aborts the deploy)
#   4. docker stack deploy         (rolling; update failure auto-rolls back)
#   5. convergence + /healthz check
#
# Migrations are backward-compatible by repo rule, so old tasks keep serving
# while the schema lands; api WS sessions drop on task drain regardless —
# rolling keeps HTTP up, it cannot preserve sockets.
#
# Usage:   scripts/deploy-watch.sh            (cron-friendly, e.g. every 5 min:
#          */5 * * * * /path/to/checkout/scripts/deploy-watch.sh >> /var/log/deploy-watch.log 2>&1)
# Reads:   <repo>/.env   DEPLOY_DIR (default: this checkout — point at a
#                        dedicated `git worktree` on main so the loop never
#                        touches a dev working tree)
#                        DEPLOY_STACK (default dating-coach)
#                        DEPLOY_GIT_REF (default origin/main)
#                        DEPLOY_HEALTH_URL (default http://localhost/healthz)
#                        POSTGRES_USER / POSTGRES_PASSWORD / POSTGRES_DB
#                        DOCKERHUB_NAMESPACE / IMAGE_TAG (image refs)
#          Plus whatever the compose build and stack env interpolation need.
# Exit:    0 = already current or deploy succeeded; non-zero on fetch, guard,
#          build, migrate, deploy or verify failure.
set -euo pipefail

# Under Git Bash (incl. the Task Scheduler entry), MSYS rewrites args like
# -path=/migrations into C:/... before docker sees them. Disable it globally.
export MSYS2_ARG_CONV_EXCL='*'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="${ENV_FILE:-$REPO_DIR/.env}"

# load_env exports deploy + image + postgres vars from $ENV_FILE; env wins.
load_env() {
    [ -f "$ENV_FILE" ] || return 0
    local line key value
    while IFS= read -r line || [ -n "$line" ]; do
        # Export every KEY=VALUE line: docker stack deploy interpolates
        # deploy/stack.yml from the process environment only — there is no
        # --env-file support — so stack vars (JWT_SECRET, RABBITMQ_*,
        # LLM_*, …) must all be present. Environment always wins.
        case "$line" in
            ''|\#*|*=*) ;;
            *) continue ;;
        esac
        case "$line" in
            ''|\#*) continue ;;
        esac
        key="${line%%=*}"
        value="${line#*=}"
        value="${value%\"}"; value="${value#\"}"
        value="${value%\'}"; value="${value#\'}"
        [ -n "${!key+x}" ] || export "$key=$value"
    done < "$ENV_FILE"
}
load_env

DEPLOY_DIR="${DEPLOY_DIR:-$REPO_DIR}"
DEPLOY_STACK="${DEPLOY_STACK:-dating-coach}"
DEPLOY_GIT_REF="${DEPLOY_GIT_REF:-origin/main}"
DEPLOY_HEALTH_URL="${DEPLOY_HEALTH_URL:-http://localhost/healthz}"
POSTGRES_USER="${POSTGRES_USER:-datingcoach}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-datingcoach}"
POSTGRES_DB="${POSTGRES_DB:-datingcoach}"
DOCKERHUB_NAMESPACE="${DOCKERHUB_NAMESPACE:-danielliu30}"
NET="${DEPLOY_STACK}_appnet"

log() { echo "deploy-watch[$(date -u +%Y-%m-%dT%H:%M:%SZ)]: $*"; }

git -C "$DEPLOY_DIR" fetch --quiet
local_sha="$(git -C "$DEPLOY_DIR" rev-parse HEAD)"
remote_sha="$(git -C "$DEPLOY_DIR" rev-parse "$DEPLOY_GIT_REF")"
if [ "$local_sha" = "$remote_sha" ]; then
    exit 0
fi
log "new revision $remote_sha (was ${local_sha:0:8})"

# Immutable per-commit image tags: the build below labels every image with
# the deploy SHA and stack.yml's ${IMAGE_TAG} refs resolve to it. A mutable
# :latest tag would make rollback_config redeploy the same tag — rolling
# "back" to the identical broken image.
IMAGE_TAG="${IMAGE_TAG:-${remote_sha:0:12}}"
export IMAGE_TAG
MIGRATE_IMAGE="${DOCKERHUB_NAMESPACE}/dating-coach-migrate:${IMAGE_TAG}"

# Fail-closed guards: never deploy over uncommitted work or a non-main branch.
branch="$(git -C "$DEPLOY_DIR" rev-parse --abbrev-ref HEAD)"
if [ "$branch" != "main" ]; then
    log "checkout is on '$branch', not main — skipping (use a dedicated main worktree)" >&2
    exit 0
fi
if ! git -C "$DEPLOY_DIR" diff --quiet || ! git -C "$DEPLOY_DIR" diff --cached --quiet; then
    log "dirty working tree at $DEPLOY_DIR — skipping" >&2
    exit 0
fi
git -C "$DEPLOY_DIR" merge --ff-only "$DEPLOY_GIT_REF"

# 2. Build images from docker-compose.yml — the single source of truth.
log "building images"
docker compose --project-directory "$DEPLOY_DIR" --env-file "$ENV_FILE" build

# Swarm-scoped overlay the migrate gate and stack share; created on first
# boot before the stack exists.
docker network inspect "$NET" >/dev/null 2>&1 \
    || docker network create --driver overlay --attachable "$NET" >/dev/null

# 3. Migrate gate — must succeed before any app task rolls. Bootstrap path:
# when postgres isn't running yet there is nothing to migrate; let the stack
# bring infra up first and converge on its own.
if docker service ls -q -f "name=${DEPLOY_STACK}_postgres" | grep -q .; then
    log "running migrate gate"
    docker run --rm --network "$NET" "$MIGRATE_IMAGE" \
        -path=/migrations \
        -database="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${POSTGRES_DB}?sslmode=disable" \
        up
else
    log "no ${DEPLOY_STACK}_postgres service yet — bootstrap deploy (migrations run next pass or by hand)"
fi

# 4. Rolling deploy.
log "stack deploy"
docker stack deploy -c "$DEPLOY_DIR/deploy/stack.yml" "$DEPLOY_STACK"

# 5. Convergence: every service at desired replicas within 5 minutes.
deadline=$((SECONDS + 300))
while :; do
    pending="$(docker stack services "$DEPLOY_STACK" --format '{{.Replicas}} {{.Name}}' | awk -F'[ /]' '$1 != $2 {print $3}')"
    [ -z "$pending" ] && break
    if [ "$SECONDS" -ge "$deadline" ]; then
        log "services not converged after 300s: $pending" >&2
        docker stack services "$DEPLOY_STACK"
        exit 1
    fi
    sleep 5
done

if curl -fsS --max-time 10 "$DEPLOY_HEALTH_URL" >/dev/null; then
    log "deployed ${remote_sha:0:8} — $DEPLOY_HEALTH_URL healthy"
else
    log "converged but $DEPLOY_HEALTH_URL not healthy" >&2
    exit 1
fi
