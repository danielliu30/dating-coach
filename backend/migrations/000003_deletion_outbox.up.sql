-- Outbox for account deletions. DELETE /me marks the account and records the
-- work here in the same statement, so the database is the point a deletion
-- becomes certain: a broker that is down can no longer leave behind an account
-- that is unusable and that nothing is going to delete.
--
-- There is deliberately no foreign key to users: the row is the instruction to
-- delete that user, so it has to outlive the row it removes. The worker deletes
-- it once the deletion has been applied.
CREATE TABLE account_deletions (
    user_id      uuid PRIMARY KEY,
    requested_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

-- The relay only ever reads the deletions that still have to be queued.
CREATE INDEX account_deletions_pending_idx
    ON account_deletions (requested_at)
    WHERE published_at IS NULL;
