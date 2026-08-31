-- name: RevokeUserSessions :exec
INSERT INTO revoked_sessions (user_id, expires_at)
VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE
SET revoked_at = now(),
    expires_at = GREATEST(revoked_sessions.expires_at, EXCLUDED.expires_at);

-- name: IsSessionRevoked :one
SELECT EXISTS (
    SELECT 1 FROM revoked_sessions WHERE user_id = $1 AND expires_at > now()
);

-- name: DeleteExpiredRevocations :execrows
DELETE FROM revoked_sessions WHERE expires_at <= now();
