# e2e guardrails (Playwright click-through)

The required `e2e` check runs this suite in CI against the real gateway stack — it is the user-visible correctness gate. Specs cover approval, booking, deletion, happy-path, profile evidence + scoring, resilience (offline queue / WS replay), and sessions.

## Run locally

```sh
docker compose --env-file e2e/.env --profile gateway up -d --build   # nginx on :80
# wait: curl -fsS localhost/healthz && curl -fsS localhost/ml/healthz
cd e2e && npm ci && npx playwright install --with-deps chromium
npm run typecheck && npx playwright test
```

`e2e/.env` is the CI environment (heuristic ML scorer, no LLM key needed). Read verification codes from API logs or Mailpit (`--profile mail`).

## Invariants

- **Test user-visible behavior**, not implementation details — these specs are the click-through contract; a removed scenario is lost coverage.
- **Deterministic:** use Playwright wait-for APIs and the helpers in `helpers/`; no fixed `sleep`s where an assertion wait works.
- **Security in tests too:** don't log or screenshot tokens, verification codes, or WS frames containing the JWT.
- **Cleanup:** specs must not depend on each other or on leftover data — each creates its own users via the helpers.
- CI collects `compose-logs.txt` and Playwright traces on failure — keep failures debuggable rather than retrying flaky code.
