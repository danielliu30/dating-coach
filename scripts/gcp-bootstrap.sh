#!/usr/bin/env bash
# One-time (idempotent) Google Cloud setup for the Cloud Run deployment.
# Creates the service accounts, GitHub OIDC trust, Secret Manager secrets and
# Pub/Sub topics. Push subscriptions and Cloud Scheduler jobs need the worker's
# URL, so the release workflow creates/updates those after it deploys the worker.
#
#   PROJECT_ID=my-project REGION=us-central1 ./scripts/gcp-bootstrap.sh
#
# Optional: export DATABASE_URL, REDIS_URL, JWT_SECRET, LLM_API_KEY,
# SMTP_PASSWORD before running to store them as new secret versions.
set -euo pipefail

: "${PROJECT_ID:?set PROJECT_ID}"
REGION="${REGION:-us-central1}"
GITHUB_REPO="${GITHUB_REPO:-danielliu30/dating-coach}"
ANALYSIS_TOPIC="${ANALYSIS_TOPIC:-conversation.analysis}"
DELETION_TOPIC="${DELETION_TOPIC:-account.deletion}"
EMAIL_TOPIC="${EMAIL_TOPIC:-email.outbox}"
SECRETS=(DATABASE_URL REDIS_URL JWT_SECRET LLM_API_KEY SMTP_PASSWORD)

g() { gcloud --project "$PROJECT_ID" --quiet "$@"; }
exists() { "$@" >/dev/null 2>&1; }

PROJECT_NUMBER="$(g projects describe "$PROJECT_ID" --format='value(projectNumber)')"
RUNTIME_SA="dc-runtime@$PROJECT_ID.iam.gserviceaccount.com"
INVOKER_SA="dc-invoker@$PROJECT_ID.iam.gserviceaccount.com"
DEPLOY_SA="dc-deploy@$PROJECT_ID.iam.gserviceaccount.com"
PUBSUB_AGENT="service-$PROJECT_NUMBER@gcp-sa-pubsub.iam.gserviceaccount.com"

echo "== enabling APIs"
g services enable run.googleapis.com pubsub.googleapis.com cloudscheduler.googleapis.com \
  secretmanager.googleapis.com iam.googleapis.com iamcredentials.googleapis.com sts.googleapis.com

echo "== service accounts"
for sa in dc-runtime:"Cloud Run runtime (api, worker, ml, migrate)" \
          dc-invoker:"Pub/Sub push and Cloud Scheduler caller" \
          dc-deploy:"GitHub Actions deployer"; do
  name="${sa%%:*}"
  exists g iam service-accounts describe "$name@$PROJECT_ID.iam.gserviceaccount.com" \
    || g iam service-accounts create "$name" --display-name "${sa#*:}"
done

bind() { g projects add-iam-policy-binding "$PROJECT_ID" --member "serviceAccount:$1" --role "$2" --condition=None >/dev/null; }
# runtime: read secrets, publish jobs, call the private ml-analyzer
bind "$RUNTIME_SA" roles/secretmanager.secretAccessor
bind "$RUNTIME_SA" roles/pubsub.publisher
bind "$RUNTIME_SA" roles/run.invoker
# invoker: Pub/Sub push and Scheduler OIDC tokens for the private worker
bind "$INVOKER_SA" roles/run.invoker
# deployer: Cloud Run services/jobs, subscriptions, scheduler jobs
bind "$DEPLOY_SA" roles/run.admin
bind "$DEPLOY_SA" roles/pubsub.editor
bind "$DEPLOY_SA" roles/cloudscheduler.admin
for sa in "$RUNTIME_SA" "$INVOKER_SA"; do
  g iam service-accounts add-iam-policy-binding "$sa" --member "serviceAccount:$DEPLOY_SA" \
    --role roles/iam.serviceAccountUser >/dev/null
done
# Pub/Sub mints the push OIDC token as the invoker account
g iam service-accounts add-iam-policy-binding "$INVOKER_SA" --member "serviceAccount:$PUBSUB_AGENT" \
  --role roles/iam.serviceAccountTokenCreator >/dev/null

echo "== GitHub Actions workload identity federation ($GITHUB_REPO)"
exists g iam workload-identity-pools describe github --location global \
  || g iam workload-identity-pools create github --location global --display-name "GitHub Actions"
exists g iam workload-identity-pools providers describe github --location global --workload-identity-pool github \
  || g iam workload-identity-pools providers create-oidc github --location global --workload-identity-pool github \
       --issuer-uri "https://token.actions.githubusercontent.com" \
       --attribute-mapping "google.subject=assertion.sub,attribute.repository=assertion.repository" \
       --attribute-condition "assertion.repository=='$GITHUB_REPO'"
g iam service-accounts add-iam-policy-binding "$DEPLOY_SA" --role roles/iam.workloadIdentityUser \
  --member "principalSet://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/github/attribute.repository/$GITHUB_REPO" >/dev/null

echo "== secrets"
for s in "${SECRETS[@]}"; do
  exists g secrets describe "$s" || g secrets create "$s" --replication-policy automatic
  if [ -n "${!s:-}" ]; then
    printf %s "${!s}" | g secrets versions add "$s" --data-file=- >/dev/null
    echo "stored a new version of $s"
  elif ! exists g secrets versions describe latest --secret "$s"; then
    echo "WARNING: $s has no value yet: printf %s '<value>' | gcloud secrets versions add $s --data-file=- --project $PROJECT_ID"
  fi
done

echo "== Pub/Sub topics (+ dead-letter topics with a pull subscription to inspect/replay)"
for t in "$ANALYSIS_TOPIC" "$DELETION_TOPIC" "$EMAIL_TOPIC"; do
  exists g pubsub topics describe "$t" || g pubsub topics create "$t"
  exists g pubsub topics describe "$t.dlq" || g pubsub topics create "$t.dlq"
  exists g pubsub subscriptions describe "$t.dlq-hold" \
    || g pubsub subscriptions create "$t.dlq-hold" --topic "$t.dlq" --message-retention-duration 7d --expiration-period never
  g pubsub topics add-iam-policy-binding "$t.dlq" --member "serviceAccount:$PUBSUB_AGENT" --role roles/pubsub.publisher >/dev/null
done

cat <<OUT

Done. Set these GitHub repository *variables* (Settings > Secrets and variables > Actions > Variables):
  GCP_PROJECT_ID=$PROJECT_ID
  GCP_REGION=$REGION
  GCP_WORKLOAD_IDENTITY_PROVIDER=projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/github/providers/github
  GCP_DEPLOY_SERVICE_ACCOUNT=$DEPLOY_SA
  GCP_RUNTIME_SERVICE_ACCOUNT=$RUNTIME_SA
  GCP_INVOKER_SERVICE_ACCOUNT=$INVOKER_SA
Cloud Run URLs are https://<service>-$PROJECT_NUMBER.$REGION.run.app, so:
  API_PUBLIC_URL=https://dating-coach-api-$PROJECT_NUMBER.$REGION.run.app
  PUBLIC_APP_URL=https://dating-coach-web-$PROJECT_NUMBER.$REGION.run.app
OUT
