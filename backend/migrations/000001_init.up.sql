CREATE EXTENSION IF NOT EXISTS "pgcrypto";
-- btree_gist lets the coaching_sessions exclusion constraint below combine uuid
-- equality with timestamp-range overlap.
CREATE EXTENSION IF NOT EXISTS "btree_gist";

CREATE TABLE users (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email                    text NOT NULL UNIQUE,
    password_hash            text NOT NULL,
    display_name             text NOT NULL,
    role                     text NOT NULL DEFAULT 'user',
    email_verified           boolean NOT NULL DEFAULT false,
    verification_token       text,
    verification_expires_at  timestamptz,
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_role_check CHECK (role IN ('user', 'coach', 'admin'))
);

CREATE INDEX users_role_idx ON users (role);

-- Coach profile. One row per user with role = 'coach'.
CREATE TABLE coaches (
    user_id           uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    headline          text NOT NULL DEFAULT '',
    bio               text NOT NULL DEFAULT '',
    specialties       text[] NOT NULL DEFAULT '{}',
    hourly_rate_cents integer NOT NULL DEFAULT 0,
    timezone          text NOT NULL DEFAULT 'UTC',
    years_experience  integer NOT NULL DEFAULT 0,
    accepting_clients boolean NOT NULL DEFAULT true,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- Weekly recurring availability, stored as minutes from midnight in the coach timezone.
CREATE TABLE coach_availability (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    coach_id      uuid NOT NULL REFERENCES coaches (user_id) ON DELETE CASCADE,
    weekday       smallint NOT NULL,
    start_minute  integer NOT NULL,
    end_minute    integer NOT NULL,
    CONSTRAINT coach_availability_weekday_check CHECK (weekday BETWEEN 0 AND 6),
    CONSTRAINT coach_availability_range_check CHECK (start_minute >= 0 AND end_minute > start_minute AND end_minute <= 1440)
);

CREATE INDEX coach_availability_coach_idx ON coach_availability (coach_id, weekday);

-- timestamptz + interval is only STABLE (day/month components depend on the
-- session timezone), so wrap the minute arithmetic to get an index-usable
-- IMMUTABLE expression for the exclusion constraint below.
CREATE FUNCTION session_range(start_time timestamptz, minutes integer)
RETURNS tstzrange
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT tstzrange(start_time, start_time + make_interval(mins => minutes))
$$;

CREATE TABLE coaching_sessions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    coach_id         uuid NOT NULL REFERENCES coaches (user_id) ON DELETE CASCADE,
    scheduled_time   timestamptz NOT NULL,
    duration_minutes integer NOT NULL DEFAULT 45,
    status           text NOT NULL DEFAULT 'scheduled',
    topic            text NOT NULL DEFAULT '',
    coach_notes      text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT coaching_sessions_status_check CHECK (status IN ('scheduled', 'completed', 'cancelled', 'no_show')),
    CONSTRAINT coaching_sessions_duration_check CHECK (duration_minutes BETWEEN 15 AND 240),
    -- Two scheduled sessions for the same coach may never overlap, whatever
    -- their start times are.
    CONSTRAINT coaching_sessions_no_overlap EXCLUDE USING gist (
        coach_id WITH =,
        session_range(scheduled_time, duration_minutes) WITH &&
    ) WHERE (status = 'scheduled')
);

CREATE INDEX coaching_sessions_user_idx ON coaching_sessions (user_id, scheduled_time DESC);
CREATE INDEX coaching_sessions_coach_idx ON coaching_sessions (coach_id, scheduled_time DESC);

-- Live user <-> coach chat.
CREATE TABLE chat_threads (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    coach_id        uuid NOT NULL REFERENCES coaches (user_id) ON DELETE CASCADE,
    session_id      uuid REFERENCES coaching_sessions (id) ON DELETE SET NULL,
    status          text NOT NULL DEFAULT 'active',
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_message_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chat_threads_status_check CHECK (status IN ('active', 'closed'))
);

CREATE INDEX chat_threads_user_idx ON chat_threads (user_id, last_message_at DESC);
CREATE INDEX chat_threads_coach_idx ON chat_threads (coach_id, last_message_at DESC);

CREATE TABLE chat_messages (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id  uuid NOT NULL REFERENCES chat_threads (id) ON DELETE CASCADE,
    sender_id  uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    body       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX chat_messages_thread_idx ON chat_messages (thread_id, created_at);

-- A dating-app conversation submitted by a user for analysis.
CREATE TABLE conversations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title       text NOT NULL DEFAULT '',
    platform    text NOT NULL DEFAULT 'unknown',
    match_name  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX conversations_user_idx ON conversations (user_id, created_at DESC);

CREATE TABLE messages (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    position        integer NOT NULL,
    sender          text NOT NULL,
    body            text NOT NULL,
    sent_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT messages_sender_check CHECK (sender IN ('self', 'match'))
);

CREATE UNIQUE INDEX messages_conversation_position_idx ON messages (conversation_id, position);

-- Output of the ML analyzer. segments holds per-segment scores, overall holds
-- the aggregate feedback; both are JSONB so the ML contract can evolve.
CREATE TABLE analysis_results (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    status          text NOT NULL DEFAULT 'pending',
    model_version   text NOT NULL DEFAULT '',
    segments        jsonb NOT NULL DEFAULT '[]'::jsonb,
    overall         jsonb NOT NULL DEFAULT '{}'::jsonb,
    error           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    completed_at    timestamptz,
    CONSTRAINT analysis_results_status_check CHECK (status IN ('pending', 'running', 'succeeded', 'failed'))
);

CREATE INDEX analysis_results_conversation_idx ON analysis_results (conversation_id, created_at DESC);

-- Labeled examples used to train/fine-tune the in-house scorer later on.
-- See ml-analyzer/README.md for the label schema.
CREATE TABLE training_examples (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id  uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    label_source     text NOT NULL DEFAULT 'user',
    outcome          text,
    reply_received   boolean,
    engagement_score double precision,
    segment_labels   jsonb NOT NULL DEFAULT '[]'::jsonb,
    notes            text NOT NULL DEFAULT '',
    consented        boolean NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT training_examples_label_source_check CHECK (label_source IN ('user', 'coach', 'heuristic')),
    CONSTRAINT training_examples_outcome_check CHECK (outcome IS NULL OR outcome IN ('ghosted', 'kept_talking', 'number_exchanged', 'date_set'))
);

-- One label per source per conversation; re-labelling updates in place.
CREATE UNIQUE INDEX training_examples_conversation_source_idx ON training_examples (conversation_id, label_source);

CREATE TABLE notifications (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind       text NOT NULL,
    payload    jsonb NOT NULL DEFAULT '{}'::jsonb,
    sent_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX notifications_user_idx ON notifications (user_id, created_at DESC);
