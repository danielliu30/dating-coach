# loadtest guardrails

Load-testing harness. Each `cmd/lt-*` is a standalone component: its own
scenario, invariants, ramp, and report. `internal/harness` is thin plumbing
only (client, seed, metrics, dbstats, ramp, fakestripe) — scenario logic and
correctness assertions belong in the cmd, not the library.

## Run

```sh
docker compose --env-file loadtest/.env \
  -f docker-compose.yml -f loadtest/docker-compose.override.yml \
  --profile gateway up -d --build
cd loadtest && go build ./... && go run ./cmd/lt-sanity
```

## Rules

- **Dev tool only** — never point a component at a production deployment.
- **No secrets in output:** reports must never contain JWTs, refresh tokens,
  verification codes, passwords, or message bodies; assert on ids/statuses.
- **Cleanup:** every run deletes its tagged fixtures (`*@loadtest.local`).
- **One component per PR** (per the pr-granularity playbook), stacked with
  `Depends on: #N` in the PR body.
- Keep `PAYMENTS_ENABLED=false` in `loadtest/.env`; lt-payments flips it per
  run once the Stripe provider exists.
