package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// registerDeployment is a test helper that POSTs to RegisterDeployment with a
// minimal payload. Returns the deployment id + HTTP status code.
func registerDeployment(t *testing.T, integrationID string, version int32) (string, int) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/deployments",
		map[string]any{
			"integration_id":  integrationID,
			"image_or_commit": "sha:abc123",
			"host_url":        "https://example.com",
			"version":         version,
		})
	testHandler.RegisterDeployment(w, req)
	if w.Code != http.StatusCreated {
		return "", w.Code
	}
	var resp IntegrationDeploymentResponse
	json.NewDecoder(w.Body).Decode(&resp)
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM integration_deployment WHERE id = $1`, resp.ID)
		testPool.Exec(ctx, `DELETE FROM audit_log WHERE resource = $1`, "deployment:"+resp.ID)
	})
	return resp.ID, w.Code
}

func TestRegisterDeployment_201AndWritesAudit(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "mcp-server", "name": "deploy-register-test",
	})
	depID, code := registerDeployment(t, integration.ID, 1)
	if code != http.StatusCreated {
		t.Fatalf("RegisterDeployment: expected 201, got %d", code)
	}
	if depID == "" {
		t.Errorf("missing deployment id in response")
	}

	// audit row should exist for the registered deployment.
	var event string
	err := testPool.QueryRow(context.Background(),
		`SELECT event_type FROM audit_log WHERE resource = $1`,
		"deployment:"+depID).Scan(&event)
	if err != nil {
		t.Fatalf("audit row for deployment:registered missing: %v", err)
	}
	if event != "deployment:registered" {
		t.Errorf("expected event_type='deployment:registered', got %q", event)
	}
}

func TestHeartbeatDeployment_UpdatesLastHeartbeat(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "mcp-server", "name": "deploy-heartbeat-test",
	})
	depID, _ := registerDeployment(t, integration.ID, 1)

	// Read the pre-heartbeat last_heartbeat (should be NULL).
	var before *time.Time
	testPool.QueryRow(context.Background(),
		`SELECT last_heartbeat FROM integration_deployment WHERE id = $1`, depID).Scan(&before)
	if before != nil {
		t.Fatalf("expected last_heartbeat NULL before any heartbeat, got %v", before)
	}

	// Heartbeat.
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/deployments/"+depID+"/heartbeat",
		map[string]any{"config_applied_version": 1, "status": "running"})
	req = withURLParam(req, "id", depID)
	testHandler.HeartbeatDeployment(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Heartbeat: expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	// After heartbeat, last_heartbeat must be set.
	var after *time.Time
	testPool.QueryRow(context.Background(),
		`SELECT last_heartbeat FROM integration_deployment WHERE id = $1`, depID).Scan(&after)
	if after == nil {
		t.Errorf("last_heartbeat not updated by heartbeat call")
	}
}

func TestGetActiveDeployment_ReturnsLatest(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "mcp-server", "name": "deploy-active-test",
	})
	depID1, _ := registerDeployment(t, integration.ID, 1)
	depID2, _ := registerDeployment(t, integration.ID, 2)

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/integrations/"+integration.ID+"/active-deployment", nil)
	req = withURLParam(req, "id", integration.ID)
	testHandler.GetActiveDeployment(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetActiveDeployment: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp IntegrationDeploymentResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID != depID2 {
		t.Errorf("expected latest deployment (%s), got %s", depID2, resp.ID)
	}
	_ = depID1 // not the active one
}

func TestGetActiveDeployment_StaleHeartbeatReportsDegraded(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "mcp-server", "name": "deploy-stale-test",
	})
	depID, _ := registerDeployment(t, integration.ID, 1)

	// Manually backdate last_heartbeat to 200s ago — past the 90s threshold.
	_, err := testPool.Exec(context.Background(),
		`UPDATE integration_deployment SET last_heartbeat = NOW() - INTERVAL '200 seconds', status = 'running' WHERE id = $1`,
		depID)
	if err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/integrations/"+integration.ID+"/active-deployment", nil)
	req = withURLParam(req, "id", integration.ID)
	testHandler.GetActiveDeployment(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp IntegrationDeploymentResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Status != "degraded" {
		t.Errorf("expected status=degraded for stale heartbeat, got %q", resp.Status)
	}
}

func TestRegisterDeployment_InvalidIntegrationID400(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	w := httptest.NewRecorder()
	testHandler.RegisterDeployment(w, newRequest("POST", "/api/deployments",
		map[string]any{
			"integration_id":  "not-a-uuid",
			"image_or_commit": "sha:abc",
			"version":         1,
		}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid integration_id, got %d", w.Code)
	}
}

func TestRegisterDeployment_MissingFields400(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "mcp-server", "name": "deploy-missing-fields-test",
	})

	// Missing image_or_commit.
	w := httptest.NewRecorder()
	testHandler.RegisterDeployment(w, newRequest("POST", "/api/deployments",
		map[string]any{
			"integration_id": integration.ID,
			"version":        1,
		}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing image_or_commit: expected 400, got %d", w.Code)
	}

	// Missing version (or version <= 0).
	w = httptest.NewRecorder()
	testHandler.RegisterDeployment(w, newRequest("POST", "/api/deployments",
		map[string]any{
			"integration_id":  integration.ID,
			"image_or_commit": "sha:abc",
			"version":         0,
		}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid version: expected 400, got %d", w.Code)
	}
}
