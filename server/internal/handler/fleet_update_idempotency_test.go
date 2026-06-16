package handler

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ---------------------------------------------------------------------------
// TEA-113 INV-13 / INV-14 mutual-exclusion & idempotency (REV-5 mini-ADR).
//
// Both the daemon-report write (UpsertFleetUpdateResult, INV-13) and the
// server-timeout sweep (SweepTimedOutFleetUpdates, INV-14) write the terminal
// (B) result columns of a fleet_update_audit row, each guarded by
// `report_status IS NULL`. The guard makes them mutually exclusive and
// idempotent: whichever lands the terminal row first wins; the loser matches 0
// rows. These DB-level tests prove both orderings (first-writer-wins) against
// the real test DB fixture (handler_test.go TestMain).
//
// This repo HAS a test DB fixture (handler_test.go connects to DATABASE_URL or
// the local default and skips cleanly when unreachable), so these are run as
// real integration tests — not query-string assertions. Migration 119 must be
// applied; the suite is skipped when the DB / table is unavailable.
// ---------------------------------------------------------------------------

// seedFleetAuditTrigger inserts an (A) trigger row with the given triggered_at
// age (triggered_at = now() - age) and no (B) result yet. Returns its update_id.
// The age lets the INV-14 sweep test place a row past / before the timeout
// threshold deterministically.
func seedFleetAuditTrigger(t *testing.T, wsID string, age time.Duration) string {
	t.Helper()
	ctx := context.Background()

	// A runtime to satisfy the NOT NULL runtime_id column. Reuse the shared
	// fixture runtime — only its id is needed as an opaque audit value.
	rtID := handlerTestRuntimeID(t)

	updateID := "fleet-idem-" + fleetUniqueSuffix()
	if _, err := testPool.Exec(ctx, `
		INSERT INTO fleet_update_audit (
			update_id, workspace_id, runtime_id, user_id, target_version, force, triggered_at
		)
		VALUES ($1, $2, $3, $4, 'v0.4.15', false, now() - make_interval(secs => $5::float8))
	`, updateID, wsID, rtID, testUserID, age.Seconds()); err != nil {
		t.Fatalf("seed fleet audit trigger: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM fleet_update_audit WHERE update_id = $1`, updateID)
	})
	return updateID
}

func fleetAuditResult(t *testing.T, updateID string) (status, source *string) {
	t.Helper()
	var s, src pgtype.Text
	if err := testPool.QueryRow(context.Background(),
		`SELECT report_status, report_source FROM fleet_update_audit WHERE update_id = $1`, updateID,
	).Scan(&s, &src); err != nil {
		t.Fatalf("load audit result: %v", err)
	}
	return util.TextToPtr(s), util.TextToPtr(src)
}

// hugeTimeout is a threshold large enough that a freshly-seeded row is NOT
// considered stale by the sweep — used to prove the sweep does NOT touch a row
// whose daemon result already landed.
const hugeTimeout = 365 * 24 * time.Hour

// TestFleetResult_DaemonFirstThenSweepNoop covers INV-13/INV-14 ordering #1:
// the daemon report lands the terminal (B) row first; a later sweep over the
// same row writes 0 rows (the `report_status IS NULL` guard excludes it).
func TestFleetResult_DaemonFirstThenSweepNoop(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	wsID, _ := fleetTestWorkspace(t)
	queries := db.New(testPool)

	// Seed a row already PAST a small timeout threshold so the sweep WOULD pick
	// it up — except the daemon writes first.
	updateID := seedFleetAuditTrigger(t, wsID, 10*time.Minute)

	// Daemon report first (INV-13): lands report_status='completed' /
	// report_source='daemon-reported'. Must affect exactly 1 row.
	rows, err := queries.UpsertFleetUpdateResult(context.Background(), db.UpsertFleetUpdateResultParams{
		ReportStatus: pgtype.Text{String: "completed", Valid: true},
		UpdateID:     updateID,
	})
	if err != nil {
		t.Fatalf("daemon UpsertFleetUpdateResult: %v", err)
	}
	if rows != 1 {
		t.Fatalf("daemon report should affect 1 row, got %d", rows)
	}

	// Sweep AFTER (INV-14): threshold 60s, the row is 10min old, but it already
	// has a terminal (B) row → the `report_status IS NULL` guard excludes it.
	// Must write 0 rows.
	swept, err := queries.SweepTimedOutFleetUpdates(context.Background(), db.SweepTimedOutFleetUpdatesParams{
		TimeoutSecs: 60,
		MaxPerTick:  500,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, s := range swept {
		if s.UpdateID == updateID {
			t.Fatalf("sweep wrote the already-terminal row %s — first-writer-wins violated", updateID)
		}
	}

	// Final state: the daemon's terminal row stands, untouched by the sweep.
	status, source := fleetAuditResult(t, updateID)
	if status == nil || *status != "completed" {
		t.Fatalf("report_status = %v, want completed (daemon won, sweep must not overwrite)", status)
	}
	if source == nil || *source != "daemon-reported" {
		t.Fatalf("report_source = %v, want daemon-reported (sweep must not overwrite)", source)
	}
}

// TestFleetResult_SweepFirstThenDaemonNoop covers INV-13/INV-14 ordering #2:
// the server-timeout sweep lands the terminal (B) row first; a later daemon
// report over the same row writes 0 rows.
func TestFleetResult_SweepFirstThenDaemonNoop(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	wsID, _ := fleetTestWorkspace(t)
	queries := db.New(testPool)

	// Seed a row well past the timeout threshold.
	updateID := seedFleetAuditTrigger(t, wsID, 10*time.Minute)

	// Sweep first (INV-14): flushes report_status='timeout' /
	// report_source='server-timeout'. Must include our row.
	swept, err := queries.SweepTimedOutFleetUpdates(context.Background(), db.SweepTimedOutFleetUpdatesParams{
		TimeoutSecs: 60,
		MaxPerTick:  500,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var sweptOurs bool
	for _, s := range swept {
		if s.UpdateID == updateID {
			sweptOurs = true
		}
	}
	if !sweptOurs {
		t.Fatalf("sweep should have flushed the stale row %s", updateID)
	}

	// Daemon report AFTER (INV-13): the row already has a terminal (B) row →
	// `report_status IS NULL` guard excludes it. Must affect 0 rows.
	rows, err := queries.UpsertFleetUpdateResult(context.Background(), db.UpsertFleetUpdateResultParams{
		ReportStatus: pgtype.Text{String: "completed", Valid: true},
		UpdateID:     updateID,
	})
	if err != nil {
		t.Fatalf("late daemon UpsertFleetUpdateResult: %v", err)
	}
	if rows != 0 {
		t.Fatalf("late daemon report should affect 0 rows (sweep won), got %d", rows)
	}

	// Final state: the sweep's timeout row stands, untouched by the daemon.
	status, source := fleetAuditResult(t, updateID)
	if status == nil || *status != "timeout" {
		t.Fatalf("report_status = %v, want timeout (sweep won, daemon must not overwrite)", status)
	}
	if source == nil || *source != "server-timeout" {
		t.Fatalf("report_source = %v, want server-timeout (daemon must not overwrite)", source)
	}
}

// TestFleetResult_DaemonReportIdempotent covers INV-13 idempotency: a duplicate
// daemon report (e.g. a retried callback) over an already-terminal row affects
// 0 rows and does not overwrite the original terminal state.
func TestFleetResult_DaemonReportIdempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	wsID, _ := fleetTestWorkspace(t)
	queries := db.New(testPool)

	updateID := seedFleetAuditTrigger(t, wsID, 0)

	first, err := queries.UpsertFleetUpdateResult(context.Background(), db.UpsertFleetUpdateResultParams{
		ReportStatus: pgtype.Text{String: "completed", Valid: true},
		UpdateID:     updateID,
	})
	if err != nil {
		t.Fatalf("first daemon report: %v", err)
	}
	if first != 1 {
		t.Fatalf("first daemon report should affect 1 row, got %d", first)
	}

	// A second report — even with a DIFFERENT status — must be a no-op: the
	// guard fires because report_status is no longer NULL.
	second, err := queries.UpsertFleetUpdateResult(context.Background(), db.UpsertFleetUpdateResultParams{
		ReportStatus: pgtype.Text{String: "failed", Valid: true},
		UpdateID:     updateID,
	})
	if err != nil {
		t.Fatalf("second daemon report: %v", err)
	}
	if second != 0 {
		t.Fatalf("duplicate daemon report should affect 0 rows, got %d", second)
	}

	status, _ := fleetAuditResult(t, updateID)
	if status == nil || *status != "completed" {
		t.Fatalf("report_status = %v, want completed (first report must stand, not be overwritten by the duplicate)", status)
	}
}

// TestFleetSweep_SkipsNotYetTimedOut covers the INV-14 threshold: a fresh (A)
// row that is NOT past the timeout threshold is left untouched (no premature
// timeout flush). Combined with the daemon-first test, this proves the sweep
// only acts on rows that are both stale AND still missing a (B) result.
func TestFleetSweep_SkipsNotYetTimedOut(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	wsID, _ := fleetTestWorkspace(t)
	queries := db.New(testPool)

	// Row triggered just now; with a huge timeout threshold it is not stale.
	updateID := seedFleetAuditTrigger(t, wsID, 0)

	swept, err := queries.SweepTimedOutFleetUpdates(context.Background(), db.SweepTimedOutFleetUpdatesParams{
		TimeoutSecs: hugeTimeout.Seconds(),
		MaxPerTick:  500,
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, s := range swept {
		if s.UpdateID == updateID {
			t.Fatalf("sweep flushed a not-yet-timed-out row %s — premature timeout", updateID)
		}
	}

	status, _ := fleetAuditResult(t, updateID)
	if status != nil {
		t.Fatalf("report_status = %v, want nil (row must remain pending until past timeout)", status)
	}
}
