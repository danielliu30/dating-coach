ALTER TABLE users
    ADD COLUMN verification_token text,
    ADD COLUMN verification_expires_at timestamptz;
