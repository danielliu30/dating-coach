DROP TABLE IF EXISTS email_outbox;

DROP INDEX IF EXISTS coaching_sessions_pending_idx;
DROP INDEX IF EXISTS coaching_sessions_confirmation_token_idx;

-- Requests that were never confirmed cannot be expressed by the old schema.
UPDATE coaching_sessions SET status = 'cancelled' WHERE status IN ('pending', 'declined', 'expired');

ALTER TABLE coaching_sessions
    DROP CONSTRAINT coaching_sessions_no_overlap,
    ADD CONSTRAINT coaching_sessions_no_overlap EXCLUDE USING gist (
        coach_id WITH =,
        session_range(scheduled_time, duration_minutes) WITH &&
    ) WHERE (status = 'scheduled'),
    DROP COLUMN calendar_sequence,
    DROP COLUMN confirmed_at,
    DROP COLUMN respond_by,
    DROP COLUMN confirmation_token,
    ALTER COLUMN status SET DEFAULT 'scheduled',
    DROP CONSTRAINT coaching_sessions_status_check,
    ADD CONSTRAINT coaching_sessions_status_check
        CHECK (status IN ('scheduled', 'completed', 'cancelled', 'no_show'));
