DROP INDEX IF EXISTS idx_project_dri_user_id;
ALTER TABLE project DROP COLUMN IF EXISTS dri_user_id;
