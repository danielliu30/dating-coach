-- Refresh tokens: the long-lived half of the session. Access tokens are JWTs
-- that expire within minutes, so nothing has to be looked up to authenticate a
-- request; ending a session instead means deleting its refresh tokens, which is
-- what the API does when an account is deleted.
--
-- Only the SHA-256 hash of the token is stored, so a dump of this table cannot
-- be replayed against the API. The foreign key cascades: once the account rows
-- are gone the access tokens minted from these refresh tokens have expired on
-- their own, so nothing outlives the cascade.
CREATE TABLE refresh_tokens (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    issued_at  timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    -- Set when the token is exchanged. The row is kept until it expires so a
    -- second presentation of the same token is recognised as reuse.
    used_at    timestamptz
);

CREATE INDEX refresh_tokens_user_id_idx ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_expires_at_idx ON refresh_tokens (expires_at);
