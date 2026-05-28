-- Append-only audit log (currently used by the control plane; the table is
-- generic and may be reused by other features later).

-- name: CreateAuditLog :one
INSERT INTO audit_log (workspace_id, actor_user_id, actor_type, event_type, resource, metadata, ip_address)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListAuditLogsForResource :many
SELECT * FROM audit_log
WHERE workspace_id = $1 AND resource = $2
ORDER BY created_at DESC
LIMIT $3;

-- name: ListAuditLogsByEventTypes :many
SELECT * FROM audit_log
WHERE workspace_id = $1
  AND event_type = ANY(@event_types::text[])
ORDER BY created_at DESC
LIMIT $2;

-- name: ListAuditLogsByWorkspace :many
-- Generic list with optional filters used by `multica audit-log list` and
-- the future UI audit tab. Empty filters (sqlc.narg('resource') NULL,
-- empty event_types array, NULL since) skip the corresponding WHERE clause.
SELECT * FROM audit_log
WHERE workspace_id = $1
  AND (sqlc.narg('resource')::text IS NULL OR resource = sqlc.narg('resource')::text)
  AND (cardinality(@event_types::text[]) = 0 OR event_type = ANY(@event_types::text[]))
  AND (sqlc.narg('since')::timestamptz IS NULL OR created_at > sqlc.narg('since')::timestamptz)
ORDER BY created_at DESC
LIMIT $2;
