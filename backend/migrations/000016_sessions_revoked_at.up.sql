-- Replayed into Redis after an outage or flush.
ALTER TABLE users ADD COLUMN sessions_revoked_at timestamptz;

CREATE INDEX users_sessions_revoked_at_idx ON users (sessions_revoked_at)
    WHERE sessions_revoked_at IS NOT NULL;
