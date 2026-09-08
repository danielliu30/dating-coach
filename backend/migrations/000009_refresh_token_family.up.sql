-- A refresh token's family is the chain of rotations it belongs to: the token
-- issued at sign-in starts one, and every token it is exchanged for inherits it.
-- The chain is what a reuse of a spent token compromises, so it is the unit a
-- revocation can be scoped to without ending the account's other sessions.
--
-- Existing rows each become a family of their own, which is what they are: the
-- rotations before this column cannot be reconstructed.
ALTER TABLE refresh_tokens
    ADD COLUMN family_id uuid NOT NULL DEFAULT gen_random_uuid();

CREATE INDEX refresh_tokens_family_id_idx ON refresh_tokens (family_id);
