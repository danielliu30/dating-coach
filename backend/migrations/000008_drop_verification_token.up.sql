ALTER TABLE users
    DROP COLUMN IF EXISTS verification_token,
    DROP COLUMN IF EXISTS verification_expires_at;
