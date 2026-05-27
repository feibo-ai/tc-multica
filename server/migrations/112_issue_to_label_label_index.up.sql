-- Reverse-lookup index for issue_to_label. The PK (issue_id, label_id)
-- covers issue→label seeks ("what labels does this issue have"), but
-- a label-name-driven issue filter does the opposite — given a label,
-- find every issue carrying it. Without a label_id index, this falls
-- back to a sequential scan or a wide PK range scan, which gets
-- expensive on workspaces with thousands of issues × dozens of labels.
--
-- Used by the new ListIssuesByLabelNames{Any,All} queries that back
-- GET /api/issues?labels=foo,bar[&labels_mode=any|all]. The existing
-- issue_label_workspace_name_lower_idx (created in migration 059)
-- already covers the workspace + lower(name) lookup; this index
-- closes the remaining gap on the join side.

CREATE INDEX IF NOT EXISTS idx_issue_to_label_label_id
    ON issue_to_label(label_id);
