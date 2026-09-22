---
name: running-dating-coach-in-docker
description: Build the dating-coach backend, ml-analyzer and web images, publish them to Docker Hub (danielliu30/*) manually or via the release.yml CI/CD pipeline, run the stack from the published images, deploy/roll back on the production VM, and expose it through nginx (compose gateway service or host-installed nginx).
---

# Running dating-coach in Docker behind nginx

## Images
| Compose service | Image | Notes |
|---|---|---|
| `api`, `worker` | `danielliu30/dating-coach-backend` | one image, two binaries; compose picks with `command: [api]` / `[worker]` |
| `ml-analyzer` | `danielliu30/dating-coach-ml-analyzer` | torch-free; heuristic scorer when no `LLM_API_KEY` |
| `web` (`--profile gateway`) | `danielliu30/dating-coach-web` | static Expo web export served by nginx:alpine; `WEB_API_URL` build arg (empty = same-origin) |

Tags default to `latest`; override with `DOCKERHUB_NAMESPACE` / `IMAGE_TAG` env vars (they feed the `image:` fields in `docker-compose.yml`).

## Build and publish (manual)
CI does this on every merge to `main` (see [Deployment flow](#deployment-flow-cicd)); build by hand only for ad-hoc testing.
```bash
cd <repo>
echo "$DOCKERHUB_TOKEN" | docker login -u danielliu30 --password-stdin   # PAT from session secrets
docker compose --profile gateway build     # tags images with the Hub names above
docker compose --profile gateway push api ml-analyzer web   # `worker` shares the api image
```
Verify with the Docker Hub MCP server (`mcp_tool server=dockerhub`):
`listRepositoriesByNamespace {"namespace":"danielliu30"}` or `listRepositoryTags {"namespace":"danielliu30","repository":"dating-coach-backend"}`.
Repository descriptions can be set with `updateRepositoryInfo`.

## Deployment flow (CI/CD)
`.github/workflows/release.yml` (trigger: `push` to `main`, or `workflow_dispatch`):
1. Runs `backend.yml`, `app.yml`, `ml-analyzer.yml`, `e2e.yml` as reusable workflows (`workflow_call`). Any failure stops the release.
2. `build-push`: `docker compose --profile gateway build api ml-analyzer web` with `IMAGE_TAG=${{ github.sha }}`, `DOCKERHUB_NAMESPACE=$DOCKERHUB_USERNAME`, `WEB_API_URL=""` (same-origin bundle) and `GOOGLE_CLIENT_ID` from the repo variable; pushes the SHA tags, then promotes `latest` only if the run is on `main` and `github.sha` is still `origin/main` (manual dispatches on other branches and superseded main runs publish SHA tags only).
3. `deploy`: `appleboy/ssh-action` into the VM, then in `DEPLOY_DIR` (default `~/dating-coach`):
   ```bash
   git fetch origin && git show "$SHA:docker-compose.yml" > /tmp/c.yml
   IMAGE_TAG=$SHA docker compose -f /tmp/c.yml --project-directory . --profile gateway pull api worker ml-analyzer web   # first: a bad tag changes nothing on the VM
   git fetch origin && git checkout --detach "$SHA"      # compose file, nginx conf, backend/migrations must match the images
   sed -i '/^IMAGE_TAG=/d;/^DOCKERHUB_NAMESPACE=/d' .env && printf 'IMAGE_TAG=%s\nDOCKERHUB_NAMESPACE=%s\n' "$SHA" "$NS" >> .env
   IMAGE_TAG=$SHA docker compose --profile gateway up -d --no-build
   curl -fsS $BASE/healthz && curl -fsS $BASE/ml/healthz && curl -fsS $BASE/   # BASE from DEPLOY_HEALTHCHECK_URL; retried 90 s, job fails otherwise
   ```
   Pinning `IMAGE_TAG` in the VM's `.env` means a manual `docker compose up -d` on the VM keeps the deployed SHA instead of drifting to `latest`.

### GitHub secrets / variables
| Name | Kind | Purpose |
|---|---|---|
| `DOCKERHUB_USERNAME` | secret | Hub account + image namespace |
| `DOCKERHUB_TOKEN` | secret | Hub PAT (Read & Write) |
| `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY` | secret | SSH target for the deploy job (user must be in the `docker` group) |
| `GOOGLE_CLIENT_ID` | variable | baked into the web bundle (public, so not a secret) |
| `DEPLOY_DIR` | variable | compose directory on the VM (default `~/dating-coach`) |
| `DEPLOY_HEALTHCHECK_URL` | variable | default `http://localhost/healthz`; `https://<domain>/healthz` once TLS exists |

### Roll back
Actions -> *Release (build, push, deploy)* -> *Run workflow*, `image_tag` = an earlier commit SHA (list with `listRepositoryTags` on the Docker Hub MCP server). Tests and build are skipped; only `deploy` runs with that tag. If a deploy's health checks fail, the job itself reverts the VM to the previously pinned `IMAGE_TAG` (read from `.env`) and its checkout, then exits non-zero. Manual equivalent on the VM: edit `IMAGE_TAG=` in `.env`, then `docker compose --profile gateway pull && docker compose --profile gateway up -d --no-build`.

### One-time VM prerequisites
- Docker Engine + Compose plugin; deploy user runs `docker` without sudo.
- Git checkout of the repo at `DEPLOY_DIR` — required; the deploy job checks out the deployed SHA there.
- Rollback caveat: `migrate` only runs `up`, so redeploying an older SHA does not revert schema changes; keep migrations backward-compatible or restore the DB first.
- Production `.env` in that directory (from `.env.example`: `APP_ENV`, `JWT_SECRET`, `POSTGRES_PASSWORD`, `PUBLIC_APP_URL`, `CORS_ORIGINS`, SMTP, Stripe, `GOOGLE_CLIENT_ID`). The deploy job only rewrites `IMAGE_TAG`/`DOCKERHUB_NAMESPACE`.
- SSH key pair: `ssh-keygen -t ed25519 -f deploy_key -N ''`; public half in `~/.ssh/authorized_keys`, private half in `DEPLOY_SSH_KEY`.
- Port 80 reachable (or host nginx fronting it with `NGINX_PORT` moved, see below). Smoke-test once by hand with `docker compose --profile gateway up -d` before enabling the secrets.

## Run from the published images
```bash
cp .env.example .env                       # only if .env missing; set JWT_SECRET for anything non-local
docker compose --profile gateway pull api ml-analyzer web   # drop `--profile gateway`/`web` for API-only
docker compose up -d --no-build            # postgres, redis, rabbitmq, migrate (one-shot), api:8080, worker, ml-analyzer:8000
docker compose ps
curl localhost:8080/healthz                # {"env":"development","status":"ok"}
curl localhost:8000/healthz                # {"status":"ok", ... "model_version":"heuristic-v1"}
docker compose logs worker | grep 'worker started'
```
Drop `--no-build` to build locally instead of pulling.

## Expose through nginx
Routes (identical in both variants):
- `/api/*` and `/healthz` -> api (`Upgrade`/`Connection` headers forwarded, `proxy_read_timeout 1h` for chat sockets)
- `/ml/*` -> ml-analyzer with the `/ml` prefix stripped (`/ml/healthz` -> `:8000/healthz`)
- `/` -> `web` (static Expo bundle; SPA fallback to index.html so `/verify?token=…` deep links work)
- `Host` is forwarded as `$http_host` (with port): the API's WebSocket same-origin check compares `Origin` to `Host`, so a gateway on a non-80 port still accepts chat sockets

### A. Containerised gateway (no nginx on the host)
```bash
docker compose --profile gateway up -d     # adds `web` and `nginx` (nginx:1.27-alpine) on ${NGINX_PORT:-80}
curl localhost/healthz && curl localhost/ml/healthz && curl -sI localhost/ | head -1
```
Config: `nginx/default.conf`, mounted read-only; upstreams are the compose service names `api:8080` / `ml-analyzer:8000` / `web:80`.
If :80 is taken (e.g. by a host nginx) set `NGINX_PORT=8088` **and** `PUBLIC_APP_URL=http://localhost:8088` (add it to `CORS_ORIGINS` too) in `.env`; verification/payment links are built from `PUBLIC_APP_URL`.

### B. Host-installed nginx
```bash
sudo cp nginx/host-site.conf /etc/nginx/sites-available/dating-coach
sudo ln -sf /etc/nginx/sites-available/dating-coach /etc/nginx/sites-enabled/dating-coach
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t && sudo systemctl reload nginx
curl localhost/healthz && curl localhost/ml/healthz
```
Upstreams are `127.0.0.1:8080` / `127.0.0.1:8000` / `127.0.0.1:3000` (`web`, published on `WEB_PORT`), i.e. the ports compose publishes. The `web` container still needs `--profile gateway`, so run it with `NGINX_PORT` moved off :80; if you change `WEB_PORT`, edit the `location /` upstream in host-site.conf to match.

### Smoke-test the proxy
```bash
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost/api/v1/auth/signup -H 'content-type: application/json' -d '{}'   # 400 = reached the API
curl -s -o /dev/null -w '%{http_code}\n' -H 'Upgrade: websocket' -H 'Connection: Upgrade' -H 'Sec-WebSocket-Version: 13' \
  -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' localhost/api/v1/chat/ws                                                 # 401 = upgrade forwarded, auth required
```

### Point the Expo app at the gateway
```bash
cd app && EXPO_PUBLIC_API_URL=http://localhost setsid npx expo start --web --port 8081 > /tmp/expo.log 2>&1 &
```
`CORS_ORIGINS` in `.env` must include the app origin (default already has `http://localhost:8081`).
For the end-to-end flow (sign-up, verification token, chat) follow `.agents/skills/testing-dating-coach/SKILL.md`.

## Postgres backups
`scripts/backup.sh` / `scripts/restore.sh` run against the compose stack from any cwd; they read `POSTGRES_USER`/`POSTGRES_DB` and `BACKUP_*`/`AWS_*` from the repo `.env` (env vars already set win).
```bash
scripts/backup.sh                     # -> $BACKUP_DIR/<db>-<UTC stamp>.sql.gz (pg_dump --clean --if-exists | gzip), prunes > BACKUP_RETENTION_DAYS (14), uploads if BACKUP_S3_URI is set
scripts/restore.sh <file|s3://...>    # confirm (or --yes) -> stop api+worker -> DROP/CREATE SCHEMA public + replay in one transaction -> start api+worker
```
- Cron on the VM (deploy user): `15 3 * * * $HOME/dating-coach/scripts/backup.sh >> $HOME/dating-coach-backup.log 2>&1`.
- Off-box: `BACKUP_S3_URI=s3://bucket/prefix`, `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`, `BACKUP_S3_ENDPOINT` for non-AWS providers; needs the `aws` CLI. Remote retention is a bucket lifecycle rule, not the script.
- Rollback across a breaking migration: `restore.sh` a pre-migration dump first, then dispatch the release workflow with the older `image_tag` (`migrate` only runs `up`).
- Second layer: provider snapshots of the VM disk / `dating-coach_postgres-data` volume.

## Gotchas
- `api` and `worker` restart until `migrate` exits 0 and redis/rabbitmq are healthy; give the stack ~20 s.
- nginx writes the access-log line for a WebSocket (`… /ws … 101`) only when the socket closes; reload the chat page to see it in `docker compose logs nginx`.
- There is no `/api/v1/healthz`; the API health check is `/healthz` at the root.
- `docker compose push` needs `docker login` first; the PAT is not available during snapshot builds.
- The Hub repos are public on the personal account; images contain no secrets (all config via env at runtime).
