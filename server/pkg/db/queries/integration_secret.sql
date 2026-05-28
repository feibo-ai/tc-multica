-- Integration secrets (encrypted credentials).

-- name: UpsertIntegrationSecret :one
INSERT INTO integration_secret (integration_id, key, encrypted_value, nonce, created_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (integration_id, key) DO UPDATE
SET encrypted_value = EXCLUDED.encrypted_value,
    nonce = EXCLUDED.nonce,
    version = integration_secret.version + 1,
    updated_at = NOW()
RETURNING *;

-- name: GetIntegrationSecret :one
SELECT * FROM integration_secret
WHERE integration_id = $1 AND key = $2;

-- name: ListIntegrationSecretKeys :many
-- Lists key + metadata only. Encrypted values are never sent for list calls.
SELECT id, integration_id, key, version, created_by, created_at, updated_at
FROM integration_secret
WHERE integration_id = $1
ORDER BY key ASC;

-- name: DeleteIntegrationSecret :exec
DELETE FROM integration_secret
WHERE integration_id = $1 AND key = $2;
