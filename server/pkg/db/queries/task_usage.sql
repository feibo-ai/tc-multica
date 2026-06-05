-- name: UpsertTaskUsage :exec
-- Bumps `updated_at` on INSERT and on conflict so the hourly-rollup worker
-- detects the row as dirty and re-aggregates its bucket.
-- Without the conflict-side bump, a correction to historical token counts
-- would never propagate to the rollup.
INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now())
ON CONFLICT (task_id, provider, model)
DO UPDATE SET
    input_tokens = EXCLUDED.input_tokens,
    output_tokens = EXCLUDED.output_tokens,
    cache_read_tokens = EXCLUDED.cache_read_tokens,
    cache_write_tokens = EXCLUDED.cache_write_tokens,
    updated_at = now();

-- name: GetTaskUsage :many
SELECT * FROM task_usage
WHERE task_id = $1
ORDER BY model;

-- name: GetIssueUsageSummary :one
SELECT
    COALESCE(SUM(tu.input_tokens), 0)::bigint AS total_input_tokens,
    COALESCE(SUM(tu.output_tokens), 0)::bigint AS total_output_tokens,
    COALESCE(SUM(tu.cache_read_tokens), 0)::bigint AS total_cache_read_tokens,
    COALESCE(SUM(tu.cache_write_tokens), 0)::bigint AS total_cache_write_tokens,
    COUNT(DISTINCT tu.task_id)::int AS task_count
FROM task_usage tu
JOIN agent_task_queue atq ON atq.id = tu.task_id
WHERE atq.issue_id = $1;

-- name: ListDashboardUsageDaily :many
-- Daily per-(date, model) token aggregates for the workspace, served
-- from the UTC-bucketed `task_usage_hourly` table and
-- sliced to calendar days under the caller-supplied @tz. Optionally
-- scoped to a single project via sqlc.narg('project_id'). Powers the
-- workspace dashboard's daily cost chart.
-- The viewer's tz is applied here at query time, so a viewer in
-- Asia/Shanghai gets their "today" cut at +08 and one in
-- America/Los_Angeles gets theirs at -08 against the same UTC rows.
--
-- @since is already the viewer's local start-of-day-(N) as a UTC
-- instant (computed by parseSinceParamInTZ). It must NOT be re-truncated
-- with DATE_TRUNC here — DATE_TRUNC operates in the session tz and would
-- snap the cutoff back to UTC midnight, dragging in an extra partial
-- local day for any non-UTC viewer.
SELECT
    DATE(bucket_hour AT TIME ZONE sqlc.arg('tz')::text) AS date,
    model,
    SUM(input_tokens)::bigint        AS input_tokens,
    SUM(output_tokens)::bigint       AS output_tokens,
    SUM(cache_read_tokens)::bigint   AS cache_read_tokens,
    SUM(cache_write_tokens)::bigint  AS cache_write_tokens,
    SUM(task_count)::int             AS task_count
FROM task_usage_hourly
WHERE workspace_id = $1
  AND bucket_hour >= sqlc.arg('since')::timestamptz
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id'))
GROUP BY DATE(bucket_hour AT TIME ZONE sqlc.arg('tz')::text), model
ORDER BY DATE(bucket_hour AT TIME ZONE sqlc.arg('tz')::text) DESC, model;

-- name: ListDashboardUsageByAgent :many
-- Per-(agent, model) token aggregates from `task_usage_hourly`. No
-- date grouping in the result, so this query takes no `@tz` — the
-- @since cutoff is a raw timestamptz the Go layer has already computed
-- in the viewer's tz. Model dimension is preserved so the client can
-- compute cost from its per-model pricing table; the client folds rows
-- by agent for the "by agent" list on the dashboard.
--
-- task_count is summed across hourly buckets — one task that spans
-- multiple hours lands in multiple buckets, so this over-counts by
-- hour the same way the daily version over-counted by day. The
-- frontend prefers `ListDashboardAgentRunTime` for the user-facing
-- "tasks" column, so this stays informational only.
SELECT
    agent_id,
    model,
    SUM(input_tokens)::bigint        AS input_tokens,
    SUM(output_tokens)::bigint       AS output_tokens,
    SUM(cache_read_tokens)::bigint   AS cache_read_tokens,
    SUM(cache_write_tokens)::bigint  AS cache_write_tokens,
    SUM(task_count)::int             AS task_count
FROM task_usage_hourly
WHERE workspace_id = $1
  AND bucket_hour >= @since::timestamptz
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id'))
GROUP BY agent_id, model
ORDER BY agent_id, model;

-- name: ListDashboardUsageByPerson :many
-- Per-person token totals for the workspace. This is the read half of the
-- runtime-token-usage feature: it UNIONs the two usage sources so one person's
-- number includes BOTH their agents' mounted-task usage (task_usage_hourly) AND
-- their own ad-hoc local CLI sessions never dispatched as tasks
-- (ambient_usage_hourly). Both attribute to the runtime owner.
--
-- ambient_tokens carries ONLY the local-CLI portion of the total so the UI can
-- surface "includes local CLI" without a second query.
--
-- owner_id is nullable AND the join to agent_runtime is a LEFT JOIN: a runtime
-- with no resolved owner — OR one hard-deleted by the offline-runtime GC
-- (DeleteStaleOfflineRuntimes; an ambient-only local-CLI machine that went quiet
-- for the TTL is exactly its profile) — aggregates into the NULL "unattributed"
-- bucket instead of having its usage dropped. An INNER JOIN here would make a
-- deleted runtime's ambient history vanish from the dashboard, defeating the
-- feature via the project's own GC. NULL renders as "Unattributed" (plan A4b).
-- #6 is honoured because executed-task usage still has exactly one rollup
-- (task_usage_hourly); ambient is a different source class unioned here at read
-- time on owner_id.
--
-- No date bucket, so no @tz — @since is the viewer-local cutoff computed in Go.
-- No project filter: ambient usage has no project, so scoping the union to a
-- project would silently drop every person's local-CLI total.
WITH combined AS (
    SELECT
        rt.owner_id AS owner_id,
        tuh.input_tokens, tuh.output_tokens, tuh.cache_read_tokens, tuh.cache_write_tokens,
        0::bigint AS ambient_tokens
      FROM task_usage_hourly tuh
      LEFT JOIN agent_runtime rt ON rt.id = tuh.runtime_id
     WHERE tuh.workspace_id = $1
       AND tuh.bucket_hour >= @since::timestamptz
    UNION ALL
    SELECT
        rt.owner_id AS owner_id,
        auh.input_tokens, auh.output_tokens, auh.cache_read_tokens, auh.cache_write_tokens,
        (auh.input_tokens + auh.output_tokens + auh.cache_read_tokens + auh.cache_write_tokens)::bigint AS ambient_tokens
      FROM ambient_usage_hourly auh
      LEFT JOIN agent_runtime rt ON rt.id = auh.runtime_id
     WHERE auh.workspace_id = $1
       AND auh.bucket_hour >= @since::timestamptz
)
SELECT
    owner_id,
    SUM(input_tokens)::bigint       AS input_tokens,
    SUM(output_tokens)::bigint      AS output_tokens,
    SUM(cache_read_tokens)::bigint  AS cache_read_tokens,
    SUM(cache_write_tokens)::bigint AS cache_write_tokens,
    SUM(ambient_tokens)::bigint     AS ambient_tokens
  FROM combined
 GROUP BY owner_id
 ORDER BY (SUM(input_tokens) + SUM(output_tokens) + SUM(cache_read_tokens) + SUM(cache_write_tokens)) DESC;

-- name: ListDashboardAmbientUsageByPerson :many
-- Per-(owner, model) ambient token totals for the workspace. This is the
-- user-tab read-out: it covers ONLY ad-hoc local-CLI usage
-- (ambient_usage_hourly), never executed-task usage — the clean ambient / task
-- split (plan D2). The model dimension is preserved so the client can compute
-- cost from its per-model pricing table and then fold the rows by owner; the
-- sibling ListDashboardUsageByPerson collapses model and so can only report
-- tokens, which is exactly why this model-preserving variant exists.
--
-- owner_id is nullable AND the join is a LEFT JOIN: a runtime with no resolved
-- owner — OR one hard-deleted by the offline-runtime GC
-- (DeleteStaleOfflineRuntimes; an ambient-only local-CLI machine that went
-- quiet for the TTL) — aggregates into the NULL "unattributed" bucket
-- (rendered "" → "Unattributed", plan Q1) instead of having its usage dropped.
--
-- No date bucket, so no @tz — @since is the viewer-local cutoff computed in Go.
-- No project filter: ambient usage has no project.
SELECT
    rt.owner_id,
    auh.model,
    SUM(auh.input_tokens)::bigint        AS input_tokens,
    SUM(auh.output_tokens)::bigint       AS output_tokens,
    SUM(auh.cache_read_tokens)::bigint   AS cache_read_tokens,
    SUM(auh.cache_write_tokens)::bigint  AS cache_write_tokens
FROM ambient_usage_hourly auh
LEFT JOIN agent_runtime rt ON rt.id = auh.runtime_id
WHERE auh.workspace_id = $1
  AND auh.bucket_hour >= @since::timestamptz
GROUP BY rt.owner_id, auh.model
ORDER BY rt.owner_id, auh.model;

-- name: ListDashboardAmbientUsageDaily :many
-- Daily per-(date, model) ambient token aggregates for a SINGLE owner (or the
-- unattributed bucket), from ambient_usage_hourly sliced into calendar days
-- under the caller-supplied @tz. Powers the user-tab 26-week heatmap.
--
-- This query has NO "all owners" mode. The owner_id filter resolves to exactly
-- one of two branches:
--   * narg owner_id IS NOT NULL → that owner's rows only;
--   * narg owner_id IS NULL     → the unattributed bucket (rt.owner_id IS NULL).
-- The Go handler passes pgtype.UUID{Valid:false} for the unattributed case
-- (owner_id="" or param absent); it MUST NOT run "" through any UUID parse
-- (util.ParseUUID("") errors). Keeping the two-branch invariant explicit stops
-- "" from drifting between "unattributed bucket" and "no filter at all".
--
-- @since is already the viewer's local start-of-day-(N) (parseSinceParamInTZ)
-- — passed straight through, NOT re-truncated; see ListDashboardUsageDaily.
SELECT
    DATE(auh.bucket_hour AT TIME ZONE sqlc.arg('tz')::text) AS date,
    auh.model,
    SUM(auh.input_tokens)::bigint        AS input_tokens,
    SUM(auh.output_tokens)::bigint       AS output_tokens,
    SUM(auh.cache_read_tokens)::bigint   AS cache_read_tokens,
    SUM(auh.cache_write_tokens)::bigint  AS cache_write_tokens
FROM ambient_usage_hourly auh
LEFT JOIN agent_runtime rt ON rt.id = auh.runtime_id
WHERE auh.workspace_id = $1
  AND auh.bucket_hour >= sqlc.arg('since')::timestamptz
  AND (
        (sqlc.narg('owner_id')::uuid IS NOT NULL AND rt.owner_id = sqlc.narg('owner_id'))
     OR (sqlc.narg('owner_id')::uuid IS NULL     AND rt.owner_id IS NULL)
      )
GROUP BY DATE(auh.bucket_hour AT TIME ZONE sqlc.arg('tz')::text), auh.model
ORDER BY DATE(auh.bucket_hour AT TIME ZONE sqlc.arg('tz')::text) DESC, auh.model;

-- name: ListDashboardAgentUsageDaily :many
-- Daily per-(date, model) task token aggregates for a SINGLE agent, from
-- task_usage_hourly sliced into calendar days under @tz. Powers the agent-tab
-- 26-week heatmap. The index idx_task_usage_hourly_workspace_agent_time covers
-- (workspace_id, agent_id, bucket_hour).
--
-- @since is already the viewer's local start-of-day-(N) (parseSinceParamInTZ)
-- — passed straight through, NOT re-truncated; see ListDashboardUsageDaily.
SELECT
    DATE(bucket_hour AT TIME ZONE sqlc.arg('tz')::text) AS date,
    model,
    SUM(input_tokens)::bigint        AS input_tokens,
    SUM(output_tokens)::bigint       AS output_tokens,
    SUM(cache_read_tokens)::bigint   AS cache_read_tokens,
    SUM(cache_write_tokens)::bigint  AS cache_write_tokens
FROM task_usage_hourly
WHERE workspace_id = $1
  AND agent_id = @agent_id::uuid
  AND bucket_hour >= sqlc.arg('since')::timestamptz
GROUP BY DATE(bucket_hour AT TIME ZONE sqlc.arg('tz')::text), model
ORDER BY DATE(bucket_hour AT TIME ZONE sqlc.arg('tz')::text) DESC, model;

-- name: ListDashboardRunTimeDaily :many
-- Daily per-date run time + task counts for the workspace, optionally
-- scoped to a single project. Powers the workspace dashboard's "Time"
-- and "Tasks" metrics on the same toggle as Tokens / Cost. Bucketed by
-- completed_at (terminal time) sliced into calendar days under the
-- caller-supplied @tz — same Viewing-tz treatment as ListDashboardUsageDaily
-- so the Time / Tasks tabs cut their day boundary identically to the
-- Cost / Tokens tabs (a viewer east of UTC would otherwise see the four
-- tabs disagree on a "1d" window). Only terminal tasks (completed or
-- failed) with both started_at and completed_at populated contribute.
--
-- @since is already the viewer's local start-of-day-(N) (parseSinceParamInTZ)
-- — passed straight through, NOT re-truncated; see ListDashboardUsageDaily.
SELECT
    DATE(atq.completed_at AT TIME ZONE sqlc.arg('tz')::text) AS date,
    COALESCE(
        SUM(EXTRACT(EPOCH FROM (atq.completed_at - atq.started_at)))::bigint,
        0
    )::bigint AS total_seconds,
    COUNT(*)::int AS task_count,
    COUNT(*) FILTER (WHERE atq.status = 'failed')::int AS failed_count
FROM agent_task_queue atq
JOIN agent a ON a.id = atq.agent_id
LEFT JOIN issue i ON i.id = atq.issue_id
WHERE a.workspace_id = $1
  AND atq.status IN ('completed', 'failed')
  AND atq.started_at IS NOT NULL
  AND atq.completed_at IS NOT NULL
  AND atq.completed_at >= sqlc.arg('since')::timestamptz
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
GROUP BY DATE(atq.completed_at AT TIME ZONE sqlc.arg('tz')::text)
ORDER BY DATE(atq.completed_at AT TIME ZONE sqlc.arg('tz')::text) DESC;

-- name: ListDashboardAgentRunTime :many
-- Per-agent total task run time and task count for the workspace, optionally
-- scoped to a single project. Counts only terminal runs (completed or failed)
-- with both started_at and completed_at populated — queued/running tasks have
-- no finite duration. Anchored on completed_at so the window matches the
-- token cost window (which is anchored on tu.created_at, ~= completion time).
--
-- No date bucketing, so no @tz — but @since is the viewer's local
-- start-of-day-(N) so the "last N days" window lines up with the per-agent
-- cost card; passed straight through without re-truncation.
SELECT
    atq.agent_id,
    COALESCE(
        SUM(EXTRACT(EPOCH FROM (atq.completed_at - atq.started_at)))::bigint,
        0
    )::bigint AS total_seconds,
    COUNT(*)::int AS task_count,
    COUNT(*) FILTER (WHERE atq.status = 'failed')::int AS failed_count
FROM agent_task_queue atq
JOIN agent a ON a.id = atq.agent_id
LEFT JOIN issue i ON i.id = atq.issue_id
WHERE a.workspace_id = $1
  AND atq.status IN ('completed', 'failed')
  AND atq.started_at IS NOT NULL
  AND atq.completed_at IS NOT NULL
  AND atq.completed_at >= @since::timestamptz
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
GROUP BY atq.agent_id
ORDER BY total_seconds DESC;
