# Dating Coach

A dating-coach platform in three independent components:

| Path           | Stack                                   | Responsibility                                                       |
| -------------- | --------------------------------------- | -------------------------------------------------------------------- |
| `backend/`     | Go 1.25, chi, pgx + sqlc, Redis, RabbitMQ | REST + WebSocket API, auth, coaching, live chat, analysis worker      |
| `app/`         | Expo SDK 57, TypeScript, react-native-web | One codebase for iOS, Android and web                                |
| `ml-analyzer/` | Python 3.12, FastAPI                      | `/analyze` engagement scoring (LLM prompt v1, trained model later)   |

Two product surfaces:

1. **Human coaching** — clients browse coaches, book a session against real availability, or open a live chat. Coaches (users with role `coach`) get a dashboard of upcoming sessions and active chats.
2. **Conversation analysis** — a client pastes a dating-app conversation and gets per-stretch engagement scores plus overall feedback, produced by the ML service.

## Architecture

```
Expo app (iOS / Android / web)
   │  REST  /api/v1/...            WebSocket  /api/v1/chat/threads/{id}/ws
   ▼
Go API ──── PostgreSQL (users, coaches, sessions, chat, conversations, analysis_results)
   ├─────── Redis        (auth rate limits, chat pub/sub, presence, typing)
   └─────── RabbitMQ ──► Go worker ──HTTP──► ml-analyzer /analyze
                              │
                              └─ writes analysis_results + "analysis ready" notification
```

- Analysis is asynchronous: `POST /api/v1/analysis/conversations` stores the transcript, creates a `pending` result and publishes a job. The worker calls the ML service, stores per-segment scores as JSONB and notifies the user. The app polls the result endpoint.
- Live chat messages are persisted in PostgreSQL and fanned out over Redis pub/sub, so any API replica can serve a socket.
- Sessions are a short-lived access JWT (15m, verified by signature alone, so no per-request database work) paired with an opaque refresh token stored only as a SHA-256 hash in `refresh_tokens`. `POST /api/v1/auth/refresh` exchanges and rotates it, so a stolen refresh token is usable at most once. Each rotation stays in the token's `family_id`, and presenting an already-spent token revokes that whole family — a replay ends the session for both the thief and the legitimate client, without touching the user's other devices.
- Account deletion is two-phase: `DELETE /api/v1/auth/me` stamps `users.deleted_at` and revokes the account's refresh tokens before it answers, so sign-in refuses the account like an unknown one, no further access token can be minted and the one the caller holds dies within 15 minutes, then queues the row removal (cascading across every table) to the worker. A failed attempt waits 30s on `account.deletion.retry` before it is redelivered, and deletions that keep failing land on `account.deletion.dlq`. The worker logs that queue's depth every `DEAD_LETTER_ALERT_PERIOD`; alert on a non-zero depth, because those accounts can no longer sign in but still hold rows.
- The ML service is fully decoupled — HTTP only, no shared database.

## Quick start (Docker Compose)

```bash
cp .env.example .env      # set JWT_SECRET, and LLM_API_KEY for real LLM scoring
docker compose up --build # postgres, redis, rabbitmq, migrations, api, worker, ml-analyzer
```

- API: http://localhost:8080/healthz
- ML service: http://localhost:8000/healthz (and `/docs`)
- RabbitMQ UI: http://localhost:15672 (guest / guest)

Migrations run in a one-shot `migrate` service before `api` and `worker` start.

The Expo app is not containerised — run it on the host:

```bash
cd app
npm install
npx expo start --web          # web target (react-native-web)
npx expo start                # then press i / a for iOS / Android
```

Point the app at the backend with `EXPO_PUBLIC_API_URL` (defaults to `http://localhost:8080`, and `http://10.0.2.2:8080` on the Android emulator).

Sign-up returns a short-lived `verify`-scoped token that only reaches `/api/v1/auth`; every other endpoint answers 403 until the email is confirmed, at which point verification hands back a full `session`-scoped token. Sign-in refuses accounts whose address is unconfirmed (403), and the app sends those users to the verify screen. With no SMTP credentials configured the verification email is written to the API log instead of being sent, so grab the token locally with:

```bash
docker compose logs api | grep -i verification
```

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

Without `LLM_API_KEY` the service falls back to a dependency-free heuristic scorer, so the whole stack works offline. See <ml-analyzer/README.md> for the training path, the labelled-data schema and how to switch `/analyze` to a fine-tuned model behind the same contract.

**Expo app checks:**

```bash
cd app
npm run typecheck                 # tsc --noEmit
npx expo export --platform web    # production web bundle
```

## API surface

| Area     | Endpoints                                                                                                                                                              |
| -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Auth     | `POST /api/v1/auth/signup` · `signin` · `verify` · `resend-verification` · `GET /me` · `DELETE /me` (rate limited per IP)                                               |
| Coaching | `GET /coaching/coaches` · `/coaches/{id}` · `/coaches/{id}/availability` · `/coaches/{id}/slots` · `POST /coaching/sessions` · `.../cancel` · `.../reschedule`           |
| Coach    | `PUT /coach/profile` · `PUT /coach/availability` · `GET /coach/sessions` · `POST /coach/sessions/{id}/status` · `.../notes` · `GET /chat/coach/threads`                  |
| Chat     | `POST /chat/threads` · `GET /chat/threads` · `GET/POST /chat/threads/{id}/messages` · `POST /chat/threads/{id}/close` · `GET /chat/threads/{id}/ws`                      |
| Analysis | `POST /analysis/conversations` · `GET /analysis/conversations` · `GET /analysis/conversations/{id}/result` · `POST /analysis/conversations/{id}/label` · `GET /analysis/results/{id}` |

All endpoints except the auth ones require `Authorization: Bearer <jwt>`; the WebSocket accepts `?token=<jwt>`.

## Configuration

Every variable lives in `.env.example`, grouped per component: Postgres/Redis/RabbitMQ URLs, `JWT_SECRET`, bcrypt cost, auth rate limits, `ML_SERVICE_URL`, SMTP credentials, CORS origins, the ML backend selector (`llm` / `heuristic` / `trained`) with LLM provider settings, and `EXPO_PUBLIC_API_URL` for the app.

## Training data

Conversations submitted for analysis are stored, and clients can attach an outcome label (ghosted / kept talking / numbers / date set) with explicit consent. Those consented rows are what `ml-analyzer/train.py` consumes to replace the prompt-based v1 — see the schema in `ml-analyzer/training/data_schema.md`.
