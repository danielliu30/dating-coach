-- A booking is a request until the coach confirms it. It starts out 'pending'
-- and the coach moves it to 'scheduled' (confirm) or 'declined'; a request the
-- coach never answers becomes 'expired' once respond_by has passed. Pending
-- requests hold their slot, so the no-overlap constraint covers both states:
-- otherwise two clients could request the same time and the coach confirm both.
ALTER TABLE coaching_sessions
    DROP CONSTRAINT coaching_sessions_status_check,
    ADD CONSTRAINT coaching_sessions_status_check
        CHECK (status IN ('pending', 'scheduled', 'declined', 'expired', 'completed', 'cancelled', 'no_show')),
    ALTER COLUMN status SET DEFAULT 'pending',
    ADD COLUMN confirmation_token text,
    ADD COLUMN respond_by         timestamptz,
    ADD COLUMN confirmed_at       timestamptz,
    DROP CONSTRAINT coaching_sessions_no_overlap,
    ADD CONSTRAINT coaching_sessions_no_overlap EXCLUDE USING gist (
        coach_id WITH =,
        session_range(scheduled_time, duration_minutes) WITH &&
    ) WHERE (status IN ('pending', 'scheduled'));

-- The confirmation link in the coach's email carries the token; the worker's
-- expiry sweep scans respond_by.
CREATE UNIQUE INDEX coaching_sessions_confirmation_token_idx
    ON coaching_sessions (confirmation_token)
    WHERE confirmation_token IS NOT NULL;

CREATE INDEX coaching_sessions_pending_idx
    ON coaching_sessions (respond_by)
    WHERE status = 'pending';

-- Outbox for the emails the booking flow sends. The request or worker that
-- changes a session records the email in the same transaction, and the worker
-- delivers it afterwards, so an SMTP outage cannot leave a coach unaware of a
-- request that is holding one of their slots. ics, when set, is attached as a
-- text/calendar invite.
CREATE TABLE email_outbox (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    to_email    text NOT NULL,
    subject     text NOT NULL,
    body        text NOT NULL,
    ics         text,
    attempts    integer NOT NULL DEFAULT 0,
    last_error  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    sent_at     timestamptz
);

CREATE INDEX email_outbox_pending_idx
    ON email_outbox (created_at)
    WHERE sent_at IS NULL;
