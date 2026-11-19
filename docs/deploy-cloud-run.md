# Production on Google Cloud Run

Each piece runs on its own free-tier host and scales on its own. Nothing idles:
every Cloud Run service scales to zero, and the worker only runs when Pub/Sub or
Cloud Scheduler calls it.

| Piece | Host | Notes |
|-------|------|-------|
| API (`dating-coach-api`) | Cloud Run, public | REST + WebSockets (1 h request timeout, session affinity) |
| Worker (`dating-coach-worker`) | Cloud Run, private | `QUEUE_BACKEND=pubsub`: HTTP endpoints instead of RabbitMQ consumers and timers |
| ML analyzer (`dating-coach-ml`) | Cloud Run, private | API and worker call it with a Google ID token (`ML_SERVICE_AUTH=google`) |
| Web (`dating-coach-web`) | Cloud Run, public | Static Expo bundle, calls the API URL baked in at build (`API_PUBLIC_URL`) |
| Migrations (`dating-coach-migrate`) | Cloud Run job | Runs `migrate up` once per deploy, before the services |
| Postgres | Neon free plan | `DATABASE_URL` with `?sslmode=require` |
| Redis | Upstash free plan | `REDIS_URL` as `rediss://default:<password>@<host>:6379` |
| Queue | Pub/Sub push | Topics `conversation.analysis`, `account.deletion`, `email.outbox`; 5 attempts, then `<topic>.dlq` |
| Timers | Cloud Scheduler, 2 jobs | Every minute: emails, deletions outbox, booking expiry, revocation replay, email-outbox check; payment sweep only when enabled. Hourly: refresh-token purge |

Images are still built and pushed to Docker Hub (`danielliu30/*`) by
`.github/workflows/release.yml`; Cloud Run pulls them from there.

## How the worker is driven

- `POST /pubsub/analysis`, `/pubsub/account-deletion`: Pub/Sub push of a job. 2xx acks, 5xx makes Pub/Sub retry (10 s to 10 min backoff); after 5 attempts the message goes to `<topic>.dlq`, held 7 days on the `<topic>.dlq-hold` pull subscription for inspection or replay.
- `POST /pubsub/email-outbox`: the API publishes a nudge right after saving an email (verification, reset codes), so mail goes out at once; the minutely sweep is only a backup.
- `POST /tasks/<name>` and `POST /tasks/batch?task=a&task=b`: one pass of a timed loop. Cloud Scheduler calls the batch endpoint.
- Payments default to disabled in the release job's `PAYMENTS_ENABLED` setting;
  the scheduler omits `sweep-payments` in that mode. To enable payments, provision
  `STRIPE_SECRET_KEY` and `STRIPE_WEBHOOK_SECRET`, add them to the backend
  deployments' secret bindings, and set that shared setting to `true`.
  A batch containing an unregistered task returns 404 before running any task.
- Auth is Cloud Run IAM: only the `dc-invoker` service account can call the worker.

Local `docker compose` is unchanged (RabbitMQ, long-running worker, `QUEUE_BACKEND=rabbitmq` by default).

## One-time setup

### 1. Accounts to create

1. **Google Cloud**: a project with billing enabled (required even for the free tier). Install the `gcloud` CLI and `gcloud auth login`.
2. **Neon** (neon.tech): a free project; copy the pooled connection string.
3. **Upstash** (upstash.com): a free Redis database; copy the `rediss://` URL.
4. An SMTP provider (e.g. Resend) if real email is wanted; without `SMTP_HOST` codes are only logged.
5. An LLM API key (Anthropic by default) for the ML analyzer.

### 2. Run the bootstrap script

```sh
export DATABASE_URL='postgres://...neon.tech/neondb?sslmode=require'
export REDIS_URL='rediss://default:...@....upstash.io:6379'
export JWT_SECRET="$(openssl rand -hex 32)"
export LLM_API_KEY='sk-ant-...'
export SMTP_PASSWORD='...'            # or leave unset; set it later
PROJECT_ID=<project> REGION=us-central1 ./scripts/gcp-bootstrap.sh
```

It enables the APIs, creates the `dc-runtime`, `dc-invoker` and `dc-deploy`
service accounts, GitHub OIDC trust for this repo (no JSON keys), the five
Secret Manager secrets and the Pub/Sub topics, then prints the GitHub variables
to set. Re-running it is safe and updates the existing WIF provider's trust
condition. Only `.github/workflows/release.yml` on `refs/heads/main`, in the
`production` GitHub environment, can impersonate `dc-deploy`. Branch and pull
request workflows cannot obtain deployment credentials. Protect `main` and
configure required reviewers / deployment branch rules on `production`.

### 3. GitHub settings (Settings > Secrets and variables > Actions)

Secrets (already present): `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`.
Delete the old VM secrets `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY`.

Variables:

| Variable | Value |
|----------|-------|
| `GCP_PROJECT_ID`, `GCP_REGION` | printed by the script |
| `GCP_WORKLOAD_IDENTITY_PROVIDER`, `GCP_DEPLOY_SERVICE_ACCOUNT`, `GCP_RUNTIME_SERVICE_ACCOUNT`, `GCP_INVOKER_SERVICE_ACCOUNT` | printed by the script |
| `API_PUBLIC_URL` | `https://dating-coach-api-<project number>.<region>.run.app` (baked into the web bundle) |
| `PUBLIC_APP_URL` | `https://dating-coach-web-<project number>.<region>.run.app`, or your domain |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `MAIL_FROM` | optional, for real email |
| `GOOGLE_CLIENT_ID` | optional, Google sign-in |
| `LLM_PROVIDER`, `LLM_MODEL` | optional (default `anthropic`, `claude-sonnet-4-5`) |
| `API_MIN_INSTANCES`, `API_MAX_INSTANCES`, `WORKER_MAX_INSTANCES`, `ML_MAX_INSTANCES`, `WEB_MAX_INSTANCES` | optional scaling (defaults 0 / 2) |

Then run *Release (build, push, deploy)* from Actions **on `main`** (or merge to `main`).
Manual runs on other refs may build images but do not deploy. Manual rollback
also runs on `main`, with `image_tag` selecting the previously published image.
The first run creates every service, the push subscriptions and the scheduler jobs.

## Deploys and rollback

Each run: migrations job, ml-analyzer, worker, subscriptions and scheduler jobs,
API, web, then a health check of the API `/healthz` and the web root. On a failed
check the API and web traffic goes back to the revisions that served before the
run and the job fails. To roll back by hand, run the workflow with `image_tag` set
to an earlier SHA. Migrations only go `up`, so keep them backward-compatible.

## Scaling up later

Same services, different knobs: set `API_MIN_INSTANCES=1` (no cold starts,
steadier WebSockets), raise the `*_MAX_INSTANCES` variables, and move Neon and
Upstash to paid plans. The worker can stay push-driven.

## Free-tier limits to watch

- Cloud Run: 2M requests and 180k vCPU-seconds per month; the minutely scheduler call is about 44k requests.
- Cloud Scheduler: 3 jobs per billing account (this uses 2).
- Secret Manager: 6 active secret versions (this uses 5; Stripe would add 2, at $0.06 each per month).
- Pub/Sub: 10 GB per month.
- Upstash free: limited daily commands; Neon free: compute suspends when idle (first query after idle is slower).
