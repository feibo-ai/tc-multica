DROP TRIGGER IF EXISTS trg_atq_session_ambient_dirty ON agent_task_queue;
DROP FUNCTION IF EXISTS enqueue_ambient_usage_hourly_dirty_for_atq_session();
DROP FUNCTION IF EXISTS ambient_usage_hourly_rollup_lag_seconds();
DROP FUNCTION IF EXISTS rollup_ambient_usage_hourly();
DROP FUNCTION IF EXISTS prune_ambient_usage_hourly_dirty(INTERVAL);
DROP FUNCTION IF EXISTS rollup_ambient_usage_hourly_window(TIMESTAMPTZ, TIMESTAMPTZ);
