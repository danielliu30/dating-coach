-- Self-reported dating profile. Styles are where the user meets people (apps
-- and in person); the two phase columns split the stages of a dating flow into
-- the ones they feel strong in and the ones they are working on. The API keeps
-- the vocabularies and keeps a phase out of both columns at once.
ALTER TABLE users
    ADD COLUMN dating_styles     text[] NOT NULL DEFAULT '{}',
    ADD COLUMN phases_strong     text[] NOT NULL DEFAULT '{}',
    ADD COLUMN phases_working_on text[] NOT NULL DEFAULT '{}';
