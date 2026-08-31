-- Session revocations, kept in PostgreSQL so they survive a restart of any
-- cache. An entry lasts until every token minted before it has expired on its
-- own; after that it can no longer make a difference and is purged.
--
-- There is deliberately no foreign key to users: a revoked account's rows are
-- removed asynchronously, and the revocation has to keep refusing the tokens
-- that outlive that removal.
CREATE TABLE revoked_sessions (
    user_id    uuid PRIMARY KEY,
    revoked_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX revoked_sessions_expires_at_idx ON revoked_sessions (expires_at);
