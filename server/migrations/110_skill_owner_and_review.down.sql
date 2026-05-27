DROP INDEX IF EXISTS idx_skill_last_reviewed_at;
DROP INDEX IF EXISTS idx_skill_owner_user_id;
ALTER TABLE skill
    DROP COLUMN IF EXISTS last_reviewed_at,
    DROP COLUMN IF EXISTS owner_user_id;
