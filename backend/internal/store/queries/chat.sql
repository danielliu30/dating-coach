-- name: CreateChatThread :one
INSERT INTO chat_threads (user_id, coach_id, session_id)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetChatThread :one
SELECT * FROM chat_threads WHERE id = $1;

-- name: GetActiveThreadForPair :one
SELECT * FROM chat_threads
WHERE user_id = $1 AND coach_id = $2 AND status = 'active'
ORDER BY last_message_at DESC
LIMIT 1;

-- name: ListThreadsForUser :many
SELECT t.*, u.display_name AS counterpart_name
FROM chat_threads t
JOIN users u ON u.id = t.coach_id
WHERE t.user_id = $1
ORDER BY t.last_message_at DESC;

-- name: ListThreadsForCoach :many
SELECT t.*, u.display_name AS counterpart_name
FROM chat_threads t
JOIN users u ON u.id = t.user_id
WHERE t.coach_id = $1
  AND (sqlc.narg('status')::text IS NULL OR t.status = sqlc.narg('status')::text)
ORDER BY t.last_message_at DESC;

-- name: CloseChatThread :one
UPDATE chat_threads SET status = 'closed' WHERE id = $1 RETURNING *;

-- name: CreateChatMessage :one
INSERT INTO chat_messages (thread_id, sender_id, body)
VALUES ($1, $2, $3)
RETURNING *;

-- name: TouchChatThread :exec
UPDATE chat_threads SET last_message_at = now() WHERE id = $1;

-- name: ListChatMessages :many
SELECT * FROM chat_messages
WHERE thread_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: GetCoachApprovalStatus :one
-- The coach's admin review state; no row when the user has no coach profile.
SELECT approval_status FROM coaches WHERE user_id = $1;
