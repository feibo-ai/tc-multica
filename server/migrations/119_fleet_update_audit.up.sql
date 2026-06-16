-- TEA-113 fleet one-click update: persistent audit-of-record for fleet
-- self-check (nudge + force-override). One row per per-runtime update that the
-- fleet self-check endpoint created. Replaces the ephemeral UpdateStore (5min
-- TTL) as the authority for both audit (force追责) and per-runtime terminal
-- state (REV-5 mini-ADR INV-4 / INV-6 / INV-13 / INV-14).
--
-- The columns are split into two trust groups (INV-4 信任面分栏):
--
--   (A) Server-recorded, non-repudiable TRIGGER FACT. Written once at
--       Create-success time inside the fleet self-check handler. These are
--       authoritative: the server itself observed "DRI <user_id> triggered a
--       (force?) update of <runtime_id> to <target_version> at <triggered_at>".
--       force追责 relies ONLY on this group. target_version is server-filled
--       from the authoritative feibo-ai/tc-multica latest release (INV-1) —
--       never from a client-supplied version.
--
--   (B) Daemon-reported / server-swept RESULT. Written later, exactly once,
--       keyed by update_id. report_source records WHO wrote it:
--         * 'daemon-reported' — the daemon's ReportUpdateResult callback
--           (INV-13). NOT a trustworthy "this machine is actually safely
--           updated" assertion: requireDaemonRuntimeAccess only checks the
--           caller owns the runtime's workspace, not the truth of the content.
--         * 'server-timeout'  — the server-side sweep (INV-14) flushed a
--           timeout because (A) was older than the timeout threshold and no
--           (B) terminal row had landed.
--
-- The `force` bool lives in (A) ONLY and is a pure audit/log field: nothing in
-- the daemon update path or the terminal-write/sweep paths ever branches on it
-- (INV-2).

CREATE TABLE fleet_update_audit (
    -- (A) server-recorded, non-repudiable trigger fact -------------------
    -- update_id is the ephemeral UpdateStore request id. It is the join key
    -- to the (B) result and the UNIQUE write-exclusion / idempotency anchor:
    -- the first writer of a terminal (B) row wins (ON CONFLICT DO NOTHING),
    -- so daemon-report (INV-13) and server-timeout sweep (INV-14) cannot both
    -- land a terminal row for the same update.
    update_id      TEXT        PRIMARY KEY,
    workspace_id   UUID        NOT NULL,
    runtime_id     UUID        NOT NULL,
    -- user_id is the DRI who pressed the button (from the authenticated PAT /
    -- session). Non-repudiable trigger attribution. No FK to "user": this is
    -- an append-only audit row whose value is the recorded id itself; a
    -- referential lock on the hot "user" table at write time is not warranted.
    user_id        UUID        NOT NULL,
    -- target_version is server-filled from the authoritative latest release.
    target_version TEXT        NOT NULL,
    -- force is AUDIT/LOG only (INV-2). Recorded so a force-override is
    -- non-repudiable; never consulted by any control-flow branch.
    force          BOOLEAN     NOT NULL DEFAULT FALSE,
    triggered_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- (B) daemon-reported / server-swept result --------------------------
    -- All nullable: the row exists from trigger time; the result lands later.
    -- report_status ∈ {completed, failed, timeout}. report_source records the
    -- writer so the UI can distinguish 'daemon自报完成' (not a safety
    -- assertion) from a server-side timeout flush.
    report_status  TEXT,
    report_source  TEXT,
    reported_at    TIMESTAMPTZ,

    CONSTRAINT chk_fleet_update_audit_report_status
        CHECK (report_status IS NULL
               OR report_status IN ('completed', 'failed', 'timeout')),
    CONSTRAINT chk_fleet_update_audit_report_source
        CHECK (report_source IS NULL
               OR report_source IN ('daemon-reported', 'server-timeout'))
);

-- Per-workspace listing of recent triggers for the fleet progress回显 (newest
-- first). Backs the x/N aggregation the UI reads off this audit table.
CREATE INDEX idx_fleet_update_audit_ws_triggered
    ON fleet_update_audit (workspace_id, triggered_at DESC);

-- INV-14 sweep scan: find (A) rows older than the timeout threshold that still
-- have no (B) terminal row. Partial index keeps the sweep tick cheap by only
-- indexing rows still awaiting a result.
CREATE INDEX idx_fleet_update_audit_pending_sweep
    ON fleet_update_audit (triggered_at)
    WHERE report_status IS NULL;
