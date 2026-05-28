package audit_test

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/audit"
)

// Recorder is mostly a thin wrapper around sqlc; the contract worth verifying
// in unit tests is the input-validation surface (nil receiver, invalid UUID,
// malformed IP) because those are the cases that could leak through to a
// runtime panic and take down a request path.

func TestRecord_NilReceiverDoesNotPanic(t *testing.T) {
	var r *audit.Recorder
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("nil Recorder.Record panicked: %v", rec)
		}
	}()
	r.Record(context.Background(), audit.EventSecretRead, audit.Subject{})
}

func TestRecord_InvalidWorkspaceIDIsLoggedNotPanicked(t *testing.T) {
	// queries is nil, but the workspace-id parse step happens before the
	// query call, so this must short-circuit cleanly without panicking.
	r := audit.NewRecorder(nil)
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("Record with invalid workspace_id panicked: %v", rec)
		}
	}()
	r.Record(context.Background(), audit.EventIntegrationCreated, audit.Subject{
		WorkspaceID: "not-a-uuid",
		Resource:    "integration:test",
	})
}

func TestEventConstants_UseColonConvention(t *testing.T) {
	all := []string{
		audit.EventIntegrationCreated,
		audit.EventIntegrationConfigChanged,
		audit.EventIntegrationRestarted,
		audit.EventIntegrationRedeployed,
		audit.EventIntegrationDeleted,
		audit.EventSecretSet,
		audit.EventSecretRotated,
		audit.EventSecretDeleted,
		audit.EventSecretRead,
	}
	for _, e := range all {
		// Colon convention matches existing protocol.Event* constants and
		// the audit_log.event_type column shape used by ListAuditLogsByEventTypes.
		hasColon := false
		for _, c := range e {
			if c == ':' {
				hasColon = true
				break
			}
		}
		if !hasColon {
			t.Errorf("event %q should use colon convention (integration:created)", e)
		}
	}
}
