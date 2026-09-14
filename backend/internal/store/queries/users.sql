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
