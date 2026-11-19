CREATE TABLE revoked_refresh_tokens (
    token_hash text PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);

CREATE INDEX revoked_refresh_tokens_expires_at_idx ON revoked_refresh_tokens (expires_at);
