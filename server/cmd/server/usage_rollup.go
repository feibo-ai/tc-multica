package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The dashboard token-usage endpoints (/api/dashboard/usage/*) read from the
// task_usage_hourly rollup table, which is populated ONLY by the
// rollup_task_usage_hourly() SQL function. Upstream schedules that function via
// pg_cron (see migration 102's operator playbook + migration 076). Our Zeabur
// Postgres uses the pgvector image, which ships without pg_cron, so nothing ran
// the rollup: raw task_usage accumulated while task_usage_hourly stayed empty
// and the dashboard showed zero token usage even though agents were running.
//
// runTaskUsageRollup is the platform-independent substitute migration 076
// explicitly anticipates ("schedule via your platform's scheduling primitive").
// rollup_task_usage_hourly() is advisory-locked (pg_try_advisory_lock(4246))
// and idempotent, so this is safe to run even where a pg_cron job also exists.
const (
	// rollup_task_usage_hourly() caps each call at a one-day window, so the
	// minute ticker keeps a live workspace's dashboard within a tick of fresh
	// in steady state (watermark stays recent → each call processes now-5min).
	taskUsageRollupInterval = time.Minute
	// First-run catch-up bound. Each iteration advances the watermark by <=1
	// day; 800 covers >2 years of history without an unbounded startup loop.
	taskUsageRollupCatchUpMaxIters = 800
	// Consider the rollup caught up once the watermark is within this of now.
	taskUsageRollupCaughtUpLag = time.Hour
)

func runTaskUsageRollup(ctx context.Context, pool *pgxpool.Pool) {
	if os.Getenv("MULTICA_DISABLE_TASK_USAGE_ROLLUP") == "true" {
		slog.Info("task-usage rollup worker disabled via MULTICA_DISABLE_TASK_USAGE_ROLLUP")
		return
	}

	// The rollup-state watermark seeds at epoch (migration 101). Left there, a
	// minute ticker advancing one day per call would crawl from 1970 for
	// decades before reaching real data. Fast-forward past the empty
	// pre-history gap to just before the oldest task_usage row. GREATEST never
	// moves the watermark backwards, so this is a no-op once steady state is
	// reached. A missing rollup-state row (schema migration not applied) means
	// 0 rows affected, not an error.
	if _, err := pool.Exec(ctx, `
		UPDATE task_usage_hourly_rollup_state
		   SET watermark_at = GREATEST(
		         watermark_at,
		         COALESCE((SELECT MIN(created_at) FROM task_usage), now()) - INTERVAL '1 hour')
		 WHERE id = 1`); err != nil {
		slog.Warn("task-usage rollup: watermark fast-forward failed", "error", err)
	}

	// Catch up existing history before handing off to the ticker, so the
	// dashboard backfills on the first deploy rather than over many minutes.
	for i := 0; i < taskUsageRollupCatchUpMaxIters; i++ {
		if ctx.Err() != nil {
			return
		}
		if _, err := pool.Exec(ctx, `SELECT rollup_task_usage_hourly()`); err != nil {
			slog.Warn("task-usage rollup: catch-up tick failed", "error", err, "iter", i)
			break
		}
		var lagSeconds float64
		if err := pool.QueryRow(ctx, `
			SELECT EXTRACT(EPOCH FROM (now() - watermark_at))
			  FROM task_usage_hourly_rollup_state WHERE id = 1`).Scan(&lagSeconds); err != nil {
			break
		}
		if time.Duration(lagSeconds)*time.Second <= taskUsageRollupCaughtUpLag {
			slog.Info("task-usage rollup: initial catch-up complete", "iters", i+1)
			break
		}
	}

	ticker := time.NewTicker(taskUsageRollupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := pool.Exec(ctx, `SELECT rollup_task_usage_hourly()`); err != nil {
				slog.Warn("task-usage rollup tick failed", "error", err)
			}
		}
	}
}
