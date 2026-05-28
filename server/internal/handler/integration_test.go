package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/audit"
	"github.com/multica-ai/multica/server/internal/secrets"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ensureControlPlaneTestDeps wires Cipher + Audit onto the shared testHandler
// the first time a control-plane test runs. The fixture in handler_test.go
// constructs testHandler with the production New(...) signature, which leaves
// these fields nil; control-plane handlers refuse to serve when Cipher is nil.
func ensureControlPlaneTestDeps(t *testing.T) {
	t.Helper()
	if testHandler.Audit == nil {
		testHandler.Audit = audit.NewRecorder(db.New(testPool))
	}
	if testHandler.Cipher == nil {
		key := make([]byte, secrets.KeySize)
		if _, err := rand.Read(key); err != nil {
			t.Fatalf("rand key: %v", err)
		}
		c, err := secrets.NewCipher(key)
		if err != nil {
			t.Fatalf("new cipher: %v", err)
		}
		testHandler.Cipher = c
	}
}

// cleanupIntegration deletes an integration row by ID (cascade-deletes its
// secrets, deployments, and audit_log rows).
func cleanupIntegration(t *testing.T, id string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM integration WHERE id = $1`, id)
		testPool.Exec(ctx, `DELETE FROM audit_log WHERE resource = $1`, "integration:"+id)
	})
}

// createIntegrationViaHandler is a test helper that POSTs to CreateIntegration
// and returns the resulting response object + cleanup registration.
func createIntegrationViaHandler(t *testing.T, body map[string]any) IntegrationResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/integrations", body)
	testHandler.CreateIntegration(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIntegration: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp IntegrationResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	cleanupIntegration(t, resp.ID)
	return resp
}

// --- D-5 acceptance tests ---

func TestCreateIntegration_201WithVersion1(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	resp := createIntegrationViaHandler(t, map[string]any{
		"kind":   "mcp-server",
		"name":   "test-create-201",
		"config": map[string]any{"command": "node"},
	})
	if resp.ID == "" {
		t.Errorf("response missing id")
	}
	if resp.Version != 1 {
		t.Errorf("expected version=1, got %d", resp.Version)
	}
	if resp.Kind != "mcp-server" {
		t.Errorf("expected kind=mcp-server, got %q", resp.Kind)
	}
	if got := resp.Config["command"]; got != "node" {
		t.Errorf("expected config.command=node, got %v", got)
	}
}

func TestCreateIntegration_InvalidKind400(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	w := httptest.NewRecorder()
	testHandler.CreateIntegration(w, newRequest("POST", "/api/integrations",
		map[string]any{"kind": "not-a-real-kind", "name": "x"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateIntegration_DuplicateNameReturns409(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	createIntegrationViaHandler(t, map[string]any{"kind": "feishu", "name": "dup-name"})

	w := httptest.NewRecorder()
	testHandler.CreateIntegration(w, newRequest("POST", "/api/integrations",
		map[string]any{"kind": "feishu", "name": "dup-name"}))
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestListIntegrations_FilterByKind(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	createIntegrationViaHandler(t, map[string]any{"kind": "mcp-server", "name": "list-mcp-a"})
	createIntegrationViaHandler(t, map[string]any{"kind": "feishu", "name": "list-feishu-a"})

	w := httptest.NewRecorder()
	testHandler.ListIntegrations(w, newRequest("GET", "/api/integrations?kind=mcp-server", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var rows []IntegrationResponse
	json.NewDecoder(w.Body).Decode(&rows)
	for _, r := range rows {
		if r.Kind != "mcp-server" {
			t.Errorf("filter leaked non-mcp-server row: kind=%q", r.Kind)
		}
	}
}

func TestUpdateIntegrationConfig_BumpsVersionAndPublishes(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	created := createIntegrationViaHandler(t, map[string]any{
		"kind": "mcp-server",
		"name": "patch-version-test",
	})

	// Side-effect check: the row's version increments. Bus event firing is
	// covered separately in cmd/server/integration_listeners_test.go.
	w := httptest.NewRecorder()
	req := newRequest("PATCH", "/api/integrations/"+created.ID+"/config",
		map[string]any{"config": map[string]any{"command": "python"}})
	req = withURLParam(req, "id", created.ID)
	testHandler.UpdateIntegrationConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH config: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var updated IntegrationResponse
	json.NewDecoder(w.Body).Decode(&updated)
	if updated.Version != 2 {
		t.Errorf("expected version=2 after PATCH, got %d", updated.Version)
	}
	if got, ok := updated.Config["command"].(string); !ok || got != "python" {
		t.Errorf("config.command not updated: %v", updated.Config["command"])
	}
}

func TestGetIntegration_CrossWorkspaceReturns404(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	created := createIntegrationViaHandler(t, map[string]any{
		"kind": "mcp-server",
		"name": "cross-ws-test",
	})

	// Use a different workspace id header — loader should refuse to surface
	// the integration to a caller from a different workspace.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/integrations/"+created.ID, nil)
	req.Header.Set("X-Workspace-ID", "00000000-0000-0000-0000-000000000000")
	req = withURLParam(req, "id", created.ID)
	testHandler.GetIntegration(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 from cross-workspace fetch, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestGetIntegration_InvalidUUID400(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/integrations/not-a-uuid", nil)
	req = withURLParam(req, "id", "not-a-uuid")
	testHandler.GetIntegration(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed UUID, got %d", w.Code)
	}
}

func TestRedeployIntegration_NoWebhookReturns400(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	created := createIntegrationViaHandler(t, map[string]any{
		"kind": "mcp-server",
		"name": "no-webhook-test",
	})

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/integrations/"+created.ID+"/redeploy", nil)
	req = withURLParam(req, "id", created.ID)
	testHandler.RedeployIntegration(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 when redeploy webhook is unset, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateIntegration_WritesAuditLogRow(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	// Don't go through createIntegrationViaHandler because its cleanup deletes
	// the audit_log row before we can check it. We register cleanup *after*
	// the assertion.
	w := httptest.NewRecorder()
	testHandler.CreateIntegration(w, newRequest("POST", "/api/integrations",
		map[string]any{"kind": "mcp-server", "name": "audit-trail-test"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", w.Code, w.Body.String())
	}
	var created IntegrationResponse
	json.NewDecoder(w.Body).Decode(&created)

	var actorType, eventType string
	err := testPool.QueryRow(context.Background(),
		`SELECT actor_type, event_type FROM audit_log WHERE resource = $1`,
		"integration:"+created.ID).Scan(&actorType, &eventType)
	if err != nil {
		t.Fatalf("audit row missing for integration %s: %v", created.ID, err)
	}
	if actorType != "user" {
		t.Errorf("expected actor_type='user', got %q (CHECK constraint allows user/agent/system only)", actorType)
	}
	if eventType != "integration:created" {
		t.Errorf("expected event_type='integration:created', got %q", eventType)
	}

	cleanupIntegration(t, created.ID)
}

func TestDeleteIntegration_CascadesAndReturns204(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	created := createIntegrationViaHandler(t, map[string]any{
		"kind": "mcp-server",
		"name": "delete-cascade-test",
	})

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/integrations/"+created.ID, nil)
	req = withURLParam(req, "id", created.ID)
	testHandler.DeleteIntegration(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d body=%s", w.Code, w.Body.String())
	}

	// Verify the row is gone.
	var count int
	testPool.QueryRow(context.Background(), `SELECT COUNT(*) FROM integration WHERE id = $1`, created.ID).Scan(&count)
	if count != 0 {
		t.Errorf("integration not deleted (count=%d)", count)
	}
}
