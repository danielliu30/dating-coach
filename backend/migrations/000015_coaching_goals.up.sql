-- Selected coaching-brain pillar keys; empty by default.
ALTER TABLE users
    ADD COLUMN coaching_goals text[] NOT NULL DEFAULT '{}';
