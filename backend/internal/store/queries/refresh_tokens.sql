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

-- name: RevokeUserRefreshTokens :execrows
DELETE FROM refresh_tokens WHERE user_id = $1;

-- name: RevokeUserRefreshTokensIssuedBy :execrows
-- Ends the sessions a sign-out at @cutoff reaches: every family with a token
-- issued by then, so one renewed since is ended too, and the signing-out
-- session's own family whenever its tokens were issued.
DELETE FROM refresh_tokens
WHERE user_id = @user_id
  AND family_id IN (
    SELECT r.family_id FROM refresh_tokens r
    WHERE r.user_id = @user_id
      AND (r.issued_at <= @cutoff::timestamptz OR r.family_id = @family_id::uuid)
  );

-- name: DeleteExpiredRefreshTokens :execrows
DELETE FROM refresh_tokens WHERE expires_at <= now();
