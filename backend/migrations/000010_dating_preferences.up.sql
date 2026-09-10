-- What the user says they are looking for, in their own words; both analysis
-- tracks tailor their feedback to it. Empty until the user fills it in.
ALTER TABLE users
    ADD COLUMN dating_preferences text NOT NULL DEFAULT '';
