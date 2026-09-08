-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (user_id, token_hash, expires_at, family_id)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetRefreshToken :one
SELECT * FROM refresh_tokens WHERE token_hash = $1;

-- name: UseRefreshToken :execrows
UPDATE refresh_tokens
SET used_at = now()
WHERE id = $1
  AND used_at IS NULL
  AND expires_at > now();

-- name: RevokeRefreshTokenFamily :execrows
DELETE FROM refresh_tokens WHERE family_id = $1;

-- name: RevokeUserRefreshTokens :execrows
DELETE FROM refresh_tokens WHERE user_id = $1;

-- name: DeleteExpiredRefreshTokens :execrows
DELETE FROM refresh_tokens WHERE expires_at <= now();
