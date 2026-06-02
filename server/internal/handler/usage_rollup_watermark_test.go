package handler

import (
	"context"
	"testing"
	"time"
)

// The hourly rollup-state watermark seeds at epoch (migration 101). The
// token-usage dashboard worker (server/cmd/server/usage_rollup.go) fast-forwards
// it past the empty 1970→first-data gap before ticking, otherwise a one-day-per-
// tick rollup would crawl out of 1970 for decades and the dashboard would stay
// empty. This guards that fast-forward SQL: from epoch it must jump forward, and
// it must never move a recent watermark backwards (GREATEST).
//
// SQL kept in sync with runTaskUsageRollup's fast-forward statement.
const fastForwardWatermarkSQL = `
	UPDATE task_usage_hourly_rollup_state
	   SET watermark_at = GREATEST(
	         watermark_at,
	         COALESCE((SELECT MIN(created_at) FROM task_usage), now()) - INTERVAL '1 hour')
	 WHERE id = 1`

func TestTaskUsageRollupWatermarkFastForward(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	// Snapshot and restore so we don't disturb the rollup tests that share
	// this singleton state row.
	var orig time.Time
	if err := testPool.QueryRow(ctx,
		`SELECT watermark_at FROM task_usage_hourly_rollup_state WHERE id = 1`).Scan(&orig); err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`UPDATE task_usage_hourly_rollup_state SET watermark_at = $1 WHERE id = 1`, orig)
	})

	// From epoch, the fast-forward must jump far past 1970.
	if _, err := testPool.Exec(ctx,
		`UPDATE task_usage_hourly_rollup_state SET watermark_at = '1970-01-01 00:00:00+00' WHERE id = 1`); err != nil {
		t.Fatalf("reset watermark to epoch: %v", err)
	}
	if _, err := testPool.Exec(ctx, fastForwardWatermarkSQL); err != nil {
		t.Fatalf("fast-forward from epoch: %v", err)
	}
	var afterEpoch time.Time
	if err := testPool.QueryRow(ctx,
		`SELECT watermark_at FROM task_usage_hourly_rollup_state WHERE id = 1`).Scan(&afterEpoch); err != nil {
		t.Fatalf("read watermark after fast-forward: %v", err)
	}
	if afterEpoch.Year() < 2020 {
		t.Fatalf("fast-forward left watermark in the pre-history gap: %s", afterEpoch)
	}

	// From a recent watermark, GREATEST must NOT move it backwards.
	recent := time.Now().Add(-2 * time.Minute).UTC()
	if _, err := testPool.Exec(ctx,
		`UPDATE task_usage_hourly_rollup_state SET watermark_at = $1 WHERE id = 1`, recent); err != nil {
		t.Fatalf("set recent watermark: %v", err)
	}
	if _, err := testPool.Exec(ctx, fastForwardWatermarkSQL); err != nil {
		t.Fatalf("fast-forward from recent: %v", err)
	}
	var afterRecent time.Time
	if err := testPool.QueryRow(ctx,
		`SELECT watermark_at FROM task_usage_hourly_rollup_state WHERE id = 1`).Scan(&afterRecent); err != nil {
		t.Fatalf("read watermark after no-op fast-forward: %v", err)
	}
	if afterRecent.Before(recent.Add(-time.Second)) {
		t.Fatalf("fast-forward moved a recent watermark backwards: was %s, now %s", recent, afterRecent)
	}
}
