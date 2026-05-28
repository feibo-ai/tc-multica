package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/audit"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// IntegrationDeploymentResponse mirrors db.IntegrationDeployment with
// timestamps as strings.
type IntegrationDeploymentResponse struct {
	ID                   string  `json:"id"`
	IntegrationID        string  `json:"integration_id"`
	ImageOrCommit        string  `json:"image_or_commit"`
	HostURL              *string `json:"host_url,omitempty"`
	Version              int32   `json:"version"`
	Status               string  `json:"status"`
	LastHeartbeat        *string `json:"last_heartbeat,omitempty"`
	ConfigAppliedVersion *int32  `json:"config_applied_version,omitempty"`
	StartedAt            string  `json:"started_at"`
	StoppedAt            *string `json:"stopped_at,omitempty"`
}

func deploymentToResponse(d db.IntegrationDeployment) IntegrationDeploymentResponse {
	var cfgV *int32
	if d.ConfigAppliedVersion.Valid {
		v := d.ConfigAppliedVersion.Int32
		cfgV = &v
	}
	return IntegrationDeploymentResponse{
		ID:                   uuidToString(d.ID),
		IntegrationID:        uuidToString(d.IntegrationID),
		ImageOrCommit:        d.ImageOrCommit,
		HostURL:              textToPtr(d.HostUrl),
		Version:              d.Version,
		Status:               d.Status,
		LastHeartbeat:        timestampToPtr(d.LastHeartbeat),
		ConfigAppliedVersion: cfgV,
		StartedAt:            timestampToString(d.StartedAt),
		StoppedAt:            timestampToPtr(d.StoppedAt),
	}
}

// RegisterDeploymentRequest is the body for POST /api/deployments.
type RegisterDeploymentRequest struct {
	IntegrationID string `json:"integration_id"`
	ImageOrCommit string `json:"image_or_commit"`
	HostURL       string `json:"host_url"`
	Version       int32  `json:"version"`
}

// HeartbeatDeploymentRequest is the body for POST /api/deployments/{id}/heartbeat.
type HeartbeatDeploymentRequest struct {
	ConfigAppliedVersion int32  `json:"config_applied_version"`
	Status               string `json:"status,omitempty"`
}

// RegisterDeployment: POST /api/deployments
// The caller must have CapIntegrationsRead on the integration's workspace
// (verified by loading the integration first to enforce workspace scope).
func (h *Handler) RegisterDeployment(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}

	var req RegisterDeploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ImageOrCommit == "" {
		writeError(w, http.StatusBadRequest, "image_or_commit is required")
		return
	}
	if req.Version <= 0 {
		writeError(w, http.StatusBadRequest, "version must be positive")
		return
	}
	integrationUUID, ok := parseUUIDOrBadRequest(w, req.IntegrationID, "integration_id")
	if !ok {
		return
	}

	// Resolve integration to verify caller's workspace owns it.
	integration, ok := h.loadIntegrationForUser(w, r, req.IntegrationID)
	if !ok {
		return
	}
	if integration.ID != integrationUUID {
		// Defense in depth — should never happen because parseUUIDOrBadRequest
		// validated above and loadIntegrationForUser used the same id.
		writeError(w, http.StatusBadRequest, "integration id mismatch")
		return
	}

	created, err := h.Queries.RegisterIntegrationDeployment(r.Context(), db.RegisterIntegrationDeploymentParams{
		IntegrationID: integration.ID,
		ImageOrCommit: req.ImageOrCommit,
		HostUrl:       strToText(req.HostURL),
		Version:       req.Version,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to register deployment")
		return
	}

	userID, _ := requireUserID(w, r)
	workspaceID := uuidToString(integration.WorkspaceID)
	actorType, _ := h.resolveActor(r, userID, workspaceID)
	h.Audit.Record(r.Context(), audit.EventDeploymentRegistered, audit.Subject{
		WorkspaceID: workspaceID,
		ActorUserID: userID,
		ActorType:   actorType,
		Resource:    "deployment:" + uuidToString(created.ID),
		IPAddress:   r.RemoteAddr,
		Metadata: map[string]any{
			"integration_id":  uuidToString(integration.ID),
			"image_or_commit": req.ImageOrCommit,
			"version":         req.Version,
		},
	})

	writeJSON(w, http.StatusCreated, deploymentToResponse(created))
}

// HeartbeatDeployment: POST /api/deployments/{id}/heartbeat
// Updates last_heartbeat, optionally bumps config_applied_version and status.
// No audit-log row — heartbeats are too frequent.
func (h *Handler) HeartbeatDeployment(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}

	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "deployment id")
	if !ok {
		return
	}

	var req HeartbeatDeploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var statusArg pgtype.Text
	if req.Status != "" {
		statusArg = pgtype.Text{String: req.Status, Valid: true}
	}

	if err := h.Queries.UpdateIntegrationDeploymentHeartbeat(r.Context(), db.UpdateIntegrationDeploymentHeartbeatParams{
		ID:                   idUUID,
		ConfigAppliedVersion: pgtype.Int4{Int32: req.ConfigAppliedVersion, Valid: req.ConfigAppliedVersion > 0},
		Status:               statusArg,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record heartbeat")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "at": time.Now().UTC().Format(time.RFC3339)})
}

// ListDeployments: GET /api/integrations/{id}/deployments?limit=N
// Returns the most recent N deployments for an integration, newest first.
// Used by the web UI timeline view.
func (h *Handler) ListDeployments(w http.ResponseWriter, r *http.Request) {
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	limit := int32(20)
	if l := r.URL.Query().Get("limit"); l != "" {
		var parsed int
		_, err := fmt.Sscanf(l, "%d", &parsed)
		if err == nil && parsed > 0 && parsed <= 100 {
			limit = int32(parsed)
		}
	}
	rows, err := h.Queries.ListIntegrationDeployments(r.Context(), db.ListIntegrationDeploymentsParams{
		IntegrationID: integration.ID,
		Limit:         limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list deployments")
		return
	}
	now := time.Now()
	resp := make([]IntegrationDeploymentResponse, len(rows))
	for i, row := range rows {
		dr := deploymentToResponse(row)
		// Same stale-heartbeat downgrade as GetActiveDeployment so the timeline
		// view shows a consistent status.
		if row.LastHeartbeat.Valid && now.Sub(row.LastHeartbeat.Time) > 90*time.Second &&
			(dr.Status == "starting" || dr.Status == "running") {
			dr.Status = "degraded"
		}
		resp[i] = dr
	}
	writeJSON(w, http.StatusOK, resp)
}

// GetActiveDeployment: GET /api/integrations/{id}/active-deployment
func (h *Handler) GetActiveDeployment(w http.ResponseWriter, r *http.Request) {
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	dep, err := h.Queries.GetActiveIntegrationDeployment(r.Context(), integration.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "no active deployment")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to fetch deployment")
		return
	}
	resp := deploymentToResponse(dep)
	// Downgrade to "degraded" if heartbeat is stale.
	if dep.LastHeartbeat.Valid && time.Since(dep.LastHeartbeat.Time) > 90*time.Second {
		resp.Status = "degraded"
	}
	writeJSON(w, http.StatusOK, resp)
}
