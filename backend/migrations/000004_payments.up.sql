-- Paid reservations. A booking made while payments are enabled is created as
-- pending_payment and only becomes scheduled once the provider confirms the
-- charge; until then hold_expires_at bounds how long it keeps its slot.
-- Bookings made while payments are disabled keep payment_status
-- 'not_required', so flipping the switch never changes existing rows.
--
-- payment_status also carries provider clean-up that is still owed after the
-- local state has moved on, so a failed provider call is retried by the sweeper
-- instead of being lost:
--   pending    -> checkout open, slot held
--   expiring   -> hold released locally, provider checkout still to be expired
--   failed     -> checkout closed without payment
--   paid       -> charge confirmed, session scheduled
--   refund_due -> charged after the slot was lost; provider refund still owed
--   refunded   -> refund issued
ALTER TABLE coaching_sessions
    ADD COLUMN payment_status  text NOT NULL DEFAULT 'not_required',
    ADD COLUMN amount_cents    integer NOT NULL DEFAULT 0,
    ADD COLUMN currency        text NOT NULL DEFAULT 'usd',
    ADD COLUMN payment_ref     text,
    ADD COLUMN hold_expires_at timestamptz,
    ADD CONSTRAINT coaching_sessions_payment_status_check
        CHECK (payment_status IN ('not_required', 'pending', 'expiring', 'paid', 'refund_due', 'refunded', 'failed')),
    ADD CONSTRAINT coaching_sessions_amount_check CHECK (amount_cents >= 0),
    DROP CONSTRAINT coaching_sessions_status_check,
    ADD CONSTRAINT coaching_sessions_status_check
        CHECK (status IN ('pending_payment', 'scheduled', 'completed', 'cancelled', 'no_show')),
    -- An unpaid hold must block the slot too, or two clients could both be
    -- sent off to pay for the same time.
    DROP CONSTRAINT coaching_sessions_no_overlap,
    ADD CONSTRAINT coaching_sessions_no_overlap EXCLUDE USING gist (
        coach_id WITH =,
        session_range(scheduled_time, duration_minutes) WITH &&
    ) WHERE (status IN ('scheduled', 'pending_payment'));

-- Webhook lookups go from the provider's checkout reference to the session.
CREATE UNIQUE INDEX coaching_sessions_payment_ref_idx
    ON coaching_sessions (payment_ref)
    WHERE payment_ref IS NOT NULL;

-- The hold sweeper only reads pending sessions that have run out of time.
CREATE INDEX coaching_sessions_hold_idx
    ON coaching_sessions (hold_expires_at)
    WHERE status = 'pending_payment';

-- The sweeper retries provider clean-up owed by rows in these states.
CREATE INDEX coaching_sessions_payment_cleanup_idx
    ON coaching_sessions (payment_status)
    WHERE payment_status IN ('expiring', 'refund_due');

-- One row per provider event so a redelivered webhook is a no-op.
CREATE TABLE payment_events (
    provider_event_id text PRIMARY KEY,
    session_id        uuid REFERENCES coaching_sessions (id) ON DELETE SET NULL,
    event_type        text NOT NULL,
    received_at       timestamptz NOT NULL DEFAULT now()
);
