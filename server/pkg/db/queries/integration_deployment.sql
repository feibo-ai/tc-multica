-- Integration deployments (running-instance tracker).

-- name: RegisterIntegrationDeployment :one
INSERT INTO integration_deployment (integration_id, image_or_commit, host_url, version)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateIntegrationDeploymentHeartbeat :exec
UPDATE integration_deployment
SET last_heartbeat = NOW(),
    config_applied_version = $2,
    status = COALESCE(sqlc.narg('status')::text, status)
WHERE id = $1;

-- name: GetActiveIntegrationDeployment :one
SELECT * FROM integration_deployment
WHERE integration_id = $1
  AND status IN ('starting', 'running', 'degraded')
ORDER BY started_at DESC
LIMIT 1;

-- name: StopIntegrationDeployment :exec
UPDATE integration_deployment
SET status = 'stopped', stopped_at = NOW()
WHERE id = $1;

-- name: ListIntegrationDeployments :many
SELECT * FROM integration_deployment
WHERE integration_id = $1
ORDER BY started_at DESC
LIMIT $2;
