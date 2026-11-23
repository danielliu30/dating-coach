# loadtest

Componentized load tests for the dating-coach stack. Each test is its own
binary under `cmd/` with its own scenario, invariants, ramp and report —
there is no do-everything runner. `internal/harness` is shared plumbing only:
client, seeding, metrics, `pg_stat_statements` diffs, staged ramp, fake Stripe.

## Components

| binary | surface | verifies under ramp |
|---|---|---|
| `lt-sanity` | all | harness smoke check: seed → signin → me → signout, report |
| `lt-auth` | auth | signup/signin correctness, refresh rotation & desync |
| `lt-chat` | chat | socket delivery + persistence, slow-client drops |
| `lt-analysis` | analysis | queue throughput, terminal correctness, poll cost |
| `lt-booking` | coaching | slot mutual exclusion, confirm/expire races |
| `lt-payments` | payments | authorize/capture/release via fake Stripe + signed webhooks |
| `lt-e2e` | journey | `-journey all|book|chat|analyze` customer journeys under the ramp |
| `lt-mixed` | interference | thin cross-service run (optional) |

Each lands as its own PR; only `lt-sanity` ships with the skeleton.

`run.sh` health-checks the gateway, then runs every component at the
standard profile (`LT_*` env vars override `LT_VUS`/`LT_RPS`/`LT_DURATION`/
`LT_COMPONENTS`) and prints a per-component summary of the newest report.

## Setup

Bring the stack up with the loadtest env + override (the override preloads
`pg_stat_statements` and gives api/worker `host.docker.internal` for the fake
Stripe):

```sh
docker compose --env-file loadtest/.env \
  -f docker-compose.yml -f loadtest/docker-compose.override.yml \
  --profile gateway up -d --build
curl -fsS localhost/healthz && curl -fsS localhost/ml/healthz
```

Then from `loadtest/`:

```sh
go build ./...
go run ./cmd/lt-sanity                      # smoke: proves the plumbing
go run ./cmd/lt-auth -vus 10 -duration 30s  # each component owns its flags
```

Reports land in `loadtest/out/<component>-<unix>.json` with per-endpoint
latency/status histograms, counters, finding notes, and pg_stat_statements
deltas.

## Standard load profile

Components expose the same ramp flags so a run is comparable across surfaces:

```sh
-vus 10,20,50   # staged concurrency: 10 clients, then 20, then 50
-rps 150        # aggregate actions/sec across each stage's clients
-duration 30s   # per stage
```

`-rps` is open-loop and **aggregate**: `harness.Rate` ticks 150×/s shared by
every VU in the stage (15 rps each at 10 VUs, 3 at 50). It gates each
component's *primary action* — the signup call, signin attempt, message send,
booking attempt, analysis submit. Consequential calls (`/me`, result polls,
history reads) stay ungated because real clients issue them anyway; the
report's endpoint counts show the true request rate. `-rps 0` runs unpaced
(closed-loop, as fast as VUs can go). Tripwires record the last stage that
stayed healthy when the stack saturates.

## Conventions for writing a component

- One binary per surface; keep scenario logic in the cmd package and shared
  plumbing in `internal/harness`. Never log tokens, codes, or message PII.
- Seed fixtures through `harness.SeedUsers`/`SeedCoach` (bcrypt cost 4 in SQL);
  tag everything `*-@loadtest.local` and call `CleanupTag` on exit.
- Ramp via `harness.Run` with a `Tripwire` (error rate, lost echoes, p99);
  the report records the last stage that stayed healthy.
- `lt-payments` starts `FakeStripe` on the host (`:12111`) and brings up
  `api-pay`/`worker-pay` (compose override, `extends` api/worker with
  `PAYMENTS_ENABLED=true` + `STRIPE_API_BASE_URL=http://host.docker.internal:12111`,
  api on `127.0.0.1:8082`) — everything else runs against the stock .env,
  where payments stay off.
