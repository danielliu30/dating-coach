#!/usr/bin/env bash
# Seed (or refresh) a local admin account in the compose Postgres so an
# admin login always exists on a development stack. The row lives in the
# postgres-data volume, so it persists across `compose down`/up — this only
# needs re-running on first bring-up or after `compose down -v`.
#
# Usage:   scripts/seed-admin.sh          (run from anywhere)
# Reads:   <repo>/.env   POSTGRES_USER / POSTGRES_DB (default datingcoach)
#                        ADMIN_EMAIL (default admin@datingcoach.local)
#                        ADMIN_PASSWORD (default admin — dev stacks only)
#                        ADMIN_DISPLAY_NAME (default Admin)
#                        BCRYPT_COST (default 12; matches the backend)
# Idempotent: re-running resets the password and re-asserts role/verified on
# the same email. The hash uses pgcrypto's bcrypt ($2a$), verified by the
# backend's golang.org/x/crypto/bcrypt compare.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${ENV_FILE:-$REPO_DIR/.env}"

# load_env exports the POSTGRES_*, ADMIN_* and BCRYPT_COST lines of $ENV_FILE
# (surrounding quotes stripped). Values already set in the environment win.
load_env() {
    [ -f "$ENV_FILE" ] || return 0
    local line key value
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in
            POSTGRES_*=*|ADMIN_*=*|BCRYPT_COST=*) ;;
            *) continue ;;
        esac
        key="${line%%=*}"
        value="${line#*=}"
        value="${value%\"}"; value="${value#\"}"
        value="${value%\'}"; value="${value#\'}"
        [ -n "${!key+x}" ] || export "$key=$value"
    done < "$ENV_FILE"
}
load_env

POSTGRES_USER="${POSTGRES_USER:-datingcoach}"
POSTGRES_DB="${POSTGRES_DB:-datingcoach}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@datingcoach.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"
ADMIN_DISPLAY_NAME="${ADMIN_DISPLAY_NAME:-Admin}"
BCRYPT_COST="${BCRYPT_COST:-12}"
[[ "$BCRYPT_COST" =~ ^[1-9][0-9]*$ ]] \
    || { echo "seed-admin: BCRYPT_COST must be a positive integer (got '$BCRYPT_COST')" >&2; exit 2; }
[ "$ADMIN_PASSWORD" = "admin" ] \
    && echo "seed-admin: default dev password in use; set ADMIN_PASSWORD in $ENV_FILE for anything shared or deployed" >&2

# compose runs docker compose against the repo's compose project regardless of
# the caller's working directory.
compose() {
    if [ -f "$ENV_FILE" ]; then
        docker compose --project-directory "$REPO_DIR" --env-file "$ENV_FILE" "$@"
    else
        docker compose --project-directory "$REPO_DIR" "$@"
    fi
}

# Sign-in lowercases/trims the email before lookup, so store it normalized.
# :'var' is psql's safely-quoted literal form — no string interpolation.
compose exec -T postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
    -v admin_email="$ADMIN_EMAIL" \
    -v admin_password="$ADMIN_PASSWORD" \
    -v admin_name="$ADMIN_DISPLAY_NAME" \
    -v bcrypt_cost="$BCRYPT_COST" <<'SQL'
INSERT INTO users (email, password_hash, display_name, role, email_verified)
VALUES (
    lower(trim(both ' ' from :'admin_email')),
    crypt(:'admin_password', gen_salt('bf', :'bcrypt_cost'::int)),
    :'admin_name',
    'admin',
    true
)
ON CONFLICT (email) DO UPDATE SET
    password_hash  = EXCLUDED.password_hash,
    role           = 'admin',
    email_verified = true,
    updated_at     = now();
SQL

echo "seeded admin ${ADMIN_EMAIL} (role=admin, verified) in ${POSTGRES_DB}"
