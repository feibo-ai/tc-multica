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
