-- Storage for `ambient` token usage: local Claude Code (and future CLI)
-- sessions that ran on a registered daemon's machine but were NEVER
-- dispatched through agent_task_queue. Today these are invisible to every
-- report (see GetRuntimeUsage's self-described blind spot in
-- internal/handler/runtime.go) because the only usage ingestion path is
-- per-task (POST /api/daemon/tasks/{taskId}/usage → task_usage).
--
-- This is a DELIBERATE, DRI-approved fork of the task_usage_hourly rollup
-- skeleton (101/102): ambient rows have a runtime but NO task / agent /
-- issue, so they cannot ride the task_usage tables (whose rollup JOINs
-- agent_task_queue → agent → issue). A nullable-agent_id on task_usage_hourly
-- was judged worse (it splits the recompute function into two disjoint
-- paths). Hard constraint #6 ("executed-task usage = exactly one rollup") is
-- honoured because ambient is a DIFFERENT source class (no task) and the two
-- rollups are unioned at read time on runtime.owner_id; executed-task usage
-- still has exactly one rollup (task_usage_hourly), and the rollup function
-- here EXCLUDES any ambient session that also appears as a task session.
--
-- Naming note: the name `runtime_usage` is intentionally avoided — it was used
-- by 013, dropped by 046, and the `runtime_usage.sql` query file has since
-- been repurposed to read task_usage_hourly. `ambient_usage` is a fresh
-- namespace.

-- ---------------------------------------------------------------------------
-- Raw table. One row per (deduped) assistant message that reported usage.
-- ---------------------------------------------------------------------------
--
-- Privacy doctrine (decisions/2026-06-03-local-log-privacy.md): this table
-- holds NUMBERS AND IDS ONLY. There is deliberately no column that could carry
-- message content, prompt text, or tool I/O. Adding one would be a visible,
-- reviewable schema diff.
CREATE TABLE ambient_usage (
    workspace_id        UUID        NOT NULL,
    runtime_id          UUID        NOT NULL,
    session_id          TEXT        NOT NULL,   -- Claude session id; dedup + task-exclusion anchor
    message_id          TEXT        NOT NULL,   -- assistant message.id; per-message dedup (坑#1)
    request_id          TEXT        NOT NULL,   -- transcript requestId; per-request dedup (坑#1)
    provider            TEXT        NOT NULL,   -- e.g. 'claude'
    model               TEXT        NOT NULL,
    event_at            TIMESTAMPTZ NOT NULL,   -- message timestamp, used for the UTC hour bucket
    input_tokens        BIGINT      NOT NULL DEFAULT 0,
    output_tokens       BIGINT      NOT NULL DEFAULT 0,
    cache_read_tokens   BIGINT      NOT NULL DEFAULT 0,
    cache_write_tokens  BIGINT      NOT NULL DEFAULT 0,
    source              TEXT        NOT NULL,   -- collector adapter id (pluggable Collector framework)
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),  -- ingestion time; the rollup watermark axis

    -- ONE key, TWO jobs (坑#1):
    --   (a) cross-scan idempotency — re-scanning the same file re-inserts the
    --       same tuples, ON CONFLICT DO NOTHING makes that a no-op.
    --   (b) in-file dedup — a transcript can repeat the SAME (message.id,
    --       requestId) line up to ~33x (empirically, 955 transcripts); naive
    --       SUM over them inflates usage ~3.46x vs ccusage. This key collapses
    --       them to the first-arrival row.
    -- workspace_id is omitted from the key: runtime_id already determines it.
    CONSTRAINT uq_ambient_usage_key
        UNIQUE (runtime_id, provider, model, session_id, message_id, request_id)
);

-- Watermark scan: the rollup's "recently ingested" discovery walks
-- created_at ∈ [from, to). Mirrors task_usage's updated_at watermark.
CREATE INDEX idx_ambient_usage_created_at
    ON ambient_usage (created_at);

-- Task-exclusion self-heal: when a task completes and writes
-- agent_task_queue.session_id, the atq trigger (migration 115) re-enqueues
-- the ambient buckets for that session so the next tick drops the now-double
-- -counted rows. That reverse lookup is `WHERE session_id = NEW.session_id`.
CREATE INDEX idx_ambient_usage_session_id
    ON ambient_usage (session_id);

-- Recompute aggregation: the rollup re-sums all ambient rows for a dirty key
-- (runtime_id, provider, model) within an hour bucket of event_at.
CREATE INDEX idx_ambient_usage_rollup
    ON ambient_usage (runtime_id, provider, model, event_at);

-- The rollup excludes ambient sessions that also ran as a dispatched task via
-- an anti-join on agent_task_queue.session_id. agent_task_queue had no index
-- on session_id (it was only ever read by id); add a partial one so the
-- anti-join does not seq-scan the queue every tick. Partial because the
-- column is NULL for every task that never reported a session.
CREATE INDEX idx_agent_task_queue_session_id
    ON agent_task_queue (session_id)
    WHERE session_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Hourly rollup table. UTC buckets, same tz-neutral rationale as 101: viewer
-- tz is applied at query time via DATE(bucket_hour AT TIME ZONE @tz).
-- Key carries runtime + provider + model (no agent / project — ambient has
-- neither). person = runtime.owner_id is resolved by JOIN at read time, not
-- materialised here, so a runtime re-attribution does not require a backfill.
-- ---------------------------------------------------------------------------
CREATE TABLE ambient_usage_hourly (
    bucket_hour         TIMESTAMPTZ NOT NULL,   -- UTC, truncated to hour boundary
    workspace_id        UUID        NOT NULL,
    runtime_id          UUID        NOT NULL,
    provider            TEXT        NOT NULL,
    model               TEXT        NOT NULL,
    input_tokens        BIGINT      NOT NULL DEFAULT 0,
    output_tokens       BIGINT      NOT NULL DEFAULT 0,
    cache_read_tokens   BIGINT      NOT NULL DEFAULT 0,
    cache_write_tokens  BIGINT      NOT NULL DEFAULT 0,
    event_count         BIGINT      NOT NULL DEFAULT 0,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_ambient_usage_hourly_key
        UNIQUE (bucket_hour, workspace_id, runtime_id, provider, model)
);

-- Per-person / workspace read: UNION with task_usage_hourly groups by
-- (workspace_id, runtime→owner_id). workspace_id leads so the dashboard's
-- "last N days" walk is index-covered.
CREATE INDEX idx_ambient_usage_hourly_workspace_time
    ON ambient_usage_hourly (workspace_id, bucket_hour DESC);

-- Runtime-detail read (per-runtime trend), mirrors 101's runtime index.
CREATE INDEX idx_ambient_usage_hourly_runtime_time
    ON ambient_usage_hourly (runtime_id, bucket_hour DESC);

-- ---------------------------------------------------------------------------
-- Single-row watermark state. Same shape as 101's
-- task_usage_hourly_rollup_state — a SMALLINT(1) PK enforces "exactly one row".
-- ---------------------------------------------------------------------------
CREATE TABLE ambient_usage_hourly_rollup_state (
    id                    SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    watermark_at          TIMESTAMPTZ NOT NULL DEFAULT '1970-01-01 00:00:00+00',
    last_run_started_at   TIMESTAMPTZ,
    last_run_finished_at  TIMESTAMPTZ,
    last_run_rows         BIGINT      NOT NULL DEFAULT 0,
    last_error            TEXT
);
INSERT INTO ambient_usage_hourly_rollup_state (id) VALUES (1) ON CONFLICT DO NOTHING;

-- ---------------------------------------------------------------------------
-- Dirty queue. The created_at watermark catches newly-ingested rows once; it
-- CANNOT catch the task-exclusion self-heal (a row already past the watermark
-- whose session_id only later appears in agent_task_queue). The atq trigger in
-- 115 enqueues those buckets here so the next tick re-sums them. Same TTL /
-- drain discipline as 101's task_usage_hourly_dirty.
-- ---------------------------------------------------------------------------
CREATE TABLE ambient_usage_hourly_dirty (
    bucket_hour   TIMESTAMPTZ NOT NULL,
    workspace_id  UUID        NOT NULL,
    runtime_id    UUID        NOT NULL,
    provider      TEXT        NOT NULL,
    model         TEXT        NOT NULL,
    enqueued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_ambient_usage_hourly_dirty_key
        UNIQUE (bucket_hour, workspace_id, runtime_id, provider, model)
);

CREATE INDEX idx_ambient_usage_hourly_dirty_enqueued_at
    ON ambient_usage_hourly_dirty (enqueued_at);
