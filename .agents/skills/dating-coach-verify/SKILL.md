---
name: dating-coach-verify
description: Pick and run the cheapest verification that covers a dating-coach change - per-service gate commands, targeted test invocations, migration/sqlc checks, and a change-type to minimum-check guide.
allowed-tools:
  - read
  - grep
  - glob
  - exec
---

# Verifying changes in dating-coach

Choose the cheapest check that covers the change. CI runs the full required
suite (`build-test`, `typecheck-export`, `test`, `e2e`, `dependency-gate`,
`merge-gate-tests`) on every PR regardless - local verification is for fast
feedback, not for reproducing CI.

Each service's `AGENTS.md` is the source of truth for its gate. The commands
below mirror it; re-read the file if a command has drifted.

## backend/ (required check: `build-test`)

```bash
cd backend
test -z "$(gofmt -l .)"          # gofmt must be clean
go build ./...
go vet ./...
go test -count=1 -p 1 ./...      # serialized: tests share a Postgres
```

Targeted runs (keep `-p 1` - shared DB):

```bash
go test -count=1 -p 1 ./internal/<pkg> -run <TestName>
```

Migrations:

- New file only - never edit a merged migration. Migrations only go `up` and
  must be backward-compatible so old binary and new schema coexist mid-deploy.
- `ls backend/migrations` after merging a branch that adds one: two files
  sharing a version number makes `migrate` refuse the whole set.
- Regenerate the sqlc store after changing migrations or queries (`sqlc.yaml`);
  never hand-edit generated code.

## app/ (required check: `typecheck-export`)

```bash
cd app
npm ci                            # first time, or after lockfile changes
npm run typecheck                 # tsc --noEmit
npm test -- --ci                  # jest; target with -t '<name>' or a path
npx expo export --platform web    # only when bundle/build-config changes warrant it
```

`WEB_API_URL` / `API_PUBLIC_URL` / `GOOGLE_CLIENT_ID` are baked into the bundle
at build - a config change needs a rebuild to verify. Web-only target: no
native-only deps.

## ml-analyzer/ (required check: `test`)

```bash
cd ml-analyzer
pip install -r requirements.txt -r requirements-dev.txt
pytest                            # target with -k '<pattern>' or a test file
```

No `LLM_API_KEY` needed - the heuristic scorer is the default and must stay
deterministic and dependency-free.

## e2e/ (required check: `e2e`)

The full Playwright suite runs in CI against the real gateway stack. Run it
locally only when the change touches user-visible flows:

```bash
docker compose --env-file e2e/.env --profile gateway up -d --build
curl -fsS localhost/healthz && curl -fsS localhost/ml/healthz
cd e2e && npm ci && npx playwright install --with-deps chromium
npm run typecheck && npx playwright test            # subset: -g '<spec name>'
```

For click-through verification beyond CI, follow the `testing-dating-coach`
skill.

## Automation scripts

After changing `scripts/dependency-gate.sh`, `merge-cascade.sh`, `pr-deps.sh`
or `.github/workflows/merge-gate.yml`: `bash scripts/test-merge-gate.sh` (also
runs in CI as `merge-gate-tests`). After changing nginx config or web security
headers: `python scripts/test-security-headers.py` (needs Docker, curl,
OpenSSL).

## Compose / wiring changes

`APP_ENV` omitted or blank means *production*, and shipped secrets get
rejected - local verification needs `APP_ENV=development` (set in
`.env.example`, `e2e/.env`). Health probes: api `:8080/healthz`, ml-analyzer
`:8000/healthz` (both dependency-free - keep them that way).

## Change type -> minimum sufficient check

| Change | Minimum local check |
|---|---|
| Pure Go logic | package test + `go vet` + gofmt |
| Migration/query | apply against Postgres + package tests + sqlc regen builds |
| Handler/route | package test; compose request smoke if wiring changed |
| app component/logic | targeted jest + `npm run typecheck` |
| app build config / nginx | add `npx expo export --platform web`; `test-security-headers.py` for headers |
| ml-analyzer scoring | affected `test_*` suites in the same change (evidence rule) |
| auth/network/resilience layers | unit checks; e2e `resilience` spec must stay green |
| worker `/pubsub/*` or `/tasks/*` | replay/idempotency test - handlers retry up to 5x then DLQ |
