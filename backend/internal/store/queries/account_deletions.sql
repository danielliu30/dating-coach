-- name: RequestUserDeletion :execrows
WITH marked AS (
    UPDATE users
    SET deleted_at = COALESCE(deleted_at, now()),
        updated_at = now()
    WHERE id = $1
    RETURNING id
)
INSERT INTO account_deletions (user_id)
SELECT id FROM marked
ON CONFLICT (user_id) DO UPDATE
SET requested_at = now(),
    published_at = NULL;

-- name: PendingDeletions :many
SELECT user_id
FROM account_deletions
WHERE published_at IS NULL
ORDER BY requested_at
LIMIT $1;

-- name: MarkDeletionPublished :execrows
UPDATE account_deletions
SET published_at = now()
WHERE user_id = $1
  AND published_at IS NULL;

-- name: FinishUserDeletion :exec
DELETE FROM account_deletions WHERE user_id = $1;
