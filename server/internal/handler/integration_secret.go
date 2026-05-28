package handler

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/audit"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// secretKeyPattern restricts secret keys to UPPER_SNAKE_CASE (digits OK after
// the first char). Keeps keys legible in audit logs and unambiguous when
// rendered into shell env or template files.
var secretKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// SecretKeyResponse is the key-list shape: never includes the value.
type SecretKeyResponse struct {
	Key       string  `json:"key"`
	Version   int32   `json:"version"`
	CreatedBy *string `json:"created_by,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// SecretValueResponse is returned by GET /secrets/{key} only.
type SecretValueResponse struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Version int32  `json:"version"`
}

// UpsertSecretRequest is the body for PUT /secrets/{key}.
type UpsertSecretRequest struct {
	Value string `json:"value"`
}

// UpsertIntegrationSecret: PUT /api/integrations/{id}/secrets/{key}
func (h *Handler) UpsertIntegrationSecret(w http.ResponseWriter, r *http.Request) {
	if h.Cipher == nil {
		writeError(w, http.StatusServiceUnavailable, "control plane cipher not initialized")
		return
	}
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	key := chi.URLParam(r, "key")
	if !secretKeyPattern.MatchString(key) {
		writeError(w, http.StatusBadRequest, "secret key must match ^[A-Z][A-Z0-9_]{0,127}$")
		return
	}

	var req UpsertSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	ciphertext, nonce, err := h.Cipher.Encrypt([]byte(req.Value))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	created, err := h.Queries.UpsertIntegrationSecret(r.Context(), db.UpsertIntegrationSecretParams{
		IntegrationID:  integration.ID,
		Key:            key,
		EncryptedValue: ciphertext,
		Nonce:          nonce,
		CreatedBy:      parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store secret")
		return
	}

	workspaceID := uuidToString(integration.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	// secret.set on first version, secret.rotated thereafter.
	event := audit.EventSecretSet
	if created.Version > 1 {
		event = audit.EventSecretRotated
	}
	h.Audit.Record(r.Context(), event, audit.Subject{
		WorkspaceID: workspaceID,
		ActorUserID: userID,
		ActorType:   actorType,
		Resource:    "secret:" + uuidToString(integration.ID) + ":" + key,
		IPAddress:   r.RemoteAddr,
		Metadata:    map[string]any{"version": created.Version},
	})
	// Subscribers may need to re-pull when secrets rotate.
	h.publish(protocol.EventIntegrationConfigChanged, workspaceID, actorType, actorID, map[string]any{
		"integration_id": uuidToString(integration.ID),
		"reason":         "secret-changed",
		"secret_key":     key,
		"secret_version": created.Version,
	})

	writeJSON(w, http.StatusOK, SecretKeyResponse{
		Key:       created.Key,
		Version:   created.Version,
		CreatedBy: uuidToPtr(created.CreatedBy),
		CreatedAt: timestampToString(created.CreatedAt),
		UpdatedAt: timestampToString(created.UpdatedAt),
	})
}

// GetIntegrationSecret: GET /api/integrations/{id}/secrets/{key}
// Returns the decrypted value. Every call writes a secret:read audit row.
// Gated by RequireCapability(CapSecretsRead).
func (h *Handler) GetIntegrationSecret(w http.ResponseWriter, r *http.Request) {
	if h.Cipher == nil {
		writeError(w, http.StatusServiceUnavailable, "control plane cipher not initialized")
		return
	}
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	key := chi.URLParam(r, "key")
	row, err := h.Queries.GetIntegrationSecret(r.Context(), db.GetIntegrationSecretParams{
		IntegrationID: integration.ID,
		Key:           key,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}

	plain, err := h.Cipher.Decrypt(row.EncryptedValue, row.Nonce)
	if err != nil {
		// Most likely cause: master key changed between encrypt and read.
		writeError(w, http.StatusInternalServerError, "decryption failed — likely a master key mismatch")
		return
	}

	workspaceID := uuidToString(integration.WorkspaceID)
	actorType, _ := h.resolveActor(r, userID, workspaceID)
	h.Audit.Record(r.Context(), audit.EventSecretRead, audit.Subject{
		WorkspaceID: workspaceID,
		ActorUserID: userID,
		ActorType:   actorType,
		Resource:    "secret:" + uuidToString(integration.ID) + ":" + key,
		IPAddress:   r.RemoteAddr,
		Metadata:    map[string]any{"version": row.Version},
	})

	writeJSON(w, http.StatusOK, SecretValueResponse{
		Key:     row.Key,
		Value:   string(plain),
		Version: row.Version,
	})
}

// ListIntegrationSecretKeys: GET /api/integrations/{id}/secrets
// Returns keys + metadata only (never values).
func (h *Handler) ListIntegrationSecretKeys(w http.ResponseWriter, r *http.Request) {
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListIntegrationSecretKeys(r.Context(), integration.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list secret keys")
		return
	}
	resp := make([]SecretKeyResponse, len(rows))
	for i, row := range rows {
		resp[i] = SecretKeyResponse{
			Key:       row.Key,
			Version:   row.Version,
			CreatedBy: uuidToPtr(row.CreatedBy),
			CreatedAt: timestampToString(row.CreatedAt),
			UpdatedAt: timestampToString(row.UpdatedAt),
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// DeleteIntegrationSecret: DELETE /api/integrations/{id}/secrets/{key}
func (h *Handler) DeleteIntegrationSecret(w http.ResponseWriter, r *http.Request) {
	integration, ok := h.loadIntegrationForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, _ := requireUserID(w, r)

	key := chi.URLParam(r, "key")
	if err := h.Queries.DeleteIntegrationSecret(r.Context(), db.DeleteIntegrationSecretParams{
		IntegrationID: integration.ID,
		Key:           key,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete secret")
		return
	}

	workspaceID := uuidToString(integration.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	h.Audit.Record(r.Context(), audit.EventSecretDeleted, audit.Subject{
		WorkspaceID: workspaceID,
		ActorUserID: userID,
		ActorType:   actorType,
		Resource:    "secret:" + uuidToString(integration.ID) + ":" + key,
		IPAddress:   r.RemoteAddr,
	})
	h.publish(protocol.EventIntegrationConfigChanged, workspaceID, actorType, actorID, map[string]any{
		"integration_id": uuidToString(integration.ID),
		"reason":         "secret-deleted",
		"secret_key":     key,
	})
	w.WriteHeader(http.StatusNoContent)
}
