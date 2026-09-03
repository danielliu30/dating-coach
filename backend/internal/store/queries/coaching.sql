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
    updated_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: DeclineSession :one
UPDATE coaching_sessions
SET status = 'declined',
    confirmation_token = NULL,
    respond_by = NULL,
    updated_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: ExpirePendingSessions :many
UPDATE coaching_sessions
SET status = 'expired',
    confirmation_token = NULL,
    updated_at = now()
WHERE status = 'pending' AND respond_by < now()
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
  AND status IN ('pending', 'scheduled')
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
SET scheduled_time = $2,
    status = $3,
    confirmation_token = $4,
    respond_by = $5,
    confirmed_at = CASE WHEN $3 = 'scheduled' THEN confirmed_at ELSE NULL END,
    updated_at = now()
WHERE id = $1 AND status IN ('pending', 'scheduled')
RETURNING *;

-- name: UpdateSessionNotes :one
UPDATE coaching_sessions
SET coach_notes = $2, updated_at = now()
WHERE id = $1
RETURNING *;
