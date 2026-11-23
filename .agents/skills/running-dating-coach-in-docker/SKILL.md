---
name: running-dating-coach-in-docker
description: Build the dating-coach backend, ml-analyzer and web images, publish them to Docker Hub (danielliu30/*) manually or via the release.yml CI/CD pipeline, deploy locally from the published images (default) or to Google Cloud Run behind the DEPLOY_TARGET switch, and expose the stack through nginx (compose gateway service or host-installed nginx).
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
`main` is protected: PRs are required and auto-merge as merge commits once the
required checks (`build-test`, `typecheck-export`, `test`, `e2e`,
`dependency-gate`, `merge-gate-tests`) pass. Merge order for dependent PRs is declared in the PR
body (`Depends on: #N`) and enforced by `merge-gate.yml`; see
`.agents/skills/pr-granularity/SKILL.md` for the protocol. Every merge to `main`
therefore flows straight into the release below — the path from green PR to
production is fully automated. Watch a deploy with
`gh run list --workflow=release.yml` / `gh run watch <run-id>`.

`.github/workflows/release.yml` (trigger: `push` to `main`, or `workflow_dispatch`):
1. Runs `backend.yml`, `app.yml`, `ml-analyzer.yml`, `e2e.yml` as reusable workflows (`workflow_call`). Any failure stops the release.
2. `build-push`: `docker compose --profile gateway build api ml-analyzer web migrate` with `IMAGE_TAG=${{ github.sha }}`, `DOCKERHUB_NAMESPACE=$DOCKERHUB_USERNAME`, `WEB_API_URL=$API_PUBLIC_URL` and `GOOGLE_CLIENT_ID` from repo variables; pushes the SHA tags, then promotes `latest` only if the run is on `main` and `github.sha` is still `origin/main` (manual dispatches on other branches and superseded main runs publish SHA tags only).
3. Deploy is behind a **switch** — the repository variable `DEPLOY_TARGET`:
   - **unset / anything but `cloud-run` (current default): local deploys.** The `deploy-local` job prints the pull commands and the run stays green; nothing touches a cloud. "Deploy" means running the published images yourself, locally or on a VM:
     ```bash
     IMAGE_TAG=<git-sha> docker compose --profile gateway pull
     IMAGE_TAG=<git-sha> docker compose --profile gateway up -d
     ```
     Pinning `IMAGE_TAG` (in `.env` or inline) keeps a later `up -d` on the deployed SHA instead of drifting to `latest`.
   - **`cloud-run`: the Google Cloud Run deploy** (api/worker/ml/web services + a migrate job + Pub/Sub push subs + Cloud Scheduler; see `docs/deploy-cloud-run.md`). Requires the one-time `scripts/gcp-bootstrap.sh` run (GCP project, Neon, Upstash, LLM key) which prints the `GCP_*` GitHub variables. If api or web health checks fail on the new revision, traffic is reverted to the previous revisions automatically.
   - `load-test` / `scale-approval` only run when `DEPLOY_TARGET=cloud-run` *and* `SCALE_PROFILE=production`.

### GitHub secrets / variables
| Name | Kind | Purpose |
|---|---|---|
| `MERGE_BOT_TOKEN` | secret | PAT (`contents:write` + `pull_requests:write`) so workflow-armed merges fire `closed`/`push` events — without it the merge cascade and `release.yml` never run for auto-merged PRs |
| `DOCKERHUB_USERNAME` | secret | Hub account + image namespace |
| `DOCKERHUB_TOKEN` | secret | Hub PAT (Read & Write) |
| `DEPLOY_TARGET` | variable | `cloud-run` enables the Cloud Run deploy job; unset/absent = local deploys only |
| `GOOGLE_CLIENT_ID` | variable | baked into the web bundle (public, so not a secret) |
| `API_PUBLIC_URL`, `PUBLIC_APP_URL` | variable | api URL baked into the web bundle; public web URL for CORS + health checks |
| `GCP_PROJECT_ID`, `GCP_REGION`, `GCP_WORKLOAD_IDENTITY_PROVIDER`, `GCP_DEPLOY_SERVICE_ACCOUNT`, `GCP_RUNTIME_SERVICE_ACCOUNT`, `GCP_WEB_SERVICE_ACCOUNT`, `GCP_INVOKER_SERVICE_ACCOUNT` | variable | Cloud Run deploy targets; printed by `scripts/gcp-bootstrap.sh`. Only used when `DEPLOY_TARGET=cloud-run` |
| `SMTP_*`, `MAIL_FROM`, `LLM_PROVIDER`, `LLM_MODEL`, `SCALE_PROFILE`, `*_INSTANCES` | variable | app/runtime settings for the Cloud Run services |

### Roll back
- **Local deploy (current):** `IMAGE_TAG=<older-sha> docker compose --profile gateway pull && docker compose --profile gateway up -d` — caveat: `migrate` only runs `up`, so redeploying an older SHA does not revert schema changes; keep migrations backward-compatible or restore a dump first (`scripts/backup.sh` / `scripts/restore.sh <dump> --no-start`).
- **Cloud Run:** Actions -> *Release* -> *Run workflow*, `image_tag` = an earlier commit SHA (list with `listRepositoryTags` on the Docker Hub MCP server); tests and build are skipped. A deploy whose health checks fail already reverts traffic itself.

### Local/VM deploy prerequisites
- Docker Engine + Compose plugin; a checkout of the repo (or just `docker-compose.yml` + `nginx/`) and a `.env` (from `.env.example`: `APP_ENV`, `JWT_SECRET`, `POSTGRES_PASSWORD`, `PUBLIC_APP_URL`, `CORS_ORIGINS`, SMTP, `GOOGLE_CLIENT_ID`; `NGINX_PORT`/`WEB_PORT` for host-nginx setups).
- Set `APP_ENV=production` after provisioning non-default Postgres and RabbitMQ credentials (`RABBITMQ_USER`/`RABBITMQ_PASSWORD`), a random `JWT_SECRET` (e.g. `openssl rand -hex 32`), and real SMTP if the relay requires auth. Outside `development`, API/worker require SMTP with TLS (`SMTP_TLS` defaults to true and cannot be false); use implicit TLS on port 465 or mandatory STARTTLS on other ports. They reject shipped credentials at startup and log variable names, not values. Changing `.env` alone does not rotate credentials in an existing database or broker.
- Ports 80/443 reachable, host nginx + certbot installed (variant B below) with `NGINX_PORT`/`WEB_PORT` moved off 80. Smoke-test once by hand with `docker compose --profile gateway up -d` before enabling the secrets.

## Run from the published images
For local use, copying `.env.example` explicitly sets `APP_ENV=development` and
allows example secrets. Compose and standalone Go binaries default to production
when `APP_ENV` is omitted or blank, rejecting shipped secrets at startup. Keep
`APP_ENV=production` and provision non-default secrets for deployments.

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

All non-gateway published ports default to `127.0.0.1`: Postgres, Redis, RabbitMQ (AMQP/management), API, ML, raw web, Mailpit and pgAdmin. Their `*_PORT` variables accept host bindings like `API_PORT=127.0.0.1:8082`; a bare port or `0.0.0.0:port` explicitly exposes the service. Update old bare-port overrides in existing `.env` files to loopback bindings. Container-to-container URLs keep the original service names and ports. Only the nginx gateway retains a public default binding.

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

### B. Host-installed nginx with TLS (production)
`nginx/host-site.conf` = port-80 server (ACME challenge + 301 to https) and a `443 ssl` server with the same routes, cert paths `/etc/letsencrypt/live/<domain>/{fullchain,privkey}.pem`. The file uses `example.com` as the placeholder domain.
```bash
sudo apt-get install -y nginx certbot python3-certbot-nginx
sudo certbot certonly --nginx -d <domain>        # before enabling the site: the 443 block fails nginx -t without the cert files
sed 's/example.com/<domain>/g' nginx/host-site.conf | sudo tee /etc/nginx/sites-available/dating-coach >/dev/null
sudo ln -sf /etc/nginx/sites-available/dating-coach /etc/nginx/sites-enabled/dating-coach
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t && sudo systemctl reload nginx
systemctl status certbot.timer && sudo certbot renew --dry-run   # auto-renewal (systemd timer, twice daily)
printf '#!/bin/sh\nsystemctl reload nginx\n' | sudo tee /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh >/dev/null && sudo chmod +x /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh
curl -fsS https://<domain>/healthz && curl -fsS https://<domain>/ml/healthz && curl -sI http://<domain>/ | head -1   # last one: 301
```
Upstreams are `127.0.0.1:8080` / `127.0.0.1:8000` / `127.0.0.1:3000`, matching the default `API_PORT`, `ML_ANALYZER_PORT` and `WEB_PORT` bindings. The `web` container still needs `--profile gateway`, so in `.env` set `NGINX_PORT=127.0.0.1:8088` (off :80, localhost-only); web already defaults to loopback. If you change any of these upstream port bindings, edit the corresponding upstream in host-site.conf to match. Also set `PUBLIC_APP_URL=https://<domain>`, `CORS_ORIGINS=https://<domain>` and the GitHub variable `DEPLOY_HEALTHCHECK_URL=https://<domain>/healthz`.

WebSocket over HTTPS: the API's same-origin check (`coder/websocket` `authenticateOrigin`) compares the `Origin` host to `r.Host`, both `<domain>` because nginx forwards `Host: $http_host`, so `wss://<domain>/api/v1/chat/ws` is accepted; `https://<domain>` in `CORS_ORIGINS` also matches as a scheme-qualified pattern.

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
scripts/restore.sh <file|s3://...>    # confirm (or --yes) -> stop api+worker -> DROP/CREATE SCHEMA public + replay in one transaction -> run migrate -> start api+worker
scripts/restore.sh <dump> --no-start  # same, but skip migrate and leave api+worker stopped (use before deploying an OLDER image tag)
```
- Cron on the VM (deploy user): `15 3 * * * $HOME/dating-coach/scripts/backup.sh >> $HOME/dating-coach-backup.log 2>&1`.
- Off-box: `BACKUP_S3_URI=s3://bucket/prefix`, `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`, `BACKUP_S3_ENDPOINT` for non-AWS providers; needs the `aws` CLI. Remote retention is a bucket lifecycle rule, not the script.
- Rollback across a breaking migration: `restore.sh <pre-migration dump> --no-start` first, then dispatch the release workflow with the older `image_tag` (`migrate` only runs `up`; without `--no-start` the current release's migrate would re-apply the new schema).
- A dump is a snapshot as of when `pg_dump` started (that is the recovery point); retention keeps the last `BACKUP_RETENTION_DAYS` daily dumps locally.
- Second layer: provider snapshots of the VM disk / `dating-coach_postgres-data` volume.

## Gotchas
- `api` and `worker` restart until `migrate` exits 0 and redis/rabbitmq are healthy; give the stack ~20 s.
- nginx writes the access-log line for a WebSocket (`… /ws … 101`) only when the socket closes; reload the chat page to see it in `docker compose logs nginx`.
- There is no `/api/v1/healthz`; the API health check is `/healthz` at the root.
- `nginx -t` on the host fails with "cannot load certificate" if the site is enabled before certbot ran; issue the cert first (or comment out the 443 server temporarily).
- `docker compose push` needs `docker login` first; the PAT is not available during snapshot builds.
- The Hub repos are public on the personal account; images contain no secrets (all config via env at runtime).
