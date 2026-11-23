# backend guardrails (Go: api + worker)

Layout: `cmd/api` (REST + WebSockets), `cmd/worker` (Pub/Sub push handlers `/pubsub/*` + timed loops `/tasks/*`; RabbitMQ consumers locally), `internal/` — account, analysis, auth, chat, coaching, config, httpx, notify, payments, store. `migrations/` + `sqlc.yaml` (generated store layer).

## Required gate (`build-test`)

```sh
cd backend
test -z "$(gofmt -l .)"   # gofmt must be clean
go build ./...
go vet ./...
go test -count=1 -p 1 ./...   # serialized: tests share a Postgres; do not parallelize
```

Migrations must apply cleanly against Postgres (CI applies them before tests).

## Invariants

- **Migrations only go `up`.** No down path exists on deploy — every migration must be backward-compatible (old binary + new schema and vice versa). Never edit a merged migration; add a new one.
- **sqlc:** regenerate after changing migrations/queries (`sqlc.yaml`); don't hand-edit generated code.
- **Auth/transport:** JWT on REST and WS. `TRUSTED_PROXY_HOPS`/`TRUSTED_PROXY_CIDRS` control client-IP resolution — on Cloud Run, hops=1 + trust-all is correct only behind the managed ingress; the default hops=0 keeps CIDR-based resolution. Don't trust `X-Forwarded-*` unconditionally.
- **Secrets:** api/worker reject shipped/default credentials outside `development` — don't weaken that; log variable names, never values.
- **Worker idempotency:** Pub/Sub retries up to 5x then DLQs — every `/pubsub/*` and `/tasks/*` handler must be safe to replay. Timed loops use session-level PG advisory locks and commit per row; keep them resumable.
- **Availability:** api holds WebSocket sessions (1h timeout, session affinity on Cloud Run); worker scales to zero — no in-process timers or queues. `/healthz` stays dependency-free.
- SMTP is required outside `development` with TLS (implicit 465 or mandatory STARTTLS).
