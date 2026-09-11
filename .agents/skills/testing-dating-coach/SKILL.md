---
name: testing-dating-coach
description: How to bring up and end-to-end test the dating-coach monorepo (Go chi API, RabbitMQ worker, FastAPI ml-analyzer, Expo SDK 57 web app) locally in a browser.
---

# End-to-end testing the dating-coach stack

## Bring up the stack
```bash
cd <repo>
cp .env.example .env            # only if .env missing
docker compose up -d --build    # postgres, redis, rabbitmq, migrate, api:8080, worker, ml-analyzer:8000
docker compose ps               # verify: api, worker, ml-analyzer, postgres, rabbitmq, redis
cd app && npm install && npx expo start --web   # web on :8081
```
- Node v20.18.1 triggers an Expo "outdated Node" warning; it is harmless.
- Health: there is no `/health`; ml-analyzer exposes `GET :8000/healthz` (`{"model_version":"heuristic-v1"}`). For the API use `docker compose logs api` plus a real request.
- Restarting Expo web: `pkill -f "expo start"` then a plain `nohup … &` dies silently. Use:
  `cd app && setsid npx expo start --web --port 8081 > /tmp/expo.log 2>&1 &` and poll `curl -s -o /dev/null -w '%{http_code}' localhost:8081`.
- Signup roles in the API are `user` and `coach` (the UI labels them "Someone dating" / "A coach"); API routes are under `/api/v1`.
- No LLM_API_KEY needed: ml-analyzer falls back to the dependency-free heuristic scorer (`model: heuristic-v1`). Do not configure an LLM.

## Accounts and the verification gate
- Role (client/coach) is chosen at sign-up and cannot be changed later; you need one of each.
- Sign-up returns a live session but the app holds you on the Verify screen until `email_verified`.
- No SMTP: read the six-digit verification code from the API log and enter it on the Verify
  screen. Match the log entry to the account email when testing multiple roles. Codes expire
  after 3 minutes and are discarded after 3 wrong attempts; use Resend if needed.
```bash
docker compose logs api --since 5m | grep -A 5 -B 3 'Your verification code is:'
```
- Older revisions used `/verify?token=<64hex>` deep links; inspect the current log and Verify
  screen rather than assuming that older token format is still accepted.
- A coach is invisible to clients (and unchattable) until a coach profile + availability is saved from the coach Profile tab.

## Direct DB inspection
Credentials come from `.env` (`POSTGRES_USER/DB=datingcoach`), NOT `postgres`:
```bash
docker compose exec -T postgres psql -U datingcoach -d datingcoach -c "\dt"
```
Useful tables: `coaches` (PK is `user_id`, rate stored in `hourly_rate_cents`), `coach_availability`
(`start_minute`/`end_minute` from midnight), `coaching_sessions`, `chat_messages`, `analysis_results`,
`training_examples` (outcome label rows, `outcome='number_exchanged'`, `label_source='user'`).

## Two-role browser testing
Use two windows: a normal Chrome window for one role and an Incognito window for the other, so the two
JWTs do not clash in localStorage. For chat, open the client chat via Coaches → coach detail →
"Start a live chat" and the coach side via Dashboard → Active chats → "Open chat"; both banners should
read "Connected" and messages should appear without reload.

## Responsive nav
`RootNavigator` switches `tabBarPosition` to top when width >= 900 css px, bottom otherwise. Resize the
real window (do not use devtools device emulation):
```bash
wmctrl -l                                     # find the window id
wmctrl -i -r <id> -b add,maximized_vert,maximized_horz   # wide
wmctrl -i -r <id> -b remove,maximized_vert,maximized_horz && wmctrl -i -r <id> -e 0,20,20,430,740  # narrow
```
Top tab labels may be truncated ("Coac…", "Analy…") at ~1024px — cosmetic, worth flagging.

## Recipes for adversarial scenarios (all reproducible through the UI)
- **Booking 409 ("slot is no longer available")**: fresh slot lists already hide taken/overlapping
  times, so force a race — open the coach detail in two tabs of the same browser profile, select the
  same chip in tab A, book it in tab B, then press "Book session" in tab A.
- **Out-of-availability rejection**: leave a slot list loaded in the client tab, remove that
  availability window from the coach Profile tab and save, then book the now-stale chip. Expect
  "coach is not available then: … is outside the coach's published availability".
- **Reschedule into the session's own interval**: client Sessions → "Reschedule" issues
  `GET /coaching/coaches/{id}/slots?...&exclude_session_id=<session>`, so the session's own time is
  offered and rescheduling onto it returns 200 (no 409). The reschedule chip list appears capped at
  ~8 slots, so a desired later time may not be reachable there.
- **Coach confirmation of bookings** (migration `000005_session_confirmation`): a new booking is
  `pending` and still holds the slot (a second client booking the same time gets 409). The coach is
  emailed confirm/decline links; without SMTP the worker logs the email instead, so read it from
  `select to_email, subject, body from email_outbox order by created_at desc limit 3` (the token is only
  in the link, its sha256 is in `coaching_sessions.confirmation_token`). Answer with
  `POST /api/v1/booking/respond {session_id, token, action: confirm|decline}` (no bearer) or
  `POST /api/v1/coach/sessions/{id}/respond {action}` as the coach. Deadline is `respond_by`
  (24h, capped at 2h before start); to see expiry, `update coaching_sessions set respond_by = now() -
  interval '1m' where id = …` and wait for the worker sweep (≤1 min) → status `expired`, slot free,
  client emailed. A client-initiated reschedule drops a `scheduled` session back to `pending` with a
  fresh token; a coach-initiated one keeps it `scheduled`. Statuses: `pending, scheduled, declined,
  expired, completed, cancelled, no_show`.
- **Chat offline queue/replay**: `docker compose stop api` (banner → "Reconnecting…"), send a message
  (nothing renders while offline), `docker compose start api`; the message replays once — verify with
  `select count(*) from chat_messages where body='…'` = 1.
- **Analysis "Try again" path**: analysis finishes in <2 s, so to see the pending/error state do
  `docker compose stop worker` first, submit, then `docker compose stop api` while the result screen
  polls; restart both and click "Try again".
- **Persisted-session recovery**: storage key is `dating-coach.session`. The browser_console tool may
  attach to the wrong tab — open real DevTools (F12) in the window under test and run
  `localStorage.setItem('dating-coach.session','{{{not json')` or patch `expires_at` to a past date,
  then reload; the app must land on Sign In (not a stuck splash).
- **Closed thread**: no UI closes a thread; do
  `update chat_threads set status='closed' where id='…'` and then send from the client chat.
- **Account deletion from the UI** (Account tab → "Delete account" card): the confirm button stays
  disabled until the phrase `delete` is typed (trimmed + lowercased, so `  DELETE  ` also arms it).
  Deletion is `DELETE /api/v1/auth/me` → 202, and the row disappears only after the worker consumes the
  job, so poll `select count(*) from users where email='…'` for ~15–60 s instead of asserting instantly.
  Always keep a second verified control account so you can prove the deletion was targeted.
- **Deletion with the broker down (outbox + relay)**: since the deletion-outbox change, stopping
  RabbitMQ no longer fails the request. `DELETE /auth/me` marks `users.deleted_at` and inserts
  `account_deletions` in one statement, then best-effort revokes refresh tokens and publishes; a
  publish failure only logs `"leaving a recorded deletion to the relay"` and the API still answers
  **202**. To test: `docker compose stop rabbitmq`, delete from the UI, then assert in psql that
  `deleted_at IS NOT NULL`, the `account_deletions` row exists with `published_at IS NULL`, the `users`
  row still exists, and sign-in is already `401` (all user queries filter `deleted_at IS NULL`).
  `docker compose start rabbitmq` and wait ≤60 s: the worker relay (5 s ticker) logs
  `"queued a recorded account deletion"` then `"account deleted"`, and users/refresh_tokens/
  account_deletions rows all disappear (refresh tokens go by FK cascade). Expect ~30 s of consumer
  retry-backoff noise (`consumer stopped … retry_in 30s`) in the worker log before the job is drained —
  that is normal, not a failure.

## Auth: short-lived access tokens + refresh tokens
- Access tokens are signature-verified JWTs (`JWT_TTL`, default 15m); refresh tokens live in
  `refresh_tokens` as sha256 hashes (`REFRESH_TOKEN_TTL`, default 720h). Migration order ends
  `000004_dating_profile`, `000005_session_confirmation`, `000006_refresh_tokens`, `000007_payments`; a fresh
  DB must reach `schema_migrations` version 7 (`docker compose down -v && docker compose up -d --build`). Two
  migrations sharing a version number makes `migrate` refuse the whole set (`duplicate migration file`)
  — always check `ls backend/migrations` after merging a branch that adds one.
- Make expiry observable: `JWT_TTL=30s docker compose up -d --build api` (rebuild/restart api only),
  then sign in, idle >35 s and press "Refresh profile". Expect `GET /auth/me 401` →
  `POST /auth/refresh 200` → `GET /auth/me 200`, no UI bounce, and in psql one revoked + one active
  refresh row (rotation). Restore with `docker compose up -d api` (back to the compose defaults).
- Replay/reuse: a rotated refresh token must 401 `invalid refresh token` on every reuse.
- **Session-ended notice levers** (`app/src/screens/SignInScreen.tsx` holds the copy):
  - "revoked" copy — *Your session was ended / This can happen if the account was deleted or signed out
    on another device.* Trigger with
    `delete from refresh_tokens where user_id=…` on a disposable local test user (or a real
    `DELETE /auth/me` from another client), then wait out the access token and trigger a request.
    Check the current auth reason mapping before asserting copy. The current table has
    `expires_at` and `used_at`, not `revoked_at`; inspect `\d refresh_tokens` if a recipe fails.
  - "expired" copy — *You were signed out / Your session expired for security…* Trigger at runtime with
    `JWT_TTL=30s REFRESH_TOKEN_TTL=40s docker compose up -d api`, sign in, idle ~60 s. For the
    startup path, expire the disposable user's DB refresh rows
    (`update refresh_tokens set expires_at=now()-interval '1 minute' where user_id=…`), patch
    `dating-coach.session` in localStorage to a past `expires_at`, and reload. Expiring only
    the stored access expiry normally refreshes successfully and does not show a Notice.
    Never print stored token values. Click the round Dismiss button and verify the banner
    disappears; sign in again afterward to restore a usable session.
  - Explicit "Sign out" must show **no** banner, and a successful self-delete from the Account screen
    must also land on a clean Sign In screen (auth.tsx clears `endedReason` in `clearSession`).
  - Regression watch: `client.ts` must call `onUnauthorized()` only once per renewal — `refresh()`
    passes the `'defer'` policy. If it fires twice, `refreshExpiresRef` is cleared before the reason is
    computed and every revoked session wrongly shows the "expired" copy.

## Dating profile (Account tab → "How you date" / "Phases of dating")
- Saves go through `PATCH /api/v1/auth/me`; the API log shows the request and psql
  `select dating_styles, phases_strong, phases_working_on from users where email='…'` shows the arrays.
- If "Save dating profile" shows "Could not save your dating profile" while the API log shows only an
  `OPTIONS /auth/me` and no PATCH, the browser CORS preflight was rejected: check `AllowedMethods` in
  `backend/cmd/api/main.go` includes every verb the web app uses (PATCH was missing once) and rebuild
  the api (`docker compose up -d --build api`). Any new HTTP verb needs the same care.
- "Refresh profile" must keep unsaved chip toggles when the server lists are unchanged; test by
  toggling a chip, pressing Refresh, and asserting the chip stays selected with Save still enabled.

## Known/likely rough edges to check rather than debug
- Sending into a closed thread is correctly rejected server-side (nothing persisted) but the client
  send is fire-and-forget (`ChatScreen.tsx`: "sending is fire-and-forget"), so the message silently
  disappears with no error banner. Verify with a DB count, not the UI.
- Top tab labels may be truncated ("Coac…", "Analy…") at ~1024px — cosmetic, worth flagging.
- Transport-level failures surface the raw browser message "Failed to fetch" in the UI. Exception: the
  Account-screen delete failure is deliberately sanitized to "Could not delete your account. Please try
  again." and the underlying error only appears via `console.warn('delete account', err)` — check the
  DevTools console (F12) for the real reason when a delete failure looks unexplained.
- Old leftover tabs from previous rounds can spam `GET /chat/threads/<id>/ws → 404` every 10 s in the
  API log; close them before reading logs.
- Historical (fixed as of 1499e3b, re-check if regressed): `no_show` rejected by the DB CHECK
  constraint (400); client session list omitting `coach_notes`; CoachProfileScreen showing sample
  defaults (rate 120 / 3 years / 17:00–21:00) instead of the saved profile; booking not validated
  against availability/overlap; reschedule having no UI control.

## Devin Secrets Needed
None — the whole stack runs offline with `.env.example` defaults.
