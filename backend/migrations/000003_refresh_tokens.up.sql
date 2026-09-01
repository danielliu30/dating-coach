-- Refresh tokens are the only long-lived credential: access tokens are JWTs
-- checked by signature alone, so nothing can withdraw one before it expires,
-- and revocation therefore happens here, on the exchange that mints them.
--
-- Only the SHA-256 of the token is stored, so a dump of this table cannot be
-- replayed against the API.
--
-- family_id ties a token to the chain of rotations it descends from, which is
-- the unit of compromise: presenting an already-spent token means the chain
-- leaked, and every token in it has to go, while other sessions of the same
-- user are unaffected.
CREATE TABLE refresh_tokens (
    token_hash text PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family_id  uuid NOT NULL,
    issued_at  timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);

CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_expires_at_idx ON refresh_tokens (expires_at);
