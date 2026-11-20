# Dating Coach

> New here? Read the product & architecture overview in [docs/OVERVIEW.md](docs/OVERVIEW.md).

A dating-coach platform in three independent components:

| Path           | Stack                                   | Responsibility                                                       |
| -------------- | --------------------------------------- | -------------------------------------------------------------------- |
| `backend/`     | Go 1.25, chi, pgx + sqlc, Redis, RabbitMQ | REST + WebSocket API, auth, coaching, live chat, analysis worker      |
| `app/`         | Expo SDK 57, TypeScript, react-native-web | One codebase for iOS, Android and web                                |
| `ml-analyzer/` | Python 3.12, FastAPI                      | Two tracks: `/analyze` critiques the customer's messages, `/analyze/images` checks profile photos; both tailored to the customer's stated preferences |

Two product surfaces:

1. **Human coaching** — clients browse coaches, book a session against real availability, or open a live chat. Coaches (users with role `coach`) get a dashboard of upcoming sessions and active chats.
2. **Conversation and photo analysis** — a client pastes a dating-app conversation and gets per-stretch scores plus hints on how their own messages landed, or submits profile photos and gets a clarity / focal-point verdict per photo. Both are produced by the ML service and tailored to the client's stated dating preferences.

## Architecture

```
Expo app (iOS / Android / web)
   │  REST  /api/v1/...            WebSocket  /api/v1/chat/threads/{id}/ws
   ▼
Go API ──── PostgreSQL (users, coaches, sessions, chat, conversations, analysis_results)
   ├─────── Redis        (auth rate limits, chat pub/sub, presence, typing)
   ├─────── RabbitMQ ──► Go worker ──HTTP──► ml-analyzer /analyze         (messages, async)
   │                            │
   │                            └─ writes analysis_results + "analysis ready" notification
   └───────────────────────────HTTP──► ml-analyzer /analyze/images  (photos, sync, nothing stored)
```

- Analysis is asynchronous: `POST /api/v1/analysis/conversations` stores the transcript, creates a `pending` result and publishes a job. The worker calls the ML service, stores per-segment scores as JSONB and notifies the user. The app polls the result endpoint.
- Live chat messages are persisted in PostgreSQL and fanned out over Redis pub/sub, so any API replica can serve a socket. How the per-thread chat hub and the per-user notification hub bridge sockets to Redis (topology, the `ready` handshake, revocation, and why delivery is deliberately at-most-once) is written up in [docs/realtime.md](docs/realtime.md).
- Sessions are a short-lived access JWT (15m) plus a rotating refresh token stored hashed in PostgreSQL. Authenticating a request is pure signature checking — no database round trip — and ending a session means deleting its refresh tokens: `POST /api/v1/auth/refresh` rotates one into a new pair, and replaying a spent token drops every refresh token of that account. Open chat sockets outlive the token that opened them, so they alone still re-check the Redis revocation record, and close on their token's expiry for the client to renew and reconnect.
- Account deletion goes through an outbox: `DELETE /api/v1/auth/me` stamps `users.deleted_at` and records the deletion in `account_deletions` in one statement, which is the only failure the caller is told about — once it commits the deletion is certain, because a relay in the worker queues every recorded deletion the request itself does not. The stamp is what locks the account out: sign-in refuses it like an unknown one and a refresh token can no longer be exchanged, so the session cannot outlive the few minutes its access token has left. Deleting the refresh tokens, revoking in Redis and publishing the job directly are optimisations the request does on a best-effort basis (they only log on failure): the revoke closes live chat sockets in milliseconds instead of waiting for the worker, and the direct publish skips the relay's next pass. The worker revokes before it removes any rows, then deletes the row (cascading across every table, refresh tokens included) and clears the outbox entry. A failed attempt waits 30s on `account.deletion.retry` before it is redelivered, and deletions that keep failing land on `account.deletion.dlq`. The worker logs that queue's depth every `DEAD_LETTER_ALERT_PERIOD`; alert on a non-zero depth, because those accounts are marked deleted but still hold rows.
- The ML service is fully decoupled — HTTP only, no shared database.

## Quick start (Docker Compose)

```bash
cp .env.example .env      # set JWT_SECRET, and LLM_API_KEY for real LLM scoring
docker compose up -d --build # postgres, redis, rabbitmq, migrations, api, worker, ml-analyzer

# Optional profiles
# docker compose --profile gateway up -d   # web app + nginx reverse proxy on :80
# docker compose --profile tools up -d     # pgAdmin on :5050
```

- API: http://localhost:8080/healthz
- ML service: http://localhost:8000/healthz (and `/docs`)
- RabbitMQ UI: http://localhost:15672 (guest / guest)

All published non-gateway ports bind to `127.0.0.1` by default, including Postgres, Redis, RabbitMQ, API, ML, raw web, Mailpit and pgAdmin. Override their `*_PORT` bindings in `.env` (for example `API_PORT=127.0.0.1:8082`); a bare port or `0.0.0.0:port` explicitly exposes that service. Container-to-container connections still use service names and their original ports. The nginx gateway keeps its existing public `NGINX_PORT=80` default.

Migrations run in a one-shot `migrate` service before `api` and `worker` start.

Client IPs for rate limits and request logs trust forwarding headers only from
`TRUSTED_PROXY_CIDRS` (default `127.0.0.0/8,::1/128,172.16.0.0/12`). Loopback
covers host nginx; the private `172.16/12` range covers Docker's default bridge
networks, including host-to-container forwarding. These ranges are trusted
infrastructure, not arbitrary clients: narrow them to your actual proxy
addresses/subnets for production, or set `none` for a directly exposed API.
If Docker uses a different subnet, configure that subnet explicitly. From a
trusted peer, `X-Forwarded-For` is walked right-to-left to the first untrusted
address; `X-Real-IP` is used only when XFF is absent. Malformed addresses fall
back to the direct peer. Both nginx configurations overwrite these headers at
ingress; do not expose another proxy that blindly relays client-supplied values.

`LOGIN_FAILURE_LIMIT` (default `10`) and `LOGIN_FAILURE_WINDOW` (default `15m`)
add a Redis-backed password-login limit per trimmed, lowercased email, including
unknown accounts. Attempts are reserved atomically before checking credentials,
so parallel guesses cannot exceed the limit. Further attempts return
`429 {"error":"too many requests"}` until the window expires. A successful
sign-in clears the counter; invalid credentials retain their reservation, while
infrastructure errors release only their own reservation. The window
starts at the first attempt, is not extended by retries, and is independent of the
existing per-IP `AUTH_RATE_LIMIT`. Redis failure makes password sign-in fail
closed with `503`. Limit and window must be positive.

Copying `.env.example` explicitly opts into `APP_ENV=development` for local use.
Without `APP_ENV` (or when blank), Compose and standalone Go binaries default to
`production`: API and worker startup reject the shipped JWT key and default
database/broker credentials. For deployments, set `APP_ENV=production` and replace
the example secrets; do not use the development opt-in.

Prebuilt images are published to Docker Hub as `danielliu30/dating-coach-backend` (both `api` and `worker` entrypoints) and `danielliu30/dating-coach-ml-analyzer`. To run from them instead of building:

```bash
docker compose pull api ml-analyzer && docker compose up -d --no-build
```

Images are published automatically by CI on every merge to `main` (see [Deployment flow (CI/CD)](#deployment-flow-cicd)); to publish by hand: `docker compose --profile gateway build && docker compose --profile gateway push api ml-analyzer web` (override the target with `DOCKERHUB_NAMESPACE` / `IMAGE_TAG`).

### Exposing the stack through nginx

`nginx/` holds one route table in two flavours — `/api/*` and `/healthz` go to the API (WebSocket upgrades included), `/ml/*` goes to the ML service, and everything else goes to the `web` image (the exported Expo web bundle built by `app/Dockerfile`, with an SPA fallback):

- Containerised: `docker compose --profile gateway up -d` adds the `web` and `nginx` services; open `http://localhost:${NGINX_PORT:-80}/`. If you change `NGINX_PORT`, set `PUBLIC_APP_URL` (and `CORS_ORIGINS`) to that origin as well.
- Host-installed nginx: install `nginx/host-site.conf` as a site (instructions in the file); it terminates TLS and proxies to the ports compose publishes on `127.0.0.1` (API 8080, ML 8000, web 3000). If you change `API_PORT`, `ML_ANALYZER_PORT` or `WEB_PORT`, update the corresponding upstream in that file. See [HTTPS](#https-host-nginx--certbot) below.

The web bundle is built with an empty `EXPO_PUBLIC_API_URL` (`WEB_API_URL` in `.env`), which means same-origin: REST and the chat WebSocket use the page's origin, so no CORS is involved. Keep `PUBLIC_APP_URL` on the nginx origin so the `/verify?token=...` deep links resolve through the proxy. The full workflow is written up in `.agents/skills/running-dating-coach-in-docker/SKILL.md`.

For native targets or hot reload, run the Expo dev server on the host instead:

```bash
cd app
npm install
npx expo start --web          # web target (react-native-web)
npx expo start                # then press i / a for iOS / Android
```

Point the app at the backend with `EXPO_PUBLIC_API_URL`. It defaults to `http://localhost:8080` (`http://10.0.2.2:8080` on the Android emulator). If you use the nginx gateway profile, set it to `http://localhost`.

Sign-up returns a short-lived `verify`-scoped token that only reaches `/api/v1/auth`; every other endpoint answers 403 until the email is confirmed, at which point verification hands back a full `session`-scoped token. Sign-in refuses accounts whose address is unconfirmed (403), and the app sends those users to the verify screen. The verification email carries a 6-digit code that expires after 3 minutes and is discarded after 3 wrong attempts. With no SMTP credentials configured the email is written to the API log instead of being sent, so grab the code locally with:

```bash
docker compose logs api | grep -i verification
```

`VERIFY_RESEND_COOLDOWN` sets the minimum interval between verification emails
per account (default `1m`, must be positive). Signup's initial email starts the
Redis-backed cooldown. Resends during it neither replace the code nor send mail;
like password reset, the endpoint returns `202` with
`{"status":"sent","resend_after_seconds":60}` using the configured interval,
including for unknown or already verified addresses. The e2e environment uses
`1s` so its verification retry helper need not wait a full minute.

### HTTPS (host nginx + certbot)

Production terminates TLS in an nginx installed on the VM; the compose stack keeps speaking plain HTTP on localhost. `nginx/host-site.conf` has a port-80 server that answers ACME challenges and 301s everything else to `https://`, and a `443 ssl` server with the same `/api/`, `/healthz`, `/ml/`, `/` routes (WebSocket `Upgrade`/`Connection` forwarding and `proxy_read_timeout 1h` included) using the Let's Encrypt paths `/etc/letsencrypt/live/<domain>/{fullchain,privkey}.pem`.

```bash
sudo apt-get install -y nginx certbot python3-certbot-nginx
sudo certbot certonly --nginx -d <domain>            # DNS must already point at the VM; nginx's default :80 site serves the challenge
sed 's/example.com/<domain>/g' nginx/host-site.conf | sudo tee /etc/nginx/sites-available/dating-coach >/dev/null
sudo ln -sf /etc/nginx/sites-available/dating-coach /etc/nginx/sites-enabled/dating-coach
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t && sudo systemctl reload nginx
```

Auto-renewal: the package installs `certbot.timer` (twice daily); check with `systemctl status certbot.timer` and `sudo certbot renew --dry-run`. Make sure the renewed cert is picked up by nginx with a deploy hook:

```bash
echo -e '#!/bin/sh\nsystemctl reload nginx' | sudo tee /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh >/dev/null
sudo chmod +x /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh
```

Then in the production `.env`:

- `NGINX_PORT=127.0.0.1:8088` — the host nginx owns 80/443, so the containerised gateway moves off 80 and binds to localhost only. `WEB_PORT=127.0.0.1:3000`, API, ML and infrastructure bindings already default to loopback; replace any existing bare-port overrides in your `.env` with loopback bindings too. Keep the VM firewall restricted to 22/80/443 as defense in depth. Then `docker compose --profile gateway up -d` as before.
- `PUBLIC_APP_URL=https://<domain>` and `CORS_ORIGINS=https://<domain>` so verification/payment links and the CORS allow-list use the public origin.
- GitHub variable `DEPLOY_HEALTHCHECK_URL=https://<domain>/healthz` so the deploy job probes through TLS.

The chat WebSocket same-origin check is unaffected by TLS: nginx forwards `Host: <domain>` and the browser sends `Origin: https://<domain>`, and the API compares only the host part, so `Origin` and `Host` still match (and `https://<domain>` in `CORS_ORIGINS` matches explicitly as well).

#### Cloudflare in front (optional)

With the host nginx + certbot setup above already working, in the Cloudflare dashboard:

1. Add the site to Cloudflare and switch the registrar's nameservers to the ones Cloudflare assigns.
2. DNS: create a **Proxied** (orange-cloud) `A` record for `<domain>` pointing at the VM IP.
3. SSL/TLS → Overview: set the encryption mode to **Full (strict)** — the origin already terminates the Let's Encrypt cert in `nginx/host-site.conf`.
4. SSL/TLS → Edge Certificates: optionally enable **Always Use HTTPS**.
5. Caching: leave the defaults. Cloudflare honors the `Cache-Control` headers set in `app/nginx.conf` (`immutable` for hashed assets, `no-cache` for `index.html`), so no page rules are needed.

On the VM, restrict inbound 443 and 80 (80 is still needed for ACME renewals) to Cloudflare's published ranges, <https://www.cloudflare.com/ips-v4> and <https://www.cloudflare.com/ips-v6>, so the origin cannot be reached (or `CF-Connecting-IP` spoofed) directly. For example with `ufw` (keep 22 open; re-run when Cloudflare updates its list):

```bash
for ip in $(curl -fsS https://www.cloudflare.com/ips-v4) $(curl -fsS https://www.cloudflare.com/ips-v6); do
  sudo ufw allow proto tcp from "$ip" to any port 80,443
done
sudo ufw delete allow 80/tcp; sudo ufw delete allow 443/tcp   # drop any open-to-all 80/443 rules
```

Before serving proxied traffic, create a root-owned `/etc/nginx/dating-coach-cloudflare/real-ip.conf` with `real_ip_header CF-Connecting-IP;` and one `set_real_ip_from <CIDR>;` per published Cloudflare IPv4 and IPv6 range above. Keep the ranges current, then run `sudo nginx -t && sudo systemctl reload nginx`. Nginx only uses the header from those peers, so direct HTTPS still forwards the actual client IP if the optional file is absent. Block direct public access to the Compose-published backend ports at the provider firewall as well; Docker-published ports can bypass UFW.

WebSockets and the same-origin model are unaffected: the browser-facing origin stays `https://<domain>`, so `WEB_API_URL`, `CORS_ORIGINS` and `PUBLIC_APP_URL` are unchanged.

## Deployment flow (CI/CD)

`.github/workflows/release.yml` turns a merge to `main` into a production deploy on Google Cloud Run: tests, then images pushed to Docker Hub tagged with the commit SHA (`latest` only for the tip of `main`), then a migrations job and one Cloud Run service each for the API, worker, ML analyzer and web bundle. Postgres is on Neon, Redis on Upstash, and the worker is driven by Pub/Sub push and Cloud Scheduler instead of idling. Setup, required accounts, GitHub variables and rollback: [docs/deploy-cloud-run.md](docs/deploy-cloud-run.md).

The nginx and HTTPS sections above still apply to a self-hosted compose stack.

## Backups

Postgres is the only stateful service worth backing up (Redis holds rate-limit counters and the token denylist, RabbitMQ only in-flight jobs). `scripts/backup.sh` and `scripts/restore.sh` work against the running compose stack and read `POSTGRES_USER` / `POSTGRES_DB` and the `BACKUP_*` settings from `.env`.

**Schedule.** Daily, via cron on the VM as the deploy user (must be able to run `docker` without sudo), from the compose checkout:

```bash
crontab -e
# 03:15 UTC every day; stdout/stderr appended to a log so a failing night is visible
15 3 * * * $HOME/dating-coach/scripts/backup.sh >> $HOME/dating-coach-backup.log 2>&1
```

Each run does `docker compose exec -T postgres pg_dump --clean --if-exists -U $POSTGRES_USER $POSTGRES_DB | gzip` into `BACKUP_DIR/<db>-<UTC timestamp>.sql.gz` (default `<repo>/backups/`, git-ignored; `/var/backups/dating-coach` is a sensible production value), then deletes local dumps older than `BACKUP_RETENTION_DAYS` (default 14, i.e. the last 14 daily dumps stay). Each dump is a consistent snapshot as of the moment `pg_dump` starts — that timestamp is the recovery point, and anything committed after it is only in the next day's dump — so pick the schedule by how much data you can afford to lose.

**Off-box storage.** Set `BACKUP_S3_URI=s3://<bucket>/dating-coach` plus `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` (and `BACKUP_S3_ENDPOINT` for Backblaze B2, Cloudflare R2, Scaleway, MinIO, …) in `.env`, and install the AWS CLI on the VM (`sudo apt-get install -y awscli` or the official installer). Every dump is then also `aws s3 cp`'d to the bucket; a failed upload fails the run but leaves the local file. The script never prunes the bucket — set a lifecycle rule there (e.g. expire after 30–90 days) and use a write-only key so a compromised VM cannot delete history. As a second layer, enable your provider's scheduled VM/volume snapshots: the Postgres data lives in the `postgres-data` Docker volume (`/var/lib/docker/volumes/dating-coach_postgres-data`), and a snapshot of the VM disk is a whole-machine fallback that also covers `.env` and the nginx/TLS setup.

**Restore.**

```bash
scripts/restore.sh backups/datingcoach-20260922T031500Z.sql.gz          # local file
scripts/restore.sh s3://<bucket>/dating-coach/datingcoach-20260922T031500Z.sql.gz   # fetched with the same AWS_* / BACKUP_S3_ENDPOINT
```

It asks for confirmation (pass `--yes` to skip), stops `api` and `worker`, replays the dump into the running `postgres` container inside a single transaction after recreating the `public` schema (so the database ends up exactly as dumped, `schema_migrations` included; a corrupt dump rolls back and changes nothing), re-runs `migrate` so a dump taken before the current release's migrations is brought up to the schema the checked-out code expects, then starts `api` and `worker` again. If `migrate` fails they stay stopped rather than running against a schema they do not understand.

**Rollback caveat.** The `migrate` service only runs `up`. Redeploying an older image tag does not undo a schema change, so to roll back across a *breaking* migration: restore a dump taken before that migration with `--no-start` (skips `migrate` and leaves `api`/`worker` stopped, so the current code never touches the old schema), then trigger the release workflow with the older `image_tag` — the older checkout's `migrate` only applies the migrations it knows about, which the restored `schema_migrations` already records, and its `api`/`worker` come up on the schema they were built for. For additive/backward-compatible migrations no restore is needed.

## Running components without Compose

**Backend** (needs PostgreSQL, Redis and RabbitMQ reachable):

First copy `.env.example` to `.env` in the repository root for local development.
The Go binaries load that file as well; without it, supply `APP_ENV=development`
explicitly for local example secrets, or provide valid non-development secrets.

```bash
cd backend
migrate -path migrations -database "$DATABASE_URL" up   # golang-migrate
go run ./cmd/api        # HTTP + WebSocket API on :8080
go run ./cmd/worker     # analysis + account-deletion worker
sqlc generate           # after editing internal/store/queries/*.sql
go build ./... && go vet ./...
go test ./...           # database-backed tests skip themselves
TEST_DATABASE_URL="$DATABASE_URL" go test ./...   # …and run with this set
```

The tests gated on `TEST_DATABASE_URL` talk to a real PostgreSQL and delete the
rows they create; point it at a scratch database, never a production one.

**ML analyzer:**

```bash
cd ml-analyzer
python -m venv .venv && . .venv/bin/activate
pip install -r requirements.txt
uvicorn app.main:app --reload --port 8000
```

Without `LLM_API_KEY` both tracks fall back to dependency-free heuristic scorers, so the whole stack works offline. See <ml-analyzer/README.md> for the two contracts, the training path, the labelled-data schema and how to switch `/analyze` to a fine-tuned model behind the same contract.

### The two analysis tracks

- **Messages** (`POST /analysis/conversations`, async via the worker). Only the customer's own messages are evaluated; the match's replies are evidence of how each one landed (no reply, short reply, engaged reply). The analyzer hints at what to reconsider and never drafts a message — the customer always writes their own.
- **Photos** (`POST /analysis/images`, synchronous). 1–10 photos as URLs or raw base64; each is scored for clarity and for whether the customer is the clear focal point, with tailored feedback. Photos are not stored.

Both are tailored to `users.dating_preferences`: free text the customer edits on the Account screen (`PATCH /auth/me`), which the backend attaches to every ML request — the worker reads it for messages, the API reads it for photos. The app never sends preferences with the analysis request itself, so they cannot go stale.

**Expo app checks:**

```bash
cd app
npm run typecheck                 # tsc --noEmit
npx expo export --platform web    # production web bundle
```

## API surface

| Area     | Endpoints                                                                                                                                                              |
| -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Auth     | `POST /api/v1/auth/signup` · `signin` · `refresh` · `verify` · `resend-verification` · `GET /me` · `PATCH /me` (`dating_preferences`, `coaching_goals`) · `DELETE /me` (rate limited per IP) |
| Coaching | `GET /coaching/coaches` · `/coaches/{id}` · `/coaches/{id}/availability` · `/coaches/{id}/slots` · `POST /coaching/sessions` · `.../cancel` · `.../reschedule`           |
| Coach    | `PUT /coach/profile` · `PUT /coach/availability` · `GET /coach/sessions` · `POST /coach/sessions/{id}/status` · `.../notes` · `GET /chat/coach/threads`                  |
| Chat     | `POST /chat/threads` · `GET /chat/threads` · `GET/POST /chat/threads/{id}/messages` · `POST /chat/threads/{id}/close` · `GET /chat/threads/{id}/ws`                      |
| Analysis | `POST /analysis/conversations` · `GET /analysis/conversations` · `GET /analysis/conversations/{id}/result` · `POST /analysis/conversations/{id}/reanalyze` · `POST /analysis/conversations/{id}/label` · `GET /analysis/results/{id}` · `POST /analysis/images` |

All endpoints except the auth ones and `POST /api/v1/booking/respond` require `Authorization: Bearer <jwt>`; the WebSocket accepts `?token=<jwt>`. `POST /booking/respond` (`{session_id, token, action}`, the coach's emailed confirm/decline link) authenticates by its single-use token, is rate limited per IP and per session (`AUTH_RATE_LIMIT` per `AUTH_RATE_WINDOW`), and answers every unusable token with the same 404.

## Configuration

Every variable lives in `.env.example`, grouped per component: Postgres/Redis/RabbitMQ URLs, `JWT_SECRET`, bcrypt cost, auth rate limits, `ML_SERVICE_URL`, SMTP credentials, CORS origins, the ML backend selector (`llm` / `heuristic` / `trained`) with LLM provider settings, and `EXPO_PUBLIC_API_URL` for the app.

## Training data

Conversations submitted for analysis are stored, and clients can attach an outcome label (ghosted / kept talking / numbers / date set) with explicit consent. Those consented rows are what `ml-analyzer/train.py` consumes to replace the prompt-based v1 — see the schema in `ml-analyzer/training/data_schema.md`.
