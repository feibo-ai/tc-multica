-- TEA-113 fleet one-click update audit (REV-5 mini-ADR). The
-- fleet_update_audit row is a single row per per-runtime update: the (A)
-- trigger-fact columns are written once at create time; the (B) result columns
-- (report_status / report_source / reported_at) are filled later, exactly once,
-- by whichever of daemon-report (INV-13) or server-timeout sweep (INV-14)
-- arrives first. The `report_status IS NULL` guard on every (B) writer is the
-- write-exclusion / idempotency anchor — first terminal writer wins.

-- name: InsertFleetUpdateTrigger :exec
-- (A) Record the non-repudiable trigger fact. Called once per runtime, right
-- after UpdateStore.Create succeeds, inside the fleet self-check handler.
-- update_id is the ephemeral UpdateStore request id and the PK / join key.
-- force is audit/log only (INV-2). target_version is server-filled from the
-- authoritative latest release (INV-1).
INSERT INTO fleet_update_audit (
    update_id,
    workspace_id,
    runtime_id,
    user_id,
    target_version,
    force,
    triggered_at
) VALUES (
    $1, $2, $3, $4, $5, $6, now()
);

-- name: UpsertFleetUpdateResult :execrows
-- (B) Record the daemon-reported terminal result (INV-13). Keyed by update_id.
-- The `report_status IS NULL` guard makes this idempotent and mutually
-- exclusive with the server-timeout sweep: whoever writes the terminal row
-- first wins; a later daemon补报 or a racing sweep matches 0 rows. report_source
-- is fixed to 'daemon-reported' here. Returns rows affected so the caller can
-- tell whether this report actually landed the terminal row.
UPDATE fleet_update_audit
SET report_status = @report_status,
    report_source = 'daemon-reported',
    reported_at   = now()
WHERE update_id = @update_id
  AND report_status IS NULL;

-- name: SweepTimedOutFleetUpdates :many
-- (B) Server-side timeout flush (INV-14). Scans the persistent audit table
-- itself — NOT the ephemeral UpdateStore — so it is store-agnostic and free of
-- the multi-node RedisUpdateStore TTL-eviction race. Selects (A) rows whose
-- trigger is older than the timeout threshold and that still have no (B)
-- terminal row, capped per tick, and flushes report_status='timeout' /
-- report_source='server-timeout'. The `report_status IS NULL` guard keeps it
-- mutually exclusive with a concurrent daemon report. Returns the swept rows
-- for logging / event purposes.
WITH stale AS (
    SELECT update_id
    FROM fleet_update_audit
    WHERE report_status IS NULL
      AND triggered_at < now() - make_interval(secs => @timeout_secs::float8)
    ORDER BY triggered_at ASC
    LIMIT @max_per_tick::int
    FOR UPDATE SKIP LOCKED
)
UPDATE fleet_update_audit a
SET report_status = 'timeout',
    report_source = 'server-timeout',
    reported_at   = now()
FROM stale
WHERE a.update_id = stale.update_id
  AND a.report_status IS NULL
RETURNING a.update_id, a.workspace_id, a.runtime_id;

-- name: ListFleetUpdateAuditByWorkspace :many
-- Authority source for the fleet progress回显 (INV-6): per-runtime terminal
-- state is read from this persistent table, not from the ephemeral
-- UpdateStore. Newest triggers first, bounded by a recent window so the
-- progress panel only aggregates the latest fleet run(s).
SELECT update_id,
       workspace_id,
       runtime_id,
       user_id,
       target_version,
       force,
       triggered_at,
       report_status,
       report_source,
       reported_at
FROM fleet_update_audit
WHERE workspace_id = @workspace_id
  AND triggered_at >= now() - make_interval(secs => @window_secs::float8)
ORDER BY triggered_at DESC;
