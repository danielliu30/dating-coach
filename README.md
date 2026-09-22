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
- Live chat messages are persisted in PostgreSQL and fanned out over Redis pub/sub, so any API replica can serve a socket.
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

Migrations run in a one-shot `migrate` service before `api` and `worker` start.

Prebuilt images are published to Docker Hub as `danielliu30/dating-coach-backend` (both `api` and `worker` entrypoints) and `danielliu30/dating-coach-ml-analyzer`. To run from them instead of building:

```bash
docker compose pull api ml-analyzer && docker compose up -d --no-build
```

Images are published automatically by CI on every merge to `main` (see [Deployment flow (CI/CD)](#deployment-flow-cicd)); to publish by hand: `docker compose --profile gateway build && docker compose --profile gateway push api ml-analyzer web` (override the target with `DOCKERHUB_NAMESPACE` / `IMAGE_TAG`).

### Exposing the stack through nginx

`nginx/` holds one route table in two flavours — `/api/*` and `/healthz` go to the API (WebSocket upgrades included), `/ml/*` goes to the ML service, and everything else goes to the `web` image (the exported Expo web bundle built by `app/Dockerfile`, with an SPA fallback):

- Containerised: `docker compose --profile gateway up -d` adds the `web` and `nginx` services; open `http://localhost:${NGINX_PORT:-80}/`. If you change `NGINX_PORT`, set `PUBLIC_APP_URL` (and `CORS_ORIGINS`) to that origin as well.
- Host-installed nginx: install `nginx/host-site.conf` as a site (instructions in the file); it proxies to the ports compose publishes on `localhost` (`web` on `${WEB_PORT:-3000}`; the file hardcodes 3000, edit it if you change `WEB_PORT`).

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

## Deployment flow (CI/CD)

`.github/workflows/release.yml` turns a merge to `main` into a production deploy:

```
push to main
  ├─ backend.yml ─┐
  ├─ app.yml      ├─ (reusable workflow_call jobs; any failure stops the release)
  ├─ ml-analyzer.yml
  └─ e2e.yml     ─┘
        └─ build-push: docker compose --profile gateway build/push api ml-analyzer web
              tags: <git sha> and latest  (worker reuses the backend image)
              └─ deploy: ssh to the VM → pin IMAGE_TAG in .env → compose pull → compose up -d → curl /healthz
```

**Tagging convention.** Every image (`dating-coach-backend`, `-ml-analyzer`, `-web`) is pushed as both `latest` and the full commit SHA. The VM always runs a SHA tag: the deploy step writes `IMAGE_TAG=<sha>` into the production `.env`, so a later manual `docker compose up -d` on the VM keeps the same images instead of drifting to `latest`.

**Required GitHub repository secrets**

| Secret | Used by | Value |
|---|---|---|
| `DOCKERHUB_USERNAME` | build-push, deploy | Docker Hub account / namespace (`danielliu30`) |
| `DOCKERHUB_TOKEN` | build-push | Docker Hub access token with Read & Write |
| `DEPLOY_HOST` | deploy | VM hostname or IP |
| `DEPLOY_USER` | deploy | SSH user on the VM (must be in the `docker` group) |
| `DEPLOY_SSH_KEY` | deploy | Private key whose public half is in that user's `~/.ssh/authorized_keys` |

Optional repository **variables**: `GOOGLE_CLIENT_ID` (baked into the web bundle), `DEPLOY_DIR` (default `~/dating-coach`), `DEPLOY_HEALTHCHECK_URL` (default `http://localhost/healthz`; use `https://<domain>/healthz` once TLS is in front).

**Rolling back.** Actions → *Release (build, push, deploy)* → *Run workflow* and set `image_tag` to an earlier commit SHA (any tag listed on Docker Hub). Tests and the build are skipped and only the deploy job runs, so the VM is back on the previous images in about a minute. The same input can re-deploy the current tag after fixing the VM.

**One-time VM prerequisites**

1. Docker Engine + the Compose plugin installed; the deploy user can run `docker` without sudo.
2. `git clone` of this repo at `DEPLOY_DIR` (the deploy only needs `docker-compose.yml` and `nginx/default.conf`, but a checkout makes `git pull` for compose/nginx changes easy).
3. A production `.env` in that directory (start from `.env.example`; set `APP_ENV`, `JWT_SECRET`, `POSTGRES_PASSWORD`, `PUBLIC_APP_URL`, `CORS_ORIGINS`, SMTP, Stripe, `GOOGLE_CLIENT_ID`). The deploy step appends/replaces only `IMAGE_TAG` and `DOCKERHUB_NAMESPACE`.
4. Ports 80 (and 443 if you terminate TLS on the host) open; if a host nginx fronts the stack, move `NGINX_PORT` off 80 as described above.
5. A deploy key: `ssh-keygen -t ed25519 -f deploy_key -N ''`, append `deploy_key.pub` to `~/.ssh/authorized_keys`, store the private half as `DEPLOY_SSH_KEY`.

A first `docker compose --profile gateway up -d` by hand is a good smoke test before wiring the secrets; after that every merge to `main` deploys itself, and a failing `/healthz` marks the run red in Actions.

## Running components without Compose

**Backend** (needs PostgreSQL, Redis and RabbitMQ reachable):

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
| Auth     | `POST /api/v1/auth/signup` · `signin` · `refresh` · `verify` · `resend-verification` · `GET /me` · `PATCH /me` (`dating_preferences`) · `DELETE /me` (rate limited per IP) |
| Coaching | `GET /coaching/coaches` · `/coaches/{id}` · `/coaches/{id}/availability` · `/coaches/{id}/slots` · `POST /coaching/sessions` · `.../cancel` · `.../reschedule`           |
| Coach    | `PUT /coach/profile` · `PUT /coach/availability` · `GET /coach/sessions` · `POST /coach/sessions/{id}/status` · `.../notes` · `GET /chat/coach/threads`                  |
| Chat     | `POST /chat/threads` · `GET /chat/threads` · `GET/POST /chat/threads/{id}/messages` · `POST /chat/threads/{id}/close` · `GET /chat/threads/{id}/ws`                      |
| Analysis | `POST /analysis/conversations` · `GET /analysis/conversations` · `GET /analysis/conversations/{id}/result` · `POST /analysis/conversations/{id}/reanalyze` · `POST /analysis/conversations/{id}/label` · `GET /analysis/results/{id}` · `POST /analysis/images` |

All endpoints except the auth ones require `Authorization: Bearer <jwt>`; the WebSocket accepts `?token=<jwt>`.

## Configuration

Every variable lives in `.env.example`, grouped per component: Postgres/Redis/RabbitMQ URLs, `JWT_SECRET`, bcrypt cost, auth rate limits, `ML_SERVICE_URL`, SMTP credentials, CORS origins, the ML backend selector (`llm` / `heuristic` / `trained`) with LLM provider settings, and `EXPO_PUBLIC_API_URL` for the app.

## Training data

Conversations submitted for analysis are stored, and clients can attach an outcome label (ghosted / kept talking / numbers / date set) with explicit consent. Those consented rows are what `ml-analyzer/train.py` consumes to replace the prompt-based v1 — see the schema in `ml-analyzer/training/data_schema.md`.
