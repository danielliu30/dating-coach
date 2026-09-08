---
name: running-dating-coach-in-docker
description: Build the dating-coach backend and ml-analyzer images, publish them to Docker Hub (danielliu30/*), run the stack from the published images, and expose it through nginx (compose gateway service or host-installed nginx).
---

# Running dating-coach in Docker behind nginx

## Images
| Compose service | Image | Notes |
|---|---|---|
| `api`, `worker` | `danielliu30/dating-coach-backend` | one image, two binaries; compose picks with `command: [api]` / `[worker]` |
| `ml-analyzer` | `danielliu30/dating-coach-ml-analyzer` | torch-free; heuristic scorer when no `LLM_API_KEY` |
| `web` (`--profile gateway`) | `danielliu30/dating-coach-web` | static Expo web export served by nginx:alpine; `WEB_API_URL` build arg (empty = same-origin) |

Tags default to `latest`; override with `DOCKERHUB_NAMESPACE` / `IMAGE_TAG` env vars (they feed the `image:` fields in `docker-compose.yml`).

## Build and publish
```bash
cd <repo>
echo "$DOCKERHUB_TOKEN" | docker login -u danielliu30 --password-stdin   # PAT from session secrets
docker compose --profile gateway build     # tags images with the Hub names above
docker compose --profile gateway push api ml-analyzer web   # `worker` shares the api image
```
Verify with the Docker Hub MCP server (`mcp_tool server=dockerhub`):
`listRepositoriesByNamespace {"namespace":"danielliu30"}` or `listRepositoryTags {"namespace":"danielliu30","repository":"dating-coach-backend"}`.
Repository descriptions can be set with `updateRepositoryInfo`.

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

## Gotchas
- `api` and `worker` restart until `migrate` exits 0 and redis/rabbitmq are healthy; give the stack ~20 s.
- nginx writes the access-log line for a WebSocket (`… /ws … 101`) only when the socket closes; reload the chat page to see it in `docker compose logs nginx`.
- There is no `/api/v1/healthz`; the API health check is `/healthz` at the root.
- `docker compose push` needs `docker login` first; the PAT is not available during snapshot builds.
- The Hub repos are public on the personal account; images contain no secrets (all config via env at runtime).
