# End-to-end suite

Browser-driven tests against the **real** stack: Chromium → nginx → Go API, worker,
RabbitMQ, Redis, Postgres and the FastAPI ml-analyzer. Nothing is mocked. Assertions
check the UI *and* Postgres (`docker compose exec postgres psql …`).

This is separate from `app/` unit/component tests, which mock the API.

## Run locally

```bash
# from the repo root — builds the web bundle + nginx (gateway profile),
# heuristic ML scorer, no LLM key needed
docker compose --env-file e2e/.env --profile gateway up -d --build

cd e2e
npm ci
npx playwright install chromium
npx playwright test               # whole suite
npx playwright test tests/happy-path.spec.ts
npx playwright show-report        # after a failure: traces, video, screenshots

npm run stack:down                # docker compose … down -v (stack:up also exists)
```

`e2e/.env` is derived from `.env.example` with `ML_BACKEND=heuristic`, a fast bcrypt
cost, a relaxed auth rate limit and same-origin web config. Always pass
`--env-file e2e/.env` so compose does not pick up a root `.env`. Nothing in it is a
real secret.

The global setup (`global-setup.ts`) polls `GET /healthz` and `GET /ml/healthz`
through nginx, checks every gateway-profile service is running and that
`schema_migrations` is clean at version ≥ 7.

## Layout

| Path | Purpose |
| --- | --- |
| `helpers/stack.ts` | `compose()`, `db(sql)`, `stopService`/`startService`/`recreateService`, health polling |
| `helpers/verificationCode.ts` | scrapes `Your verification code is:` from `docker compose logs api`, matched to the account email |
| `helpers/signUpAndVerify.ts` | drives Sign Up → Verify (role labels "Someone dating"/"A coach") and returns the page, user id and JWT |
| `helpers/api.ts` | thin `fetch` wrapper for the few steps the spec allows to skip the UI (coach confirm, slot listing) |
| `helpers/ui.ts` | locators for icon-prefixed buttons/tabs, slot chips, sign-out assertions |
| `tests/happy-path.spec.ts` | signup → coach profile/availability → booking → confirm → live chat → analysis |
| `tests/booking.spec.ts` | 409 race, out-of-availability rejection, reschedule via `exclude_session_id`, `respond_by` expiry sweep |
| `tests/resilience.spec.ts` | chat send while API is stopped replays exactly once; analysis "Try again" after worker+API outage |
| `tests/session.spec.ts` | corrupt/expired `dating-coach.session`, refresh rotation, `JWT_TTL=30s` expiry notices, explicit Sign out |
| `tests/deletion.spec.ts` | type-to-confirm deletion (202, worker removes rows, control account untouched) incl. rabbitmq-down relay |

Each role gets its own Playwright `BrowserContext` so JWTs in `localStorage` never clash.
Tests run with one worker; specs that stop/restart services restore them in `afterEach`,
and `session.spec.ts` recreates the `api` container with short TTLs then puts it back.

## CI

`.github/workflows/e2e.yml` runs the suite on PRs and pushes to `main` that touch
`app/`, `backend/`, `ml-analyzer/`, `nginx/`, `docker-compose.yml`, `e2e/` or the
workflow. It uploads the HTML report + traces, and compose logs on failure.

## Adding a test

Prefer `getByRole`/`getByText` on real labels; buttons and tabs carry an Ionicons glyph
in their accessible name, so use `button(page, 'Label')` / `openTab(page, 'Coaches')`.
Use `makeAccount(prefix, role)` for unique accounts, `dbOne`/`dbCount`/`waitForDb` for
DB assertions, and never commit `playwright-report/` or `test-results/`.
