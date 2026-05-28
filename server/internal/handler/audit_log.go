package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AuditLogResponse is the wire format. Timestamps stringified for consistency
// with other endpoints; metadata is the raw JSONB bytes returned to the
// client as a parsed object via json.RawMessage on serialization.
type AuditLogResponse struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	ActorUserID *string        `json:"actor_user_id,omitempty"`
	ActorType   string         `json:"actor_type"`
	EventType   string         `json:"event_type"`
	Resource    string         `json:"resource"`
	Metadata    map[string]any `json:"metadata"`
	IPAddress   *string        `json:"ip_address,omitempty"`
	CreatedAt   string         `json:"created_at"`
}

// ListAuditLogs: GET /api/audit-logs
// Query params (all optional):
//   - resource: exact match on a single resource string (e.g.
//     "secret:abc-uuid:FEISHU_APP_SECRET")
//   - event_types: comma-separated allowlist (e.g. "secret:read,secret:set")
//   - since: RFC3339 timestamp or YYYY-MM-DD (UTC midnight)
//   - limit: 1-500, default 100
//
// Gated by RequireCapability(CapIntegrationsRead) — anyone who can read
// integration metadata can read audit history; secret values are never
// in audit_log so no special gate beyond that. Workspace scope enforced
// via the standard middleware.
func (h *Handler) ListAuditLogs(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	q := r.URL.Query()

	var resource pgtype.Text
	if v := strings.TrimSpace(q.Get("resource")); v != "" {
		resource = pgtype.Text{String: v, Valid: true}
	}

	var eventTypes []string
	if v := strings.TrimSpace(q.Get("event_types")); v != "" {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				eventTypes = append(eventTypes, t)
			}
		}
	}
	if eventTypes == nil {
		eventTypes = []string{} // cardinality(0) → skip filter
	}

	var since pgtype.Timestamptz
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		ts, err := parseUpdatedAfter(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since — expected YYYY-MM-DD or RFC3339")
			return
		}
		since = pgtype.Timestamptz{Time: ts, Valid: true}
	}

	limit := int32(100)
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = int32(n)
		}
	}

	rows, err := h.Queries.ListAuditLogsByWorkspace(r.Context(), db.ListAuditLogsByWorkspaceParams{
		WorkspaceID: wsUUID,
		Limit:       limit,
		Resource:    resource,
		EventTypes:  eventTypes,
		Since:       since,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list audit logs")
		return
	}

	resp := make([]AuditLogResponse, len(rows))
	for i, row := range rows {
		var ip *string
		if row.IpAddress != nil {
			s := row.IpAddress.String()
			ip = &s
		}
		resp[i] = AuditLogResponse{
			ID:          uuidToString(row.ID),
			WorkspaceID: uuidToString(row.WorkspaceID),
			ActorUserID: uuidToPtr(row.ActorUserID),
			ActorType:   row.ActorType,
			EventType:   row.EventType,
			Resource:    row.Resource,
			Metadata:    decodeJSONB(row.Metadata),
			IPAddress:   ip,
			CreatedAt:   row.CreatedAt.Time.Format(time.RFC3339),
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// decodeJSONB is a small helper that unmarshals a JSONB-as-bytes field into
// a generic map. Empty / invalid input falls back to an empty map so the
// response shape stays stable.
func decodeJSONB(b []byte) map[string]any {
	if len(b) == 0 {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// Reference middleware so its constants stay in scope for the audit gate
// even when this file's own route wiring lives in cmd/server.
var _ = middleware.CapIntegrationsRead
