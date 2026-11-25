#!/usr/bin/env bash
# Seed (or refresh) the founder's coach account in the compose Postgres so a
# bookable coach always exists on a development stack. The row lives in the
# postgres-data volume, so it persists across `compose down`/up — this only
# needs re-running on first bring-up or after `compose down -v`.
#
# The account is created verified with role 'coach', its coach profile is
# upserted and force-approved (accepting clients), and its weekly availability
# is replaced wholesale — re-running converges to the same state.
#
# Usage:   scripts/seed-coach.sh          (run from anywhere)
# Reads:   <repo>/.env   POSTGRES_USER / POSTGRES_DB (default datingcoach)
#                        BCRYPT_COST (default 12; matches the backend)
#                        COACH_EMAIL (default daniel@datingcoach.local)
#                        COACH_PASSWORD (default coach — dev stacks only)
#                        COACH_DISPLAY_NAME (default Daniel Liu)
#                        COACH_TIMEZONE (default America/New_York)
#                        COACH_HEADLINE / COACH_BIO / COACH_SPECIALTIES /
#                        COACH_PHASES (comma-separated, default all phases)
#                        COACH_HOURLY_RATE_CENTS (default 12000 = $120/hr)
#                        COACH_YEARS_EXPERIENCE (default 5)
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${ENV_FILE:-$REPO_DIR/.env}"

# load_env exports the POSTGRES_*, BCRYPT_COST and COACH_* lines of $ENV_FILE
# (surrounding quotes stripped). Values already set in the environment win.
load_env() {
    [ -f "$ENV_FILE" ] || return 0
    local line key value
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in
            POSTGRES_*=*|BCRYPT_COST=*|COACH_*=*) ;;
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
BCRYPT_COST="${BCRYPT_COST:-12}"
COACH_EMAIL="${COACH_EMAIL:-daniel@datingcoach.local}"
COACH_PASSWORD="${COACH_PASSWORD:-coach}"
COACH_DISPLAY_NAME="${COACH_DISPLAY_NAME:-Daniel Liu}"
COACH_TIMEZONE="${COACH_TIMEZONE:-America/New_York}"
COACH_HEADLINE="${COACH_HEADLINE:-Dating coach}"
COACH_BIO="${COACH_BIO:-Founder coach: practical, judgement-free help with apps, texting and first dates.}"
COACH_SPECIALTIES="${COACH_SPECIALTIES:-profile photos,openers,texting,first dates}"
COACH_PHASES="${COACH_PHASES:-opening,first_messages,building_rapport,flirting,asking_out,first_date,follow_up,defining_relationship}"
COACH_HOURLY_RATE_CENTS="${COACH_HOURLY_RATE_CENTS:-12000}"
COACH_YEARS_EXPERIENCE="${COACH_YEARS_EXPERIENCE:-5}"
[[ "$BCRYPT_COST" =~ ^[1-9][0-9]*$ ]] \
    || { echo "seed-coach: BCRYPT_COST must be a positive integer (got '$BCRYPT_COST')" >&2; exit 2; }
[[ "$COACH_HOURLY_RATE_CENTS" =~ ^[0-9]+$ && "$COACH_YEARS_EXPERIENCE" =~ ^[0-9]+$ ]] \
    || { echo "seed-coach: COACH_HOURLY_RATE_CENTS and COACH_YEARS_EXPERIENCE must be non-negative integers" >&2; exit 2; }
[ "$COACH_PASSWORD" = "coach" ] \
    && echo "seed-coach: default dev password in use; set COACH_PASSWORD in $ENV_FILE for anything shared or deployed" >&2

# Default availability: weekday evenings and weekend days, in the coach's
# timezone. COACH_AVAILABILITY can replace it with "dow=HH:MM-HH:MM,..."
# entries where dow is 0=Sunday..6=Saturday.
COACH_AVAILABILITY="${COACH_AVAILABILITY:-1=16:00-21:00,2=16:00-21:00,3=16:00-21:00,4=16:00-21:00,5=16:00-21:00,0=10:00-17:00,6=10:00-17:00}"

# compose runs docker compose against the repo's compose project regardless of
# the caller's working directory.
compose() {
    if [ -f "$ENV_FILE" ]; then
        docker compose --project-directory "$REPO_DIR" --env-file "$ENV_FILE" "$@"
    else
        docker compose --project-directory "$REPO_DIR" "$@"
    fi
}

avail_values=""
IFS=',' read -ra WINDOWS <<< "$COACH_AVAILABILITY"
for w in "${WINDOWS[@]}"; do
    IFS='=' read -r dow times <<< "$w"
    IFS='-' read -r tstart tend <<< "$times"
    IFS=':' read -r sh sm <<< "$tstart"
    IFS=':' read -r eh em <<< "$tend"
    [[ "$dow" =~ ^[0-6]$ && "$sh" =~ ^[0-9]{1,2}$ && "$sm" =~ ^[0-9]{1,2}$ \
       && "$eh" =~ ^[0-9]{1,2}$ && "$em" =~ ^[0-9]{1,2}$ ]] \
        || { echo "seed-coach: bad availability window '$w'" >&2; exit 2; }
    start=$((10#$sh * 60 + 10#$sm))
    end=$((10#$eh * 60 + 10#$em))
    [ $((10#$sh)) -le 23 ] && [ $((10#$eh)) -le 23 ] && [ $((10#$sm)) -le 59 ] && [ $((10#$em)) -le 59 ] \
        && [ "$end" -gt "$start" ] && [ "$end" -le 1440 ] \
        || { echo "seed-coach: bad availability window '$w'" >&2; exit 2; }
    avail_values+="(${dow}, ${start}, ${end}),"
done
avail_values="${avail_values%,}"
[ -n "$avail_values" ] || { echo "seed-coach: COACH_AVAILABILITY is empty" >&2; exit 2; }

# :'var' is psql's safely-quoted literal form — no string interpolation.
compose exec -T postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
    -v coach_email="$COACH_EMAIL" \
    -v coach_password="$COACH_PASSWORD" \
    -v coach_name="$COACH_DISPLAY_NAME" \
    -v coach_tz="$COACH_TIMEZONE" \
    -v coach_headline="$COACH_HEADLINE" \
    -v coach_bio="$COACH_BIO" \
    -v coach_specialties="$COACH_SPECIALTIES" \
    -v coach_phases="$COACH_PHASES" \
    -v coach_rate="$COACH_HOURLY_RATE_CENTS" \
    -v coach_years="$COACH_YEARS_EXPERIENCE" \
    -v bcrypt_cost="$BCRYPT_COST" \
    -v avail_values="$avail_values" <<'SQL'
BEGIN;

-- The account may already exist under any role (e.g. signed up as a client
-- first); either way it ends verified with role 'coach'.
INSERT INTO users (email, password_hash, display_name, role, email_verified)
VALUES (
    lower(trim(both ' ' from :'coach_email')),
    crypt(:'coach_password', gen_salt('bf', :'bcrypt_cost'::int)),
    :'coach_name',
    'coach',
    true
)
ON CONFLICT (email) DO UPDATE SET
    password_hash  = EXCLUDED.password_hash,
    display_name   = EXCLUDED.display_name,
    role           = 'coach',
    email_verified = true,
    updated_at     = now();

-- Profile upsert mirrors UpsertCoachProfile, plus the approval a founder
-- coach needs before clients can see or book it.
INSERT INTO coaches (
    user_id, headline, bio, specialties, phases,
    hourly_rate_cents, timezone, years_experience,
    accepting_clients, approval_status
)
SELECT id, :'coach_headline', :'coach_bio',
       string_to_array(:'coach_specialties', ','),
       string_to_array(:'coach_phases', ','),
       :'coach_rate'::int, :'coach_tz', :'coach_years'::int,
       true, 'approved'
FROM users WHERE email = lower(trim(both ' ' from :'coach_email'))
ON CONFLICT (user_id) DO UPDATE SET
    headline          = EXCLUDED.headline,
    bio               = EXCLUDED.bio,
    specialties       = EXCLUDED.specialties,
    phases            = EXCLUDED.phases,
    hourly_rate_cents = EXCLUDED.hourly_rate_cents,
    timezone          = EXCLUDED.timezone,
    years_experience  = EXCLUDED.years_experience,
    accepting_clients = true,
    approval_status   = 'approved',
    updated_at        = now();

-- Availability is fully replaced, matching PUT /coach/availability semantics.
DELETE FROM coach_availability
WHERE coach_id = (SELECT id FROM users WHERE email = lower(trim(both ' ' from :'coach_email')));

INSERT INTO coach_availability (coach_id, weekday, start_minute, end_minute)
SELECT u.id, v.weekday, v.start_minute, v.end_minute
FROM users u
JOIN (VALUES :avail_values) AS v(weekday, start_minute, end_minute) ON true
WHERE u.email = lower(trim(both ' ' from :'coach_email'));

COMMIT;

SELECT u.email, u.role, u.email_verified, c.approval_status, c.accepting_clients,
       c.timezone, c.hourly_rate_cents,
       (SELECT count(*) FROM coach_availability a WHERE a.coach_id = c.user_id) AS windows
FROM users u JOIN coaches c ON c.user_id = u.id
WHERE u.email = lower(trim(both ' ' from :'coach_email'));
SQL

echo "seeded coach ${COACH_EMAIL} (role=coach, verified, approved) in ${POSTGRES_DB}"
