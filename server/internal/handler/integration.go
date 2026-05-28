package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/audit"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Integration control plane (PR D). All handlers are wired under
// /api/integrations by the router only when MULTICA_CONTROL_PLANE_ENABLED is
// true; missing-flag responses are 404 (route not registered), so handlers
// here may assume the flag is on.

// IntegrationResponse mirrors db.Integration with timestamps as strings, which
// is the wire format every other handler in this codebase uses.
type IntegrationResponse struct {
	ID                   string         `json:"id"`
	WorkspaceID          string         `json:"workspace_id"`
	Kind                 string         `json:"kind"`
	Name                 string         `json:"name"`
	Config               map[string]any `json:"config"`
	Version              int32          `json:"version"`
	Status               string         `json:"status"`
	DeploymentWebhookURL *string        `json:"deployment_webhook_url,omitempty"`
	ConfigSchemaRef      *string        `json:"config_schema_ref,omitempty"`
	CreatedAt            string         `json:"created_at"`
	UpdatedAt            string         `json:"updated_at"`
}

func integrationToResponse(i db.Integration) IntegrationResponse {
	cfg := map[string]any{}
	if len(i.Config) > 0 {
		_ = json.Unmarshal(i.Config, &cfg)
	}
	return IntegrationResponse{
		ID:                   uuidToString(i.ID),
		WorkspaceID:          uuidToString(i.WorkspaceID),
		Kind:                 i.Kind,
		Name:                 i.Name,
		Config:               cfg,
		Version:              i.Version,
		Status:               i.Status,
		DeploymentWebhookURL: textToPtr(i.DeploymentWebhookUrl),
		ConfigSchemaRef:      textToPtr(i.ConfigSchemaRef),
		CreatedAt:            timestampToString(i.CreatedAt),
		UpdatedAt:            timestampToString(i.UpdatedAt),
	}
}

// validIntegrationKind matches the CHECK constraint in migration 113.
var validIntegrationKind = map[string]bool{
	"mcp-server":    true,
	"feishu":        true,
	"autopilot-bot": true,
}

// CreateIntegrationRequest is the body for POST /api/integrations.
type CreateIntegrationRequest struct {
	Kind                 string         `json:"kind"`
	Name                 string         `json:"name"`
	Config               map[string]any `json:"config"`
	DeploymentWebhookURL string         `json:"deployment_webhook_url"`
	ConfigSchemaRef      string         `json:"config_schema_ref"`
}

// UpdateIntegrationConfigRequest is the body for PATCH /api/integrations/{id}/config.
type UpdateIntegrationConfigRequest struct {
	Config map[string]any `json:"config"`
}

// loadIntegrationForUser validates the URL id, resolves the workspace from
// the request, and looks up the integration scoped to that workspace. Returns
// (integration, true) on success; on failure it writes the response (404/400/
// 401) and returns ok=false — the caller must return immediately.
func (h *Handler) loadIntegrationForUser(w http.ResponseWriter, r *http.Request, integrationID string) (db.Integration, bool) {
	if _, ok := requireUserID(w, r); !ok {
		return db.Integration{}, false
	}

	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Integration{}, false
	}

	idUUID, ok := parseUUIDOrBadRequest(w, integrationID, "integration id")
	if !ok {
		return db.Integration{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.Integration{}, false
	}

	integration, err := h.Queries.GetIntegrationInWorkspace(r.Context(), db.GetIntegrationInWorkspaceParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "integration not found")
		return db.Integration{}, false
	}
	return integration, true
}

// CreateIntegration: POST /api/integrations
func (h *Handler) CreateIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var req CreateIntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !validIntegrationKind[req.Kind] {
		writeError(w, http.StatusBadRequest, "invalid kind (mcp-server | feishu | autopilot-bot)")
		return
	}

	cfgJSON := []byte("{}")
	if req.Config != nil {
		b, err := json.Marshal(req.Config)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid config json")
			return
		}
		cfgJSON = b
	}

	created, err := h.Queries.CreateIntegration(r.Context(), db.CreateIntegrationParams{
		WorkspaceID:          wsUUID,
		Kind:                 req.Kind,
		Name:                 req.Name,
		Config:               cfgJSON,
		DeploymentWebhookUrl: strToText(req.DeploymentWebhookURL),
		ConfigSchemaRef:      strToText(req.ConfigSchemaRef),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an integration with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create integration: "+err.Error())
		return
	}

	resp := integrationToResponse(created)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	h.Audit.Record(r.Context(), audit.EventIntegrationCreated, audit.Subject{
		WorkspaceID: workspaceID,
		ActorUserID: userID,
		ActorType:   actorType,
		Resource:    "integration:" + resp.ID,
		IPAddress:   r.RemoteAddr,
		Metadata:    map[string]any{"kind": resp.Kind, "name": resp.Name},
	})
	// Notify any subscriber listening for this workspace so they re-pull.
	h.publish(protocol.EventIntegrationConfigChanged, workspaceID, actorType, actorID, map[string]any{
		"integration": resp,
		"reason":      "created",
	})
	writeJSON(w, http.StatusCreated, resp)
}

// ListIntegrations: GET /api/integrations?kind=...
func (h *Handler) ListIntegrations(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var kindArg pgtype.Text
	if k := r.URL.Query().Get("kind"); k != "" {
		kindArg = pgtype.Text{String: k, Valid: true}
	}

	rows, err := h.Queries.ListIntegrationsByWorkspace(r.Context(), db.ListIntegrationsByWorkspaceParams{
		WorkspaceID: wsUUID,
		Kind:        kindArg,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list integrations")
		return
	}
	resp := make([]IntegrationResponse, len(rows))
	for i, row := range rows {
		resp[i] = integrationToResponse(row)
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetIntegration: GET /api/integrations/{id}
func (h *Handler) GetIntegration(w http.ResponseWriter, r *http.Request) {
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, integrationToResponse(integration))
}

// GetIntegrationStatus: GET /api/integrations/{id}/status
// Returns status + active deployment summary + last heartbeat. Computes a
// "degraded" downgrade when the active deployment hasn't heartbeated in 90s.
func (h *Handler) GetIntegrationStatus(w http.ResponseWriter, r *http.Request) {
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	resp := map[string]any{
		"integration_id":     uuidToString(integration.ID),
		"integration_status": integration.Status,
		"config_version":     integration.Version,
	}

	dep, err := h.Queries.GetActiveIntegrationDeployment(r.Context(), integration.ID)
	if err == nil {
		status := dep.Status
		if dep.LastHeartbeat.Valid && time.Since(dep.LastHeartbeat.Time) > 90*time.Second {
			status = "degraded"
		}
		resp["active_deployment"] = map[string]any{
			"id":                     uuidToString(dep.ID),
			"image_or_commit":        dep.ImageOrCommit,
			"host_url":               textToPtr(dep.HostUrl),
			"version":                dep.Version,
			"status":                 status,
			"last_heartbeat":         timestampToPtr(dep.LastHeartbeat),
			"config_applied_version": int8ToPtr(pgtype.Int8{Int64: int64(dep.ConfigAppliedVersion.Int32), Valid: dep.ConfigAppliedVersion.Valid}),
			"started_at":             timestampToString(dep.StartedAt),
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to fetch deployment")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// UpdateIntegrationConfig: PATCH /api/integrations/{id}/config
func (h *Handler) UpdateIntegrationConfig(w http.ResponseWriter, r *http.Request) {
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req UpdateIntegrationConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Config == nil {
		writeError(w, http.StatusBadRequest, "config is required")
		return
	}
	cfgJSON, err := json.Marshal(req.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid config json")
		return
	}

	updated, err := h.Queries.UpdateIntegrationConfig(r.Context(), db.UpdateIntegrationConfigParams{
		ID:     integration.ID,
		Config: cfgJSON,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update config: "+err.Error())
		return
	}

	resp := integrationToResponse(updated)
	workspaceID := uuidToString(updated.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	h.Audit.Record(r.Context(), audit.EventIntegrationConfigChanged, audit.Subject{
		WorkspaceID: workspaceID,
		ActorUserID: userID,
		ActorType:   actorType,
		Resource:    "integration:" + resp.ID,
		IPAddress:   r.RemoteAddr,
		Metadata:    map[string]any{"new_version": resp.Version},
	})
	h.publish(protocol.EventIntegrationConfigChanged, workspaceID, actorType, actorID, map[string]any{
		"integration": resp,
		"reason":      "config-changed",
	})

	writeJSON(w, http.StatusOK, resp)
}

// RestartIntegration: POST /api/integrations/{id}/restart
// Publishes an event so subscribers re-pull and restart their workers. The
// server does not own the runtime process; the runtime decides what
// "restart" means.
func (h *Handler) RestartIntegration(w http.ResponseWriter, r *http.Request) {
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, _ := requireUserID(w, r)
	workspaceID := uuidToString(integration.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	h.Audit.Record(r.Context(), audit.EventIntegrationRestarted, audit.Subject{
		WorkspaceID: workspaceID,
		ActorUserID: userID,
		ActorType:   actorType,
		Resource:    "integration:" + uuidToString(integration.ID),
		IPAddress:   r.RemoteAddr,
	})
	h.publish(protocol.EventIntegrationConfigChanged, workspaceID, actorType, actorID, map[string]any{
		"integration": integrationToResponse(integration),
		"reason":      "restart",
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RedeployIntegration: POST /api/integrations/{id}/redeploy
// Calls the integration's configured deployment webhook if any. Without a
// webhook URL there's no automated action to take — return 400 so the caller
// learns to configure one (or to do the redeploy by hand).
func (h *Handler) RedeployIntegration(w http.ResponseWriter, r *http.Request) {
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !integration.DeploymentWebhookUrl.Valid || integration.DeploymentWebhookUrl.String == "" {
		writeError(w, http.StatusBadRequest, "deployment_webhook_url is not configured for this integration")
		return
	}
	userID, _ := requireUserID(w, r)
	workspaceID := uuidToString(integration.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	// Fire the webhook with a short timeout. Errors are reported but the
	// audit row + event still go out so operators can see the attempt.
	payload, _ := json.Marshal(map[string]any{
		"integration_id":  uuidToString(integration.ID),
		"current_version": integration.Version,
		"action":          "redeploy",
	})
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost,
		integration.DeploymentWebhookUrl.String, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	hookErr := "ok"
	resp, err := client.Do(req)
	if err != nil {
		hookErr = err.Error()
	} else {
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			hookErr = "webhook returned " + resp.Status
		}
	}

	h.Audit.Record(r.Context(), audit.EventIntegrationRedeployed, audit.Subject{
		WorkspaceID: workspaceID,
		ActorUserID: userID,
		ActorType:   actorType,
		Resource:    "integration:" + uuidToString(integration.ID),
		IPAddress:   r.RemoteAddr,
		Metadata:    map[string]any{"webhook_result": hookErr},
	})
	h.publish(protocol.EventIntegrationConfigChanged, workspaceID, actorType, actorID, map[string]any{
		"integration": integrationToResponse(integration),
		"reason":      "redeploy",
	})

	out := map[string]any{"ok": hookErr == "ok"}
	if hookErr != "ok" {
		out["webhook_error"] = hookErr
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteIntegration: DELETE /api/integrations/{id}
func (h *Handler) DeleteIntegration(w http.ResponseWriter, r *http.Request) {
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, _ := requireUserID(w, r)
	workspaceID := uuidToString(integration.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	if err := h.Queries.DeleteIntegration(r.Context(), integration.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete integration")
		return
	}

	h.Audit.Record(r.Context(), audit.EventIntegrationDeleted, audit.Subject{
		WorkspaceID: workspaceID,
		ActorUserID: userID,
		ActorType:   actorType,
		Resource:    "integration:" + uuidToString(integration.ID),
		IPAddress:   r.RemoteAddr,
	})
	h.publish(protocol.EventIntegrationConfigChanged, workspaceID, actorType, actorID, map[string]any{
		"integration_id": uuidToString(integration.ID),
		"reason":         "deleted",
	})
	w.WriteHeader(http.StatusNoContent)
}

// Silence unused-import warnings when integration.go is the only file using
// these helpers in some build configurations.
var _ = util.UUIDToString
