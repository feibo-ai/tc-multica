package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// runAmbientUsageRollup is the ambient-usage twin of runTaskUsageRollup. It
// drives rollup_ambient_usage_hourly() — which aggregates task-LESS local
// session usage (ambient_usage) into ambient_usage_hourly — on the same
// in-process minute ticker, because our Zeabur Postgres ships without pg_cron
// (see usage_rollup.go for the full rationale).
//
// rollup_ambient_usage_hourly() takes its own advisory lock (4247, distinct
// from the task rollup's 4246) and is idempotent, so running it here is safe
// alongside any external scheduler and never serialises against the task
// rollup.
const (
	ambientUsageRollupInterval        = time.Minute
	ambientUsageRollupCatchUpMaxIters = 800
	ambientUsageRollupCaughtUpLag     = time.Hour
)

func runAmbientUsageRollup(ctx context.Context, pool *pgxpool.Pool) {
	if os.Getenv("MULTICA_DISABLE_AMBIENT_USAGE_ROLLUP") == "true" {
		slog.Info("ambient-usage rollup worker disabled via MULTICA_DISABLE_AMBIENT_USAGE_ROLLUP")
		return
	}

	// Fast-forward the watermark past the empty pre-history so a minute ticker
	// advancing one day per call does not crawl from 1970. GREATEST never moves
	// the watermark backwards, so this is a no-op once steady state is reached.
	// A fresh deploy with no ambient_usage rows yet seeds the watermark to
	// now()-1h (COALESCE fallback), so the worker is forward-only from first
	// boot — matching the plan's "只向前" decision. A missing state row (schema
	// not applied) means 0 rows affected, not an error.
	if _, err := pool.Exec(ctx, `
		UPDATE ambient_usage_hourly_rollup_state
		   SET watermark_at = GREATEST(
		         watermark_at,
		         COALESCE((SELECT MIN(created_at) FROM ambient_usage), now()) - INTERVAL '1 hour')
		 WHERE id = 1`); err != nil {
		slog.Warn("ambient-usage rollup: watermark fast-forward failed", "error", err)
	}

	for i := 0; i < ambientUsageRollupCatchUpMaxIters; i++ {
		if ctx.Err() != nil {
			return
		}
		if _, err := pool.Exec(ctx, `SELECT rollup_ambient_usage_hourly()`); err != nil {
			slog.Warn("ambient-usage rollup: catch-up tick failed", "error", err, "iter", i)
			break
		}
		var lagSeconds float64
		if err := pool.QueryRow(ctx, `
			SELECT EXTRACT(EPOCH FROM (now() - watermark_at))
			  FROM ambient_usage_hourly_rollup_state WHERE id = 1`).Scan(&lagSeconds); err != nil {
			break
		}
		if time.Duration(lagSeconds)*time.Second <= ambientUsageRollupCaughtUpLag {
			slog.Info("ambient-usage rollup: initial catch-up complete", "iters", i+1)
			break
		}
	}

	ticker := time.NewTicker(ambientUsageRollupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := pool.Exec(ctx, `SELECT rollup_ambient_usage_hourly()`); err != nil {
				slog.Warn("ambient-usage rollup tick failed", "error", err)
			}
		}
	}
}
