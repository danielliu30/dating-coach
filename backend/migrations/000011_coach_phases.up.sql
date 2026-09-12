-- Structured dating-cycle phases a coach specialises in, drawn from the same
-- vocabulary as users.phases_strong / phases_working_on. The free-text
-- specialties column stays for anything outside that vocabulary.
ALTER TABLE coaches
    ADD COLUMN phases text[] NOT NULL DEFAULT '{}';
