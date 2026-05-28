package main

import (
	"encoding/json"
	"log/slog"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerIntegrationListeners forwards control-plane domain events to every
// WS client subscribed to the originating workspace. Called from main() only
// when MULTICA_CONTROL_PLANE_ENABLED=true. Mirrors the pattern in listeners.go.
func registerIntegrationListeners(bus *events.Bus, b realtime.Broadcaster) {
	forward := func(e events.Event) {
		data, err := json.Marshal(map[string]any{
			"type":       e.Type,
			"payload":    e.Payload,
			"actor_id":   e.ActorID,
			"actor_type": e.ActorType,
		})
		if err != nil {
			slog.Error("integration listener: marshal failed",
				"event_type", e.Type, "error", err)
			return
		}
		realtime.M.RecordEvent(e.Type)
		b.BroadcastToWorkspace(e.WorkspaceID, data)
	}

	bus.Subscribe(protocol.EventIntegrationConfigChanged, forward)
	bus.Subscribe(protocol.EventIntegrationStatusChanged, forward)
}
