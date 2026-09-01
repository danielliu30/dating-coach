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
- No SMTP: grab the token from the API log. Newer revisions log a deep link — opening
  `http://localhost:8081/verify?token=<64hex>` auto-submits, no typing needed:
```bash
docker compose logs api | grep -i verification       # token=<64 hex chars> / full /verify?token=... link
```
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
- **Forcing a server-side (500) delete failure**: `docker compose stop rabbitmq` — the publish of the
  deletion job fails and the API answers `500 {"error":"could not delete account"}` (transport-level
  failure via `docker compose stop api` gives a fetch error instead, which is a different code path).
  Note that only a 401 clears the client session, so a 500/fetch failure must leave the user signed in.
  After `docker compose start rabbitmq`, wait for `docker compose ps` to report rabbitmq `healthy`, and
  expect the worker to need up to ~30 s of consumer-retry backoff before it drains the queue.
  `DELETE /auth/me` is deliberately denylist-exempt, so retrying with the same (already revoked) token
  works — but any `active`-gated call such as "Refresh profile" (`GET /auth/me`) will 401 and auto sign
  the user out after a failed attempt, so assert the sanitized error before touching other buttons.

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
