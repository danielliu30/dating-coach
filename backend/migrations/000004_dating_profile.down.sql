ALTER TABLE users
    DROP COLUMN IF EXISTS dating_styles,
    DROP COLUMN IF EXISTS phases_strong,
    DROP COLUMN IF EXISTS phases_working_on;
