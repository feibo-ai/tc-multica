package main

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Reuses fakeBroadcaster from listeners_scope_test.go.

func TestRegisterIntegrationListeners_ConfigChangedForwards(t *testing.T) {
	bus := events.New()
	fb := &fakeBroadcaster{}
	registerIntegrationListeners(bus, fb)

	bus.Publish(events.Event{
		Type:        protocol.EventIntegrationConfigChanged,
		WorkspaceID: "ws-abc",
		ActorType:   "user",
		ActorID:     "u-1",
		Payload:     map[string]any{"integration_id": "i-1", "reason": "config-changed"},
	})

	if len(fb.workspaceCalls) != 1 {
		t.Fatalf("expected 1 workspace broadcast, got %d", len(fb.workspaceCalls))
	}
	c := fb.workspaceCalls[0]
	if c.workspaceID != "ws-abc" {
		t.Errorf("workspace mismatch: got %q, want ws-abc", c.workspaceID)
	}
	var decoded map[string]any
	if err := json.Unmarshal(c.msg, &decoded); err != nil {
		t.Fatalf("invalid JSON in broadcast: %v", err)
	}
	if decoded["type"] != protocol.EventIntegrationConfigChanged {
		t.Errorf("type mismatch: got %v", decoded["type"])
	}
	payload, ok := decoded["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing or wrong shape")
	}
	if payload["integration_id"] != "i-1" {
		t.Errorf("payload integration_id mismatch: got %v", payload["integration_id"])
	}
}

func TestRegisterIntegrationListeners_StatusChangedForwards(t *testing.T) {
	bus := events.New()
	fb := &fakeBroadcaster{}
	registerIntegrationListeners(bus, fb)

	bus.Publish(events.Event{
		Type:        protocol.EventIntegrationStatusChanged,
		WorkspaceID: "ws-xyz",
		Payload:     map[string]any{"status": "degraded"},
	})

	if len(fb.workspaceCalls) != 1 {
		t.Fatalf("expected 1 broadcast, got %d", len(fb.workspaceCalls))
	}
	if fb.workspaceCalls[0].workspaceID != "ws-xyz" {
		t.Errorf("workspace mismatch: got %q, want ws-xyz", fb.workspaceCalls[0].workspaceID)
	}
}

func TestRegisterIntegrationListeners_DoesNotForwardOtherEvents(t *testing.T) {
	bus := events.New()
	fb := &fakeBroadcaster{}
	registerIntegrationListeners(bus, fb)

	bus.Publish(events.Event{
		Type:        "issue:created",
		WorkspaceID: "ws-abc",
		Payload:     map[string]any{},
	})
	if len(fb.workspaceCalls) != 0 {
		t.Fatalf("expected 0 broadcasts for unrelated event, got %d", len(fb.workspaceCalls))
	}
}
