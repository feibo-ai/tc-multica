-- name: UpsertAmbientUsage :exec
-- Idempotent insert of one ambient (task-less local-session) usage event.
--
-- ON CONFLICT DO NOTHING on the composite key does double duty (坑#1):
--   * cross-scan idempotency — re-scanning a transcript re-sends the same
--     tuples and they no-op, so completion criterion "re-running the scanner
--     adds 0 rows" holds without the daemon tracking what it already sent.
--   * in-file dedup — a transcript repeats the SAME (message.id, requestId)
--     line up to ~33x; only the first-arrival row survives.
--
-- First-arrival wins, which relies on repeated lines for the same
-- (message.id, requestId) carrying identical token counts — an invariant of
-- the on-disk transcript format (a naive SUM over the repeats would inflate
-- usage ~3.46x vs ccusage; this key is what prevents that).
--
-- Privacy (decisions/2026-06-03-local-log-privacy.md): the column list is
-- numbers and ids only — there is deliberately no content/prompt/text column.
INSERT INTO ambient_usage (
    workspace_id, runtime_id, session_id, message_id, request_id,
    provider, model, event_at,
    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
    source
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
)
ON CONFLICT ON CONSTRAINT uq_ambient_usage_key DO NOTHING;
