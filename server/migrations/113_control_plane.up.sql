BEGIN;

CREATE TABLE integration (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('mcp-server', 'feishu', 'autopilot-bot')),
    name TEXT NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    version INT NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'stopped', 'degraded')),
    deployment_webhook_url TEXT,
    config_schema_ref TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (workspace_id, name)
);

CREATE INDEX idx_integration_workspace_kind ON integration(workspace_id, kind);

CREATE TABLE integration_secret (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    integration_id UUID NOT NULL REFERENCES integration(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    encrypted_value BYTEA NOT NULL,
    nonce BYTEA NOT NULL,
    version INT NOT NULL DEFAULT 1,
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (integration_id, key)
);

CREATE INDEX idx_integration_secret_integration ON integration_secret(integration_id);

CREATE TABLE integration_deployment (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    integration_id UUID NOT NULL REFERENCES integration(id) ON DELETE CASCADE,
    image_or_commit TEXT NOT NULL,
    host_url TEXT,
    version INT NOT NULL,
    status TEXT NOT NULL DEFAULT 'starting'
        CHECK (status IN ('starting', 'running', 'degraded', 'stopped')),
    last_heartbeat TIMESTAMPTZ,
    config_applied_version INT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    stopped_at TIMESTAMPTZ
);

CREATE INDEX idx_integration_deployment_integration ON integration_deployment(integration_id);
CREATE INDEX idx_integration_deployment_active ON integration_deployment(integration_id, status)
    WHERE status IN ('starting', 'running', 'degraded');

CREATE TABLE audit_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    actor_user_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
    actor_type TEXT NOT NULL DEFAULT 'user'
        CHECK (actor_type IN ('user', 'agent', 'system')),
    event_type TEXT NOT NULL,
    resource TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    ip_address INET,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_log_workspace_time ON audit_log(workspace_id, created_at DESC);
CREATE INDEX idx_audit_log_resource ON audit_log(resource, created_at DESC);
CREATE INDEX idx_audit_log_event_type ON audit_log(workspace_id, event_type, created_at DESC);

COMMENT ON TABLE integration IS 'Control plane: managed external service configurations';
COMMENT ON TABLE integration_secret IS 'Control plane: encrypted credentials per integration';
COMMENT ON TABLE integration_deployment IS 'Control plane: running instance tracker';
COMMENT ON TABLE audit_log IS 'Append-only audit trail for control plane reads/writes';

COMMIT;
