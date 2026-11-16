-- name: CreateUser :one
INSERT INTO users (email, password_hash, display_name, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CreateVerifiedUser :one
INSERT INTO users (email, password_hash, display_name, role, email_verified)
VALUES ($1, $2, $3, $4, true)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1 AND deleted_at IS NULL;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: VerifyUserEmail :one
UPDATE users
SET email_verified = true,
    updated_at = now()
WHERE id = $1
  AND deleted_at IS NULL
RETURNING *;

-- name: UserActive :one
SELECT EXISTS (SELECT 1 FROM users WHERE id = $1 AND deleted_at IS NULL);

-- name: UserRowExists :one
SELECT EXISTS (SELECT 1 FROM users WHERE id = $1);

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = $1;

-- name: UpdateUserDatingProfile :one
UPDATE users
SET dating_styles = $2,
    phases_strong = $3,
    phases_working_on = $4,
    dating_preferences = $5,
    updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: RecordSessionRevocation :exec
-- Never moves a cutoff back, so a delayed write cannot re-admit tokens.
UPDATE users
SET sessions_revoked_at = GREATEST(COALESCE(sessions_revoked_at, '-infinity'::timestamptz), @revoked_at::timestamptz)
WHERE id = @id;

-- name: ListSessionRevocationsSince :many
SELECT id, sessions_revoked_at
FROM users
WHERE sessions_revoked_at > @since::timestamptz;

-- name: LockUserForRefresh :one
-- Serialises a refresh-token rotation against a concurrent reuse revocation,
-- which updates the same row before deleting the user's refresh tokens.
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE;
