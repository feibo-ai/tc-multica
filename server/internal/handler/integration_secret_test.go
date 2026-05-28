package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// putSecret is a small helper for the secret tests. Returns HTTP status code.
func putSecret(t *testing.T, integrationID, key, value string) int {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/integrations/"+integrationID+"/secrets/"+key,
		map[string]any{"value": value})
	req = withURLParams(req, "id", integrationID, "key", key)
	testHandler.UpsertIntegrationSecret(w, req)
	return w.Code
}

func TestUpsertAndGetSecret_RoundTripsDecryptedValue(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "feishu", "name": "secret-roundtrip-test",
	})

	if code := putSecret(t, integration.ID, "FEISHU_APP_SECRET", "very-secret-value"); code != http.StatusOK {
		t.Fatalf("PUT secret: expected 200, got %d", code)
	}

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/integrations/"+integration.ID+"/secrets/FEISHU_APP_SECRET", nil)
	req = withURLParams(req, "id", integration.ID, "key", "FEISHU_APP_SECRET")
	testHandler.GetIntegrationSecret(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET secret: expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp SecretValueResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Value != "very-secret-value" {
		t.Errorf("decrypted value mismatch: got %q, want %q", resp.Value, "very-secret-value")
	}
	if resp.Key != "FEISHU_APP_SECRET" {
		t.Errorf("key mismatch: got %q", resp.Key)
	}
}

func TestListSecretKeys_OmitsValues(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "feishu", "name": "secret-list-test",
	})
	putSecret(t, integration.ID, "FEISHU_APP_SECRET", "value-a")
	putSecret(t, integration.ID, "FEISHU_VERIFICATION_TOKEN", "value-b")

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/integrations/"+integration.ID+"/secrets", nil)
	req = withURLParam(req, "id", integration.ID)
	testHandler.ListIntegrationSecretKeys(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var rows []SecretKeyResponse
	json.NewDecoder(w.Body).Decode(&rows)
	if len(rows) != 2 {
		t.Fatalf("expected 2 key rows, got %d", len(rows))
	}
	// SecretKeyResponse type literally has no Value field — that's the test.
	// But also verify the JSON body did not leak a "value" property.
	bodyStr := w.Body.String()
	if strings.Contains(bodyStr, "value-a") || strings.Contains(bodyStr, "value-b") {
		t.Errorf("list response leaked secret value: %s", bodyStr)
	}
}

func TestUpsertSecret_InvalidKeyPattern400(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "feishu", "name": "secret-bad-key-test",
	})

	// Keys must match ^[A-Z][A-Z0-9_]{0,127}$. Lowercase, leading-digit, dashes,
	// and dots should all be rejected at the handler boundary.
	for _, badKey := range []string{"lowercase", "1LEADING_DIGIT", "WITH-DASH", "with.dot", ""} {
		w := httptest.NewRecorder()
		req := newRequest("PUT", "/api/integrations/"+integration.ID+"/secrets/"+badKey,
			map[string]any{"value": "x"})
		req = withURLParams(req, "id", integration.ID, "key", badKey)
		testHandler.UpsertIntegrationSecret(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("key %q: expected 400, got %d", badKey, w.Code)
		}
	}
}

func TestDeleteSecret_RemovesRow(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "feishu", "name": "secret-delete-test",
	})
	putSecret(t, integration.ID, "DELETE_ME", "x")

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/integrations/"+integration.ID+"/secrets/DELETE_ME", nil)
	req = withURLParams(req, "id", integration.ID, "key", "DELETE_ME")
	testHandler.DeleteIntegrationSecret(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}

	// Verify in DB.
	var count int
	testPool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM integration_secret WHERE integration_id = $1 AND key = $2`,
		integration.ID, "DELETE_ME").Scan(&count)
	if count != 0 {
		t.Errorf("secret not deleted (count=%d)", count)
	}
}

func TestSecretRead_WritesAuditRow(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "feishu", "name": "secret-audit-test",
	})
	putSecret(t, integration.ID, "AUDIT_TEST_KEY", "x")

	// Read once.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/integrations/"+integration.ID+"/secrets/AUDIT_TEST_KEY", nil)
	req = withURLParams(req, "id", integration.ID, "key", "AUDIT_TEST_KEY")
	testHandler.GetIntegrationSecret(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET secret: %d %s", w.Code, w.Body.String())
	}

	var event string
	err := testPool.QueryRow(context.Background(),
		`SELECT event_type FROM audit_log
		 WHERE resource = $1 AND event_type = 'secret:read'`,
		"secret:"+integration.ID+":AUDIT_TEST_KEY").Scan(&event)
	if err != nil {
		t.Fatalf("audit row for secret:read missing: %v", err)
	}
}

func TestGetSecret_CrossWorkspaceReturns404(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "feishu", "name": "secret-cross-ws-test",
	})
	putSecret(t, integration.ID, "FOO", "bar")

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/integrations/"+integration.ID+"/secrets/FOO", nil)
	req.Header.Set("X-Workspace-ID", "00000000-0000-0000-0000-000000000000")
	req = withURLParams(req, "id", integration.ID, "key", "FOO")
	testHandler.GetIntegrationSecret(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 cross-workspace, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestGetSecret_NonExistentKey404(t *testing.T) {
	ensureControlPlaneTestDeps(t)
	integration := createIntegrationViaHandler(t, map[string]any{
		"kind": "feishu", "name": "secret-missing-test",
	})

	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/integrations/"+integration.ID+"/secrets/DOES_NOT_EXIST", nil)
	req = withURLParams(req, "id", integration.ID, "key", "DOES_NOT_EXIST")
	testHandler.GetIntegrationSecret(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

