-- project.start_date / due_date mirror the issue-level fields: a project can
-- express a planned start as well as a deadline, feeding the Project Gantt view
-- and the project date pickers. Stored as DATE (no time, no timezone) so a
-- picked calendar day is preserved as-is for every viewer, matching the issue
-- columns after migration 112. Projects have no historical TIMESTAMPTZ rows, so
-- the column is created as DATE directly — no two-step add-then-convert needed.
ALTER TABLE project ADD COLUMN start_date DATE;
ALTER TABLE project ADD COLUMN due_date DATE;
