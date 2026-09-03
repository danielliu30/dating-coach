-- name: EnqueueEmail :one
INSERT INTO email_outbox (to_email, subject, body, ics)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: PendingEmails :many
SELECT * FROM email_outbox
WHERE sent_at IS NULL AND attempts < $2
ORDER BY created_at
LIMIT $1;

-- name: MarkEmailSent :execrows
UPDATE email_outbox
SET sent_at = now(), attempts = attempts + 1, last_error = ''
WHERE id = $1 AND sent_at IS NULL;

-- name: MarkEmailFailed :execrows
UPDATE email_outbox
SET attempts = attempts + 1, last_error = $2
WHERE id = $1 AND sent_at IS NULL;
