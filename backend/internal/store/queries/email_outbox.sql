-- name: EnqueueEmail :one
INSERT INTO email_outbox (to_email, subject, body, ics)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: PendingEmails :many
SELECT * FROM email_outbox
WHERE sent_at IS NULL AND attempts < $2 AND next_attempt_at <= now()
ORDER BY next_attempt_at
LIMIT $1;

-- name: MarkEmailSent :execrows
UPDATE email_outbox
SET sent_at = now(), attempts = attempts + 1, last_error = ''
WHERE id = $1 AND sent_at IS NULL;

-- name: MarkEmailFailed :execrows
UPDATE email_outbox
SET attempts = attempts + 1,
    last_error = $2,
    next_attempt_at = now() + sqlc.arg('retry_after')::interval
WHERE id = $1 AND sent_at IS NULL;
