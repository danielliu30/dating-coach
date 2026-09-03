-- name: UpsertCoachProfile :one
INSERT INTO coaches (user_id, headline, bio, specialties, hourly_rate_cents, timezone, years_experience, accepting_clients)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (user_id) DO UPDATE
SET headline = EXCLUDED.headline,
    bio = EXCLUDED.bio,
    specialties = EXCLUDED.specialties,
    hourly_rate_cents = EXCLUDED.hourly_rate_cents,
    timezone = EXCLUDED.timezone,
    years_experience = EXCLUDED.years_experience,
    accepting_clients = EXCLUDED.accepting_clients,
    updated_at = now()
RETURNING *;

-- name: ListCoaches :many
SELECT c.*, u.display_name, u.email
FROM coaches c
JOIN users u ON u.id = c.user_id
WHERE (sqlc.narg('accepting_only')::boolean IS NOT TRUE OR c.accepting_clients)
ORDER BY c.years_experience DESC, u.display_name
LIMIT $1 OFFSET $2;

-- name: GetCoach :one
SELECT c.*, u.display_name, u.email
FROM coaches c
JOIN users u ON u.id = c.user_id
WHERE c.user_id = $1;

-- name: ReplaceCoachAvailability :exec
DELETE FROM coach_availability WHERE coach_id = $1;

-- name: AddCoachAvailability :one
INSERT INTO coach_availability (coach_id, weekday, start_minute, end_minute)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: ListCoachAvailability :many
SELECT * FROM coach_availability
WHERE coach_id = $1
ORDER BY weekday, start_minute;

-- name: CreateCoachingSession :one
INSERT INTO coaching_sessions (user_id, coach_id, scheduled_time, duration_minutes, topic)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: CreatePendingPaymentSession :one
INSERT INTO coaching_sessions (user_id, coach_id, scheduled_time, duration_minutes, topic,
                               status, payment_status, amount_cents, currency, hold_expires_at)
VALUES ($1, $2, $3, $4, $5, 'pending_payment', 'pending', $6, $7, $8)
RETURNING *;

-- name: AttachCheckout :one
-- Stores the provider checkout on the session. If the hold has already been
-- released meanwhile, the checkout is recorded as owed clean-up ('expiring')
-- so the sweeper closes it; the caller must not hand out its URL.
UPDATE coaching_sessions
SET payment_ref = $2,
    payment_status = CASE WHEN status = 'pending_payment' THEN payment_status ELSE 'expiring' END,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: GetSessionByPaymentRefForUpdate :one
SELECT * FROM coaching_sessions WHERE payment_ref = $1 FOR UPDATE;

-- name: MarkSessionPaid :execrows
UPDATE coaching_sessions
SET status = 'scheduled', payment_status = 'paid', hold_expires_at = NULL, updated_at = now()
WHERE id = $1 AND status = 'pending_payment';

-- name: ReinstatePaidSession :execrows
-- Only a hold the sweeper timed out (hold_expires_at still set) is revived;
-- one the participant cancelled (hold cleared) is left cancelled.
UPDATE coaching_sessions
SET status = 'scheduled', payment_status = 'paid', hold_expires_at = NULL, updated_at = now()
WHERE id = $1 AND status = 'cancelled' AND hold_expires_at IS NOT NULL
  AND payment_status IN ('pending', 'expiring', 'failed');

-- name: MarkPaymentRefundDue :execrows
UPDATE coaching_sessions
SET payment_status = 'refund_due', updated_at = now()
WHERE id = $1 AND status = 'cancelled' AND payment_status IN ('pending', 'expiring', 'failed');

-- name: MarkSessionRefunded :execrows
UPDATE coaching_sessions
SET payment_status = 'refunded', updated_at = now()
WHERE id = $1 AND payment_status IN ('paid', 'refund_due');

-- name: CancelPendingPaymentSession :execrows
-- For a hold whose checkout is already closed (provider expired it, or it
-- was never created).
UPDATE coaching_sessions
SET status = 'cancelled', payment_status = 'failed', updated_at = now()
WHERE id = $1 AND status = 'pending_payment';

-- name: ReleasePendingPaymentSession :one
-- For a hold abandoned by a participant while its checkout may still be open:
-- the checkout becomes owed clean-up for the sweeper. Clearing hold_expires_at
-- records that this was a deliberate cancel, not a timeout.
UPDATE coaching_sessions
SET status = 'cancelled',
    payment_status = CASE WHEN payment_ref IS NULL THEN 'failed' ELSE 'expiring' END,
    hold_expires_at = NULL,
    updated_at = now()
WHERE id = $1 AND status = 'pending_payment'
RETURNING *;

-- name: CancelPaidSessionForRefund :one
-- For a payment that landed while the participant was cancelling the hold.
UPDATE coaching_sessions
SET status = 'cancelled', payment_status = 'refund_due', updated_at = now()
WHERE id = $1 AND status = 'scheduled' AND payment_status = 'paid'
RETURNING *;

-- name: ExpirePaymentHolds :execrows
UPDATE coaching_sessions
SET status = 'cancelled',
    payment_status = CASE WHEN payment_ref IS NULL THEN 'failed' ELSE 'expiring' END,
    updated_at = now()
WHERE status = 'pending_payment' AND hold_expires_at < $1;

-- name: ListCheckoutsToExpire :many
SELECT * FROM coaching_sessions
WHERE payment_status = 'expiring' AND payment_ref IS NOT NULL
ORDER BY updated_at;

-- name: MarkCheckoutExpired :execrows
UPDATE coaching_sessions
SET payment_status = 'failed', updated_at = now()
WHERE id = $1 AND payment_status = 'expiring';

-- name: ListRefundsDue :many
SELECT * FROM coaching_sessions
WHERE payment_status = 'refund_due' AND payment_ref IS NOT NULL
ORDER BY updated_at;

-- name: RecordPaymentEvent :execrows
INSERT INTO payment_events (provider_event_id, session_id, event_type)
VALUES ($1, $2, $3)
ON CONFLICT (provider_event_id) DO NOTHING;

-- name: GetCoachingSession :one
SELECT * FROM coaching_sessions WHERE id = $1;

-- name: ListSessionsForUser :many
SELECT s.*, u.display_name AS coach_name
FROM coaching_sessions s
JOIN users u ON u.id = s.coach_id
WHERE s.user_id = $1
  AND (sqlc.narg('status')::text IS NULL OR s.status = sqlc.narg('status')::text)
ORDER BY s.scheduled_time;

-- name: ListSessionsForCoach :many
SELECT s.*, u.display_name AS user_name
FROM coaching_sessions s
JOIN users u ON u.id = s.user_id
WHERE s.coach_id = $1
  AND (sqlc.narg('status')::text IS NULL OR s.status = sqlc.narg('status')::text)
  AND (sqlc.narg('from_time')::timestamptz IS NULL OR s.scheduled_time >= sqlc.narg('from_time')::timestamptz)
ORDER BY s.scheduled_time;

-- name: ListBookedSlots :many
SELECT id, scheduled_time, duration_minutes
FROM coaching_sessions
WHERE coach_id = $1
  AND status IN ('scheduled', 'pending_payment')
  AND scheduled_time >= $2
  AND scheduled_time < $3
ORDER BY scheduled_time;

-- name: UpdateSessionStatus :one
UPDATE coaching_sessions
SET status = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: RescheduleSession :one
UPDATE coaching_sessions
SET scheduled_time = $2, updated_at = now()
WHERE id = $1 AND status = 'scheduled'
RETURNING *;

-- name: UpdateSessionNotes :one
UPDATE coaching_sessions
SET coach_notes = $2, updated_at = now()
WHERE id = $1
RETURNING *;
