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
INSERT INTO coaching_sessions (user_id, coach_id, scheduled_time, duration_minutes, topic, status, confirmation_token, respond_by)
VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7)
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

-- name: AuthorizeSession :one
-- The customer's card is held: the booking becomes a request the coach has
-- until respond_by to answer.
UPDATE coaching_sessions
SET status = 'pending', payment_status = 'authorized', hold_expires_at = NULL,
    confirmation_token = $2, respond_by = $3, updated_at = now()
WHERE id = $1 AND status = 'pending_payment'
RETURNING *;

-- name: ReinstateAuthorizedSession :one
-- A card hold that landed after the payment hold lapsed. Only a hold the
-- sweeper timed out (hold_expires_at still set) is revived; one the participant
-- cancelled (hold cleared) is left as it is.
UPDATE coaching_sessions
SET status = 'pending', payment_status = 'authorized', hold_expires_at = NULL,
    confirmation_token = $2, respond_by = $3, updated_at = now()
WHERE id = $1 AND status = 'expired' AND hold_expires_at IS NOT NULL
  AND payment_status IN ('pending', 'expiring', 'failed')
RETURNING *;

-- name: ExpireLateAuthorizedHold :execrows
-- A card hold whose authorisation arrived after the session had already
-- started: the booking is over and the money is owed back.
UPDATE coaching_sessions
SET status = 'expired', payment_status = 'releasing', updated_at = now()
WHERE id = $1 AND status = 'pending_payment';

-- name: MarkAuthorizationToRelease :execrows
-- A card hold on a booking that will not go ahead (declined, expired,
-- cancelled, or authorised after the participant cancelled the request).
UPDATE coaching_sessions
SET payment_status = 'releasing', updated_at = now()
WHERE id = $1 AND status <> 'pending' AND status <> 'scheduled'
  AND payment_status IN ('pending', 'expiring', 'failed', 'authorized');

-- name: MarkSessionCaptured :execrows
UPDATE coaching_sessions
SET payment_status = 'paid', updated_at = now()
WHERE id = $1 AND payment_status = 'authorized';

-- name: MarkAuthorizationReleased :execrows
UPDATE coaching_sessions
SET payment_status = 'released', updated_at = now()
WHERE id = $1 AND payment_status = 'releasing';

-- name: MarkSessionRefunded :execrows
UPDATE coaching_sessions
SET payment_status = 'refunded', updated_at = now()
WHERE id = $1 AND payment_status = 'refund_due';

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

-- name: ExpirePaymentHolds :execrows
UPDATE coaching_sessions
SET status = 'expired',
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

-- name: ListAuthorizationsToRelease :many
SELECT * FROM coaching_sessions
WHERE payment_status = 'releasing' AND payment_ref IS NOT NULL
ORDER BY updated_at;

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

-- name: GetSessionParties :one
SELECT s.*,
       u.email        AS user_email,
       u.display_name AS user_name,
       cu.email       AS coach_email,
       cu.display_name AS coach_name,
       c.timezone     AS coach_timezone
FROM coaching_sessions s
JOIN users u ON u.id = s.user_id
JOIN users cu ON cu.id = s.coach_id
JOIN coaches c ON c.user_id = s.coach_id
WHERE s.id = $1;

-- name: GetSessionByConfirmationToken :one
SELECT * FROM coaching_sessions
WHERE id = $1 AND confirmation_token = $2;

-- name: ConfirmSession :one
UPDATE coaching_sessions
SET status = 'scheduled',
    confirmed_at = now(),
    confirmation_token = NULL,
    respond_by = NULL,
    calendar_sequence = calendar_sequence + 1,
    updated_at = now()
WHERE id = $1 AND status = 'pending' AND (respond_by IS NULL OR respond_by >= now())
RETURNING *;

-- name: DeclineSession :one
UPDATE coaching_sessions
SET status = 'declined',
    payment_status = CASE payment_status
                         WHEN 'authorized' THEN 'releasing'
                         WHEN 'paid' THEN 'refund_due'
                         ELSE payment_status END,
    confirmation_token = NULL,
    respond_by = NULL,
    calendar_sequence = calendar_sequence + 1,
    updated_at = now()
WHERE id = $1 AND status = 'pending' AND (respond_by IS NULL OR respond_by >= now())
RETURNING *;

-- name: ExpirePendingSessions :many
UPDATE coaching_sessions
SET status = 'expired',
    payment_status = CASE payment_status
                         WHEN 'authorized' THEN 'releasing'
                         WHEN 'paid' THEN 'refund_due'
                         ELSE payment_status END,
    confirmation_token = NULL,
    calendar_sequence = calendar_sequence + 1,
    updated_at = now()
WHERE id IN (
    SELECT id FROM coaching_sessions
    WHERE status = 'pending' AND respond_by < now()
    ORDER BY respond_by
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

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
  AND status IN ('pending_payment', 'pending', 'scheduled')
  AND scheduled_time >= $2
  AND scheduled_time < $3
ORDER BY scheduled_time;

-- name: UpdateSessionStatus :one
-- A participant's cancel clears the hold marker so a late payment refunds
-- rather than reinstates, even if the sweeper had already released the hold.
UPDATE coaching_sessions
SET status = $2,
    payment_status = CASE WHEN $2 = 'cancelled' AND payment_status = 'authorized' THEN 'releasing' ELSE payment_status END,
    calendar_sequence = calendar_sequence + 1,
    updated_at = now()
WHERE id = $1 AND status = sqlc.arg('expected_status')
RETURNING *;

-- name: RescheduleSession :one
UPDATE coaching_sessions
SET scheduled_time = $2,
    status = $3,
    confirmation_token = $4,
    respond_by = $5,
    confirmed_at = CASE WHEN $3 = 'scheduled' THEN confirmed_at ELSE NULL END,
    calendar_sequence = calendar_sequence + 1,
    updated_at = now()
WHERE id = $1 AND status = sqlc.arg('expected_status')
RETURNING *;

-- name: UpdateSessionNotes :one
UPDATE coaching_sessions
SET coach_notes = $2, updated_at = now()
WHERE id = $1
RETURNING *;
