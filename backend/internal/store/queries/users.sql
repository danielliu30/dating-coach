-- name: CreateUser :one
INSERT INTO users (email, password_hash, display_name, role, verification_token, verification_expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1 AND deleted_at IS NULL;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: VerifyUserEmail :one
UPDATE users
SET email_verified = true,
    verification_token = NULL,
    verification_expires_at = NULL,
    updated_at = now()
WHERE verification_token = $1
  AND verification_expires_at > now()
  AND deleted_at IS NULL
RETURNING *;

-- name: SetVerificationToken :exec
UPDATE users
SET verification_token = $2,
    verification_expires_at = $3,
    updated_at = now()
WHERE id = $1;

-- name: MarkUserDeleted :execrows
UPDATE users
SET deleted_at = COALESCE(deleted_at, now()),
    updated_at = now()
WHERE id = $1;

-- name: UserRowExists :one
SELECT EXISTS (SELECT 1 FROM users WHERE id = $1);

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = $1;
