DROP INDEX IF EXISTS coaches_approval_status_idx;
ALTER TABLE coaches DROP COLUMN IF EXISTS approval_status;
