-- Integration CRUD (control plane).

-- name: CreateIntegration :one
INSERT INTO integration (workspace_id, kind, name, config, deployment_webhook_url, config_schema_ref)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetIntegration :one
SELECT * FROM integration WHERE id = $1;

-- name: GetIntegrationInWorkspace :one
SELECT * FROM integration WHERE id = $1 AND workspace_id = $2;

-- name: GetIntegrationByWorkspaceAndName :one
SELECT * FROM integration WHERE workspace_id = $1 AND name = $2;

-- name: ListIntegrationsByWorkspace :many
SELECT * FROM integration
WHERE workspace_id = $1
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind')::text)
ORDER BY name ASC;

-- name: UpdateIntegrationConfig :one
UPDATE integration
SET config = $2,
    version = version + 1,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateIntegrationStatus :exec
UPDATE integration
SET status = $2, updated_at = NOW()
WHERE id = $1;

-- name: DeleteIntegration :exec
DELETE FROM integration WHERE id = $1;
