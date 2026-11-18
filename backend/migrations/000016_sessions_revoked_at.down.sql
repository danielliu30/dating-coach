DROP INDEX IF EXISTS users_sessions_revoked_at_idx;
ALTER TABLE users DROP COLUMN IF EXISTS sessions_revoked_at;
