# Design: cookie sessions for web (BFF-style auth)

Status: proposal
Audience: engineering

## 1. Context

Web and native clients authenticate the same way today: the API mints a
self-contained JWT access token (15 min) plus a refresh token (30 d), the client
holds both in `AsyncStorage`, and every request carries
`Authorization: Bearer <jwt>`. REST middleware validates the signature and
expiry only — deliberately, so no route pays a database round trip
(`backend/cmd/api/main.go`, `newRouter`).

The consequence is an irrevocable-credential window. Deleting an account marks
`users.deleted_at` and revokes refresh tokens, which stops sign-in, rotation,
and — via the socket's account-row re-check — live connections. But a JWT
already issued keeps working on REST until it expires, because nothing on the
request path can contradict a valid signature. The Redis denylist exists only
to shorten that window for sockets; REST never reads it. The comments in
`Service.DeleteAccount` accept this explicitly: "the access token in the
caller's hand keeps working until it expires, which is minutes away."

A BFF changes what the browser holds. Instead of a bearer credential, the
browser holds a session cookie — an opaque lookup key into server-side state we
control. Revocation stops being "denylist a token already in the wild" and
becomes "delete a row," which can happen **in the same transaction as the
deletion mark**. That is the property this design is after: mark and revoke as
one atomic write, with no residual access-token window, for web clients.

## 2. Goals

- Deleting an account ends every web session **atomically with the mark** — no
  in-flight window, no dependence on Redis or the worker being up.
- A deleted (or marked-for-deletion) account cannot sign back in, by any method.
- Every device and tab the account was signed in on signs out on its next
  request — including the device that performed the deletion — and lands on the
  signed-out navigator state the app already has.
- The browser never possesses a credential that outlives server-side
  revocation. No JWT, no refresh token reaches web client storage.
- Native (iOS/Android) clients keep working on bearer JWTs, unchanged.

## 3. Non-goals

- Eliminating the JWT window for **native** clients. Their tokens are still
  self-contained; the existing mark + denylist + socket re-check architecture
  stays as-is for them.
- A separate BFF *service*. See Alternatives — the session layer lives in the
  existing Go API, which already owns the database.
- Changing what the ML analyzer or internal services see.

## 4. Design

### 4.1 The model: server-side sessions in Postgres

New table (migration `000015_sessions`):

```sql
CREATE TABLE sessions (
    id_hash      bytea PRIMARY KEY,            -- SHA-256 of the cookie value
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope        text NOT NULL,                -- 'session' | 'verify'
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,         -- absolute expiry
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id ON sessions(user_id);
```

Decisions baked into this shape:

- **Postgres, not Redis.** A session lookup is a primary-key read — sub-millisecond
  — and Postgres is what lets the delete join `RequestUserDeletion`'s
  transaction. Redis would reintroduce exactly the dual-write problem this
  design removes.
- **Hash at rest.** The cookie carries 256 bits of random (base64url); the table
  stores only its SHA-256. A read-only DB leak (backup, log, replica) does not
  hand out live sessions.
- **No refresh token for web.** The JWT/refresh split exists because JWTs are
  irrevocable — the access token is kept short so the damage window stays
  small. A session row *is* revocable, so it can simply live for the old
  refresh lifetime (30 d) with a sliding `last_seen_at` touch. One credential,
  one row, one delete.
- **`scope` replaces the verify-scoped JWT.** Sign-up creates a `verify`-scoped
  session (can reach `/auth/verify`, nothing private); successful verification
  rotates the session id *and* upgrades scope — see fixation note in §4.3.

### 4.2 Cookie

```
Set-Cookie: dc_session=<256-bit random>; HttpOnly; Secure; SameSite=Lax; Path=/api; Max-Age=2592000
```

- `Path=/api` scopes it to the API subtree — it does not ride on static-asset
  requests. The chat WebSocket (`/api/v1/chat/threads/:id/ws`) is under `/api`,
  so the upgrade request carries it.
- `SameSite=Lax` works because nginx already serves app and API same-origin
  (`/` → web, `/api/` → api). Production is first-party by deployment, not by
  CORS exception.
- `Secure` in production (TLS terminates at the host nginx per
  `nginx/host-site.conf`); a config flag downgrades it for plain-HTTP local dev.

### 4.3 Middleware: dual-auth

`auth.Middleware` tries, in order:

1. `Authorization: Bearer` → existing JWT path (native clients; unchanged).
2. `dc_session` cookie → `SELECT s.* FROM sessions s JOIN users u ON u.id = s.user_id
   WHERE s.id_hash = $1 AND s.expires_at > now() AND u.deleted_at IS NULL`.

Found → same `Principal` as today (`ExpiresAt` = `sessions.expires_at`).
Not found or expired → 401. DB error → 401 (fail closed — the row's standing
is unknown, matching `sessionStatus`'s reasoning for sockets).

The account join is what makes a marked account's cookie dead **the moment the
mark commits**, even if the session row hasn't been cleaned up yet — defense in
depth under the atomic delete in §4.5.

`RequireScope` is unchanged: cookie principals carry `scope` from the row.

**Fixation**: any transition that raises privilege — sign-in, verify-scope →
session-scope — issues a *new* session id and deletes the old row. No client
ever carries the same session id across a privilege change.

**Idle touch**: `last_seen_at` updates at most once per few minutes (compare
against the value already read) to avoid a write on every request.

### 4.4 Endpoints

| Endpoint | Change |
| --- | --- |
| `POST /auth/signup` | Same body/response, plus `Set-Cookie` (verify-scoped session). Token fields still returned for native. |
| `POST /auth/signin` | Same, `Set-Cookie` (session-scoped). |
| `POST /auth/google` | Same, `Set-Cookie`. |
| `POST /auth/verify` | Rotates session id, upgrades scope to `session`, `Set-Cookie`. |
| `POST /auth/signout` *(new)* | Deletes the session row for the presented cookie; `Set-Cookie` expired. Native callers may also hit it with Bearer to revoke their refresh token — a small bonus over today's client-side-only sign-out. |
| `POST /auth/refresh` | Unchanged — native only. Web sessions need no renewal call. |
| `GET /auth/me`, `DELETE /auth/me` | Unchanged — dual-auth middleware serves both. |

Every auth response keeps returning the token payload it does today. Web
clients ignore it; native clients keep using it. This keeps one response shape
and makes the client-side switch a platform choice, not an API version.

### 4.5 Deletion: mark and revoke in one statement

`RequestUserDeletion` already performs mark + outbox insert atomically. Extend
the CTE to also remove both server-side credential types:

```sql
-- name: RequestUserDeletion :execrows
WITH marked AS (
    UPDATE users
    SET deleted_at = COALESCE(deleted_at, now()),
        updated_at = now()
    WHERE id = $1
    RETURNING id
), sessions_gone AS (
    DELETE FROM sessions WHERE user_id IN (SELECT id FROM marked)
), refresh_gone AS (
    DELETE FROM refresh_tokens WHERE user_id IN (SELECT id FROM marked)
)
INSERT INTO account_deletions (user_id)
SELECT id FROM marked
ON CONFLICT (user_id) DO UPDATE
SET requested_at = now(),
    published_at = NULL;
```

One commit now does: mark, outbox record, **web session revocation**, and
**refresh-token revocation**. The standalone `RevokeUserRefreshTokens` call in
`Service.DeleteAccount` folds into the transaction and its best-effort logging
goes away — it is guaranteed now, not attempted.

What remains outside the transaction, still best-effort, still only a latency
optimisation:

- `denylist.Revoke` + pub/sub announce — speeds up socket termination for
  **native** JWT holders. Web has no JWT, so web revocation needs nothing here.
- `deletions.Publish` — the worker still cleans up the rows.

The worker's revoke-before-delete ordering is unchanged; it now only matters
for native tokens.

### 4.6 Sign-out and the "automatic redirect"

The client-side machinery for this already exists; the cookie model feeds the
same triggers:

- **Deleting device**: `deleteAccount()` calls `DELETE /auth/me` then
  `clearSession()` — already implemented
  (`app/src/state/auth.tsx`). Under cookie mode it additionally calls
  `POST /auth/signout` semantics are unnecessary — the server already deleted
  the row — but clearing local profile cache stays.
- **Other tabs / devices**: their next API call returns 401 →
  `api.onSessionRejected` → central session drop → `signedOutReason` set →
  navigator leaves the authenticated stack. With cookies the 401 is *immediate*
  rather than waiting for a refresh to be refused, because the credential dies
  server-side at mark time.
- **Chat socket**: closes with 1008 exactly as today — `sessionStatus` reads
  the account row — and the client's refused-renewal path signs out.
- **Sign back in**: `GetUserByEmail` already filters `deleted_at IS NULL` for
  both password and Google paths; unchanged.

### 4.7 Web client changes

- All `fetch`/`api` calls add `credentials: 'include'` (Expo web → `fetch`,
  supported).
- Stop persisting `token`/`refresh_token` on web; `AsyncStorage` keeps only the
  profile cache for cold-start rendering. The auth provider's token state stays
  — it just reports "signed in" from a successful `/auth/me` probe at boot
  instead of a stored token.
- `ChatSocket` drops the `?token=` param on web — the upgrade carries the
  cookie same-origin. Native keeps the param.
- `api.onSessionRejected`, `signedOutReason`, the renewal plumbing: reused
  as-is.

### 4.8 CSRF

Cookie auth reintroduces ambient-credential risk on mutations. Mitigations,
in order of what actually carries weight:

1. `SameSite=Lax` — cross-site POSTs don't send the cookie, period.
2. Custom-header check: cookie-authenticated mutating requests must carry
   `X-DC-Client: web` (or similar). Browsers cannot attach custom headers
   cross-origin without a CORS preflight, and `CORSOrigins` already whitelists
   exact origins with `AllowCredentials`.
3. `Content-Type: application/json` bodies — already true; a form POST can't
   produce it without preflight either.

No token-in-cookie double-submit dance needed; 1+2 is sufficient for a JSON API
behind an exact-origin CORS list.

## 5. Security notes

- Session ids: 256-bit CSPRNG, base64url; only SHA-256 stored.
- Session fixation prevented by rotation on every privilege change (§4.3).
- XSS blast radius *shrinks*: `HttpOnly` means injected JS cannot exfiltrate the
  credential at all — today a token in `AsyncStorage` is readable by any JS
  that runs.
- Sign-in / sign-up endpoints keep the existing rate limiter.
- DB leak: hashes are useless as credentials; attacker would need the preimage.
- Cookie `Secure` off only via explicit `COOKIE_SECURE=false`-style flag for
  local dev over plain HTTP.

## 6. Failure modes

| Failure | Behaviour |
| --- | --- |
| Postgres down | Web auth fails closed (401) — consistent with every other query in the system; native JWT path unaffected. |
| Session row deleted mid-request burst | In-flight requests that already authenticated complete; the next gets 401. Same semantics as token revocation. |
| Cookie present + Bearer present | Bearer wins (native path tried first); no ambiguity. |
| Redis down | Only the socket fast-path announce degrades; web revocation doesn't touch Redis at all. |
| Dev cross-origin (Expo web on `:19006` → API `:8080`) | Cookie needs `SameSite=None; Secure`, and `credentials:'include'` works with the existing credentialed CORS. Alternatively run dev behind the compose nginx so it's same-origin like prod. |

## 7. Rollout

Ordered so each step is independently shippable and revertible:

1. **Migration + queries** — `sessions` table, `CreateSession`,
   `GetSessionUser` (with the `deleted_at` join), `DeleteSession`,
   `DeleteUserSessions`, `TouchSession`. Unit tests with a live pool.
2. **Session issuer + middleware** — cookie read/write helpers, dual-auth in
   `auth.Middleware`, `X-DC-Client` check on cookie-auth'd mutations. Tests:
   bearer path unchanged, cookie path, expired/deleted/deleted-account cases.
3. **Endpoints** — `Set-Cookie` on the four issuing endpoints, `POST
   /auth/signout`. Response bodies unchanged.
4. **Deletion transaction** — extend `RequestUserDeletion` CTE, fold in
   `RevokeUserRefreshTokens`, adjust `DeleteAccount` and its tests.
5. **Web client** — `credentials:'include'`, boot via `/auth/me`, drop web
   token persistence, WS cookie path. Gated per-platform so native ships no
   change.
6. **Ops** — `COOKIE_SECURE` / domain flags in `config.go`, `.env.example`,
   README auth section.

Rollback at any step ≤4: web simply keeps using the Bearer path it already has.
After step 5, rollback is a client flag.

## 8. Alternatives considered

**Separate BFF service** (a Node/Go proxy holding sessions, forwarding
`Authorization` to the API). This is the textbook BFF, but here it buys little:
the Go API already owns Postgres, so a proxy adds a network hop, a deployable,
and a new trust boundary (BFF→API must be unreachable directly) only to hold
state the API can hold itself. Worth revisiting if the frontend ever needs
server-side rendering or API composition across services.

**Sessions in Redis, looked up per request.** Lower latency ceiling, but the
session delete can no longer join the mark transaction — back to dual-write
with a retry burden. Postgres PK lookups are cheap enough at this scale;
Redis can be added later as a write-through cache keyed by `id_hash` if
profiling ever demands it.

**Denylist-only fix (no sessions)**: extend `RequestUserDeletion` to write a
`session_revocations` outbox row, relay it to Redis, and have REST middleware
check Redis. Atomically durable, but revocation visibility is still eventual
(relay latency + Redis health) and adds a per-request Redis read to every REST
call — more machinery for a weaker guarantee than deleting a session row.

**Keep JWT for web, shorten TTL.** Shrinks the window but never closes it, and
increases rotation traffic for every client.

## 9. Open questions

- **Session listing / "sign out other devices."** The `sessions` table makes
  per-device sessions enumerable — do we expose `GET /auth/sessions` +
  per-session delete now or defer? (Cheap once the table exists.)
- **Re-registration during the mark→delete window.** A marked account's email
  still occupies the unique constraint until the worker deletes the row; a
  same-email sign-up in that window gets a generic conflict. Acceptable, or
  should the error say "account deletion in progress"?
- **Native sign-out.** Today it's client-side only; `POST /auth/signout` lets
  native revoke its refresh token too. Take it, or keep native purely local?
- **Session idle expiry.** Design has absolute expiry + `last_seen_at`
  bookkeeping but no idle cutoff yet — do we want one (e.g., 7 d idle →
  re-auth), or is 30 d absolute fine?
