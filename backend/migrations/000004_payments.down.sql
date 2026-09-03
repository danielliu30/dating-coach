DROP TABLE payment_events;

DROP INDEX coaching_sessions_payment_cleanup_idx;
DROP INDEX coaching_sessions_hold_idx;
DROP INDEX coaching_sessions_payment_ref_idx;

-- Unpaid holds cannot exist under the old status check.
DELETE FROM coaching_sessions WHERE status = 'pending_payment';

ALTER TABLE coaching_sessions
    DROP CONSTRAINT coaching_sessions_no_overlap,
    ADD CONSTRAINT coaching_sessions_no_overlap EXCLUDE USING gist (
        coach_id WITH =,
        session_range(scheduled_time, duration_minutes) WITH &&
    ) WHERE (status = 'scheduled'),
    DROP CONSTRAINT coaching_sessions_status_check,
    ADD CONSTRAINT coaching_sessions_status_check
        CHECK (status IN ('scheduled', 'completed', 'cancelled', 'no_show')),
    DROP CONSTRAINT coaching_sessions_amount_check,
    DROP CONSTRAINT coaching_sessions_payment_status_check,
    DROP COLUMN hold_expires_at,
    DROP COLUMN payment_ref,
    DROP COLUMN currency,
    DROP COLUMN amount_cents,
    DROP COLUMN payment_status;
