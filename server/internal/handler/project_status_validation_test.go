package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// An agent once created a project with status="active" (valid for chat
// sessions, not projects), which slipped past the handler and hit the DB
// project_status_check constraint — surfacing as a 500 plus a Postgres error
// in the logs instead of a clean 400. These guard the handler-level enum
// validation that closes that gap.

func TestCreateProject_RejectsInvalidStatus(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects?workspace_id="+testWorkspaceID,
		strings.NewReader(`{"title":"bad-status","status":"active"}`))
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	testHandler.CreateProject(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=active: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateProject_RejectsInvalidPriority(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects?workspace_id="+testWorkspaceID,
		strings.NewReader(`{"title":"bad-priority","priority":"critical"}`))
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	testHandler.CreateProject(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("priority=critical: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateProject_AcceptsValidStatus(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects?workspace_id="+testWorkspaceID,
		strings.NewReader(`{"title":"ok-status","status":"in_progress","priority":"high"}`))
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	testHandler.CreateProject(w, req)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("valid status/priority: expected 2xx, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	t.Cleanup(func() {
		if pid, ok := resp["id"].(string); ok {
			testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, pid)
		}
	})
}

func TestUpdateProject_RejectsInvalidStatus(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	projID := insertHandlerTestProject(t, "p-update-invalid-status")

	w := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/projects/"+projID+"?workspace_id="+testWorkspaceID,
		strings.NewReader(`{"status":"active"}`))
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.UpdateProject(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("update status=active: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
