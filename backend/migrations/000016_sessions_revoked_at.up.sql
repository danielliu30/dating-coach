-- Durable copy of the sign-out / refresh-reuse cutoff kept in Redis
-- (auth:revoked:<id>). Redis is the fast path the middleware reads; this column
-- survives a Redis outage or flush, and the worker re-applies recent cutoffs to
-- Redis so a revocation written while Redis was down still takes effect.
ALTER TABLE users ADD COLUMN sessions_revoked_at timestamptz;

CREATE INDEX users_sessions_revoked_at_idx ON users (sessions_revoked_at)
    WHERE sessions_revoked_at IS NOT NULL;
