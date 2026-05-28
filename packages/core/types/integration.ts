// Control plane types (Plan 4 / PR D).
//
// Mirrors handler response shapes in server/internal/handler/integration*.go.
// Keep enums permissive — server may introduce new kinds / statuses; UI must
// degrade gracefully via API-response-compatibility rules (CLAUDE.md).

export type IntegrationKind = "mcp-server" | "feishu" | "autopilot-bot";

export type IntegrationStatus = "pending" | "running" | "stopped" | "degraded";

export type DeploymentStatus = "starting" | "running" | "degraded" | "stopped";

export interface Integration {
  id: string;
  workspace_id: string;
  kind: IntegrationKind;
  name: string;
  config: Record<string, unknown>;
  version: number;
  status: IntegrationStatus;
  deployment_webhook_url: string | null | undefined;
  config_schema_ref: string | null | undefined;
  created_at: string;
  updated_at: string;
}

export interface IntegrationStatusSummary {
  integration_id: string;
  integration_status: IntegrationStatus;
  config_version: number;
  active_deployment: IntegrationDeployment | null | undefined;
}

export interface IntegrationDeployment {
  id: string;
  integration_id: string;
  image_or_commit: string;
  host_url: string | null | undefined;
  version: number;
  status: DeploymentStatus;
  last_heartbeat: string | null | undefined;
  config_applied_version: number | null | undefined;
  started_at: string;
  stopped_at: string | null | undefined;
}

// Secret-key-list rows never expose values.
export interface SecretKey {
  key: string;
  version: number;
  created_by: string | null | undefined;
  created_at: string;
  updated_at: string;
}

export interface SecretValue {
  key: string;
  value: string;
  version: number;
}
