-- Rollup pipeline for `ambient_usage` → `ambient_usage_hourly`. Structurally
-- parallel to 102 (the task_usage_hourly pipeline) — same advisory-lock +
-- watermark + dirty-drain + capped-window + deleted_empty control skeleton —
-- but the recompute BODY is new:
--   * source is the standalone `ambient_usage` table, which already carries
--     workspace_id + runtime_id, so there is NO agent_task_queue → agent →
--     issue join to resolve attribution (the whole reason ambient can't ride
--     task_usage_hourly).
--   * the recompute EXCLUDES any ambient session that also appears as a
--     dispatched-task session (anti-join on agent_task_queue.session_id) so a
--     mounted run's transcript — which the Claude adapter also scans, since it
--     lives under ~/.claude/projects — is counted by task_usage_hourly ONLY,
--     never twice (坑#2). The exclusion is SERVER-AUTHORITATIVE: the daemon
--     does not persist executed session ids across restarts, so it cannot be
--     trusted to skip them; the daemon-side skip is best-effort only.
--
-- IDEMPOTENCY CONTRACT (same as 101/102): for every dirty key this REPLACES
-- the hourly row with the SUM of all (non-excluded) ambient rows for that key.
-- It does not delta. Re-running an overlapping window converges to the same
-- state — which is what makes the in-flight double-count window self-heal:
-- while a mounted task is running its session_id is not yet on agent_task_queue
-- (it is written on complete/fail), so an ambient row scanned mid-run is not
-- yet excluded and the bucket may briefly include it; when the task finishes
-- and writes session_id, the atq trigger below re-enqueues the bucket and the
-- next tick re-sums it WITH the exclusion, dropping the double count.
--
-- bucket grain: HOUR, boundary always UTC. Reuses task_usage_hour_bucket()
-- from 102 deliberately — a second copy of that subtle double-AT-TIME-ZONE
-- expression could drift and split the bucket key between the trigger and the
-- recompute. One canonical helper, no drift.

-- ---------------------------------------------------------------------------
-- Trigger: agent_task_queue session-membership changes re-roll ambient buckets.
--
-- This is the ONLY external invalidation ambient has. ambient_usage itself is
-- insert-only (ON CONFLICT DO NOTHING — see the adapter), so the created_at
-- watermark catches every new row exactly once; it CANNOT catch a row whose
-- session_id only later joins agent_task_queue. That self-heal is this trigger.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION enqueue_ambient_usage_hourly_dirty_for_atq_session()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    -- FailAgentTask writes session_id = COALESCE(@session_id, session_id) on
    -- every fail, so UPDATE OF session_id fires even when the value is
    -- unchanged. Skip those no-ops.
    IF TG_OP = 'UPDATE' AND NEW.session_id IS NOT DISTINCT FROM OLD.session_id THEN
        RETURN NEW;
    END IF;

    -- OLD side (UPDATE/DELETE): a task LOST or CHANGED its session. The old
    -- session's ambient buckets must RE-include their rows now that the task
    -- copy no longer excludes them.
    IF (TG_OP = 'UPDATE' OR TG_OP = 'DELETE') AND OLD.session_id IS NOT NULL THEN
        INSERT INTO ambient_usage_hourly_dirty (
            bucket_hour, workspace_id, runtime_id, provider, model
        )
        SELECT DISTINCT
            task_usage_hour_bucket(au.event_at),
            au.workspace_id, au.runtime_id, au.provider, au.model
          FROM ambient_usage au
         WHERE au.session_id = OLD.session_id
        ON CONFLICT ON CONSTRAINT uq_ambient_usage_hourly_dirty_key DO UPDATE
            SET enqueued_at = GREATEST(ambient_usage_hourly_dirty.enqueued_at, EXCLUDED.enqueued_at);
    END IF;

    -- NEW side (INSERT/UPDATE): a task GAINED a session. That session's ambient
    -- buckets must drop the now-double-counted rows.
    IF (TG_OP = 'INSERT' OR TG_OP = 'UPDATE') AND NEW.session_id IS NOT NULL THEN
        INSERT INTO ambient_usage_hourly_dirty (
            bucket_hour, workspace_id, runtime_id, provider, model
        )
        SELECT DISTINCT
            task_usage_hour_bucket(au.event_at),
            au.workspace_id, au.runtime_id, au.provider, au.model
          FROM ambient_usage au
         WHERE au.session_id = NEW.session_id
        ON CONFLICT ON CONSTRAINT uq_ambient_usage_hourly_dirty_key DO UPDATE
            SET enqueued_at = GREATEST(ambient_usage_hourly_dirty.enqueued_at, EXCLUDED.enqueued_at);
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER trg_atq_session_ambient_dirty
AFTER INSERT OR UPDATE OF session_id OR DELETE ON agent_task_queue
FOR EACH ROW EXECUTE FUNCTION enqueue_ambient_usage_hourly_dirty_for_atq_session();

-- ---------------------------------------------------------------------------
-- Window function. Mirrors 102's structure:
--   1. Discover dirty keys: created_at watermark window ∪ the dirty queue.
--   2. Recompute each from raw ambient_usage, EXCLUDING task sessions.
--   3. Upsert; delete buckets that recomputed to nothing.
--   4. Drain queue rows enqueued before p_to.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION rollup_ambient_usage_hourly_window(
    p_from TIMESTAMPTZ,
    p_to   TIMESTAMPTZ
)
RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_rows BIGINT;
BEGIN
    IF p_from >= p_to THEN
        RETURN 0;
    END IF;

    WITH
    dirty_from_updates AS (
        SELECT DISTINCT
            task_usage_hour_bucket(au.event_at) AS bucket_hour,
            au.workspace_id                     AS workspace_id,
            au.runtime_id                       AS runtime_id,
            au.provider                         AS provider,
            au.model                            AS model
          FROM ambient_usage au
         WHERE au.created_at >= p_from
           AND au.created_at <  p_to
    ),
    dirty_from_queue AS (
        SELECT bucket_hour, workspace_id, runtime_id, provider, model
          FROM ambient_usage_hourly_dirty
         WHERE enqueued_at < p_to
    ),
    dirty_keys AS (
        SELECT * FROM dirty_from_updates
        UNION
        SELECT * FROM dirty_from_queue
    ),
    recomputed AS (
        SELECT
            dk.bucket_hour,
            dk.workspace_id,
            dk.runtime_id,
            dk.provider,
            dk.model,
            SUM(au.input_tokens)::bigint       AS input_tokens,
            SUM(au.output_tokens)::bigint      AS output_tokens,
            SUM(au.cache_read_tokens)::bigint  AS cache_read_tokens,
            SUM(au.cache_write_tokens)::bigint AS cache_write_tokens,
            COUNT(*)::bigint                   AS event_count
          FROM dirty_keys dk
          JOIN ambient_usage au
            ON au.workspace_id = dk.workspace_id
           AND au.runtime_id   = dk.runtime_id
           AND au.provider     = dk.provider
           AND au.model        = dk.model
           AND task_usage_hour_bucket(au.event_at) = dk.bucket_hour
          -- Anti-join: drop any ambient session that also ran as a dispatched
          -- task (its usage is already in task_usage_hourly). Server-side and
          -- authoritative; see 坑#2 above.
          LEFT JOIN agent_task_queue atq ON atq.session_id = au.session_id
         WHERE atq.session_id IS NULL
         GROUP BY 1, 2, 3, 4, 5
    ),
    upserted AS (
        INSERT INTO ambient_usage_hourly AS d (
            bucket_hour, workspace_id, runtime_id, provider, model,
            input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
            event_count
        )
        SELECT
            bucket_hour, workspace_id, runtime_id, provider, model,
            input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
            event_count
          FROM recomputed
        ON CONFLICT ON CONSTRAINT uq_ambient_usage_hourly_key DO UPDATE
            SET input_tokens       = EXCLUDED.input_tokens,
                output_tokens      = EXCLUDED.output_tokens,
                cache_read_tokens  = EXCLUDED.cache_read_tokens,
                cache_write_tokens = EXCLUDED.cache_write_tokens,
                event_count        = EXCLUDED.event_count,
                updated_at         = now()
        RETURNING 1
    ),
    deleted_empty AS (
        DELETE FROM ambient_usage_hourly d
         USING dirty_keys dk
         WHERE d.bucket_hour  = dk.bucket_hour
           AND d.workspace_id = dk.workspace_id
           AND d.runtime_id   = dk.runtime_id
           AND d.provider     = dk.provider
           AND d.model        = dk.model
           AND NOT EXISTS (
               SELECT 1 FROM recomputed r
                WHERE r.bucket_hour  = dk.bucket_hour
                  AND r.workspace_id = dk.workspace_id
                  AND r.runtime_id   = dk.runtime_id
                  AND r.provider     = dk.provider
                  AND r.model        = dk.model
           )
        RETURNING 1
    )
    SELECT (SELECT COUNT(*) FROM upserted) + (SELECT COUNT(*) FROM deleted_empty)
      INTO v_rows;

    DELETE FROM ambient_usage_hourly_dirty WHERE enqueued_at < p_to;

    RETURN v_rows;
END;
$$;

-- Dirty-queue TTL. Belt-and-braces for rows that escape a tick (crash mid-tick,
-- worker paused). Same 7-day rationale as 102.
CREATE OR REPLACE FUNCTION prune_ambient_usage_hourly_dirty(
    p_retention INTERVAL DEFAULT INTERVAL '7 days'
)
RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_rows BIGINT;
BEGIN
    DELETE FROM ambient_usage_hourly_dirty
     WHERE enqueued_at < now() - p_retention;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    RETURN v_rows;
END;
$$;

-- Cron entry. Advisory lock id 4247 — distinct from task_usage_hourly's 4246
-- (and 4242 / 4244 used elsewhere) so an ambient tick serialises only against
-- other ambient ticks, never against the task_usage rollup.
CREATE OR REPLACE FUNCTION rollup_ambient_usage_hourly()
RETURNS BIGINT
LANGUAGE plpgsql
AS $$
DECLARE
    v_lock_ok BOOLEAN;
    v_from    TIMESTAMPTZ;
    v_to      TIMESTAMPTZ;
    v_rows    BIGINT := 0;
BEGIN
    SELECT pg_try_advisory_lock(4247) INTO v_lock_ok;
    IF NOT v_lock_ok THEN
        RETURN 0;
    END IF;

    BEGIN
        UPDATE ambient_usage_hourly_rollup_state
           SET last_run_started_at = now(),
               last_error          = NULL
         WHERE id = 1
        RETURNING watermark_at INTO v_from;

        -- Cap each tick at a one-day window (watermark axis = created_at). Same
        -- catch-up bound as 102: a paused worker advances in one-day steps over
        -- successive ticks instead of recomputing weeks under the lock.
        v_to := LEAST(now() - INTERVAL '5 minutes', v_from + INTERVAL '1 day');

        IF v_from < v_to THEN
            v_rows := rollup_ambient_usage_hourly_window(v_from, v_to);

            UPDATE ambient_usage_hourly_rollup_state
               SET watermark_at         = v_to,
                   last_run_finished_at = now(),
                   last_run_rows        = v_rows
             WHERE id = 1;
        ELSE
            UPDATE ambient_usage_hourly_rollup_state
               SET last_run_finished_at = now(),
                   last_run_rows        = 0
             WHERE id = 1;
        END IF;

        PERFORM pg_advisory_unlock(4247);
    EXCEPTION WHEN OTHERS THEN
        UPDATE ambient_usage_hourly_rollup_state
           SET last_error           = SQLERRM,
               last_run_finished_at = now()
         WHERE id = 1;
        PERFORM pg_advisory_unlock(4247);
        RAISE;
    END;

    -- TTL prune runs unlocked (plain bounded DELETE, idempotent).
    PERFORM prune_ambient_usage_hourly_dirty();
    RETURN v_rows;
END;
$$;

-- Health-check helper — same shape as 102's lag helper.
CREATE OR REPLACE FUNCTION ambient_usage_hourly_rollup_lag_seconds()
RETURNS DOUBLE PRECISION
LANGUAGE sql
STABLE
AS $$
    SELECT EXTRACT(EPOCH FROM (now() - last_run_finished_at))
      FROM ambient_usage_hourly_rollup_state
     WHERE id = 1;
$$;
