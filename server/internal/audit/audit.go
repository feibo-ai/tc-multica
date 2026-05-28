// Package audit writes append-only audit log entries for control-plane reads
// and writes. Failures are logged but never surfaced to the caller — audit
// must not block business operations.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/netip"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Event types — also serve as audit_log.event_type column values. Colon
// convention matches protocol.Event* constants.
const (
	EventIntegrationCreated       = "integration:created"
	EventIntegrationConfigChanged = "integration:config-changed"
	EventIntegrationRestarted     = "integration:restarted"
	EventIntegrationRedeployed    = "integration:redeployed"
	EventIntegrationDeleted       = "integration:deleted"

	EventSecretSet     = "secret:set"
	EventSecretRotated = "secret:rotated"
	EventSecretDeleted = "secret:deleted"
	EventSecretRead    = "secret:read" // sensitive — every read recorded
)

// Subject is the input shape for Recorder.Record. WorkspaceID is required;
// everything else is best-effort and may be empty.
type Subject struct {
	WorkspaceID string
	ActorUserID string         // empty for system/agent actors
	ActorType   string         // "user" | "agent" | "system"; defaults to "user"
	Resource    string         // e.g. "integration:<uuid>" or "secret:<integration_id>:<key>"
	IPAddress   string         // raw r.RemoteAddr is fine; port is stripped if present
	Metadata    map[string]any // marshaled to jsonb; nil → {}
}

// Recorder persists audit entries via the sqlc-generated CreateAuditLog query.
type Recorder struct {
	queries *db.Queries
}

// NewRecorder builds a Recorder against a queries instance.
func NewRecorder(q *db.Queries) *Recorder {
	return &Recorder{queries: q}
}

// Record writes one audit_log row. The caller does not need to check the
// returned value — failures are logged but never propagated, because audit
// is a side channel and must not block control-plane operations.
func (r *Recorder) Record(ctx context.Context, eventType string, s Subject) {
	if r == nil || r.queries == nil {
		return
	}

	meta := s.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		slog.Error("audit: metadata marshal failed",
			"event_type", eventType, "resource", s.Resource, "error", err)
		metaJSON = []byte("{}")
	}

	wsUUID, err := util.ParseUUID(s.WorkspaceID)
	if err != nil {
		slog.Error("audit: invalid workspace_id",
			"event_type", eventType, "workspace_id", s.WorkspaceID, "error", err)
		return
	}

	actorType := s.ActorType
	if actorType == "" {
		actorType = "user"
	}

	_, err = r.queries.CreateAuditLog(ctx, db.CreateAuditLogParams{
		WorkspaceID: wsUUID,
		ActorUserID: optionalUUID(s.ActorUserID),
		ActorType:   actorType,
		EventType:   eventType,
		Resource:    s.Resource,
		Metadata:    metaJSON,
		IpAddress:   parseIPAddr(s.IPAddress),
	})
	if err != nil {
		slog.Error("audit: insert failed",
			"event_type", eventType, "resource", s.Resource, "error", err)
	}
}

// optionalUUID returns a nullable pgtype.UUID. Empty input → pgtype.UUID with
// Valid=false (i.e. SQL NULL).
func optionalUUID(s string) pgtype.UUID {
	if s == "" {
		return pgtype.UUID{}
	}
	u, err := util.ParseUUID(s)
	if err != nil {
		return pgtype.UUID{}
	}
	return u
}

// parseIPAddr accepts either a bare IP literal or a "host:port" pair (as in
// http.Request.RemoteAddr). Returns nil on any parse failure so the column
// stays NULL — audit must not panic on a malformed address.
func parseIPAddr(s string) *netip.Addr {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// Strip port if present.
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return &addr
}
