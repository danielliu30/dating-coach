-- Coaching-brain pillars the user wants to improve on ("agency", "feedback",
-- "support"); message analyses score each one. Distinct from the free-text
-- dating_preferences. Empty until the user picks some.
ALTER TABLE users
    ADD COLUMN coaching_goals text[] NOT NULL DEFAULT '{}';
