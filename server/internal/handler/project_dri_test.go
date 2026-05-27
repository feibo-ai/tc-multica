package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestCreateProject_WithDRI(t *testing.T) {
	driID := insertHandlerTestUser(t, "dri-create@test.local")

	body := strings.NewReader(`{
		"title": "P with DRI",
		"status": "planned",
		"priority": "none",
		"dri_user_id": "` + driID + `"
	}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/projects?workspace_id="+testWorkspaceID, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	testHandler.CreateProject(w, req)
	if w.Code != 201 {
		t.Fatalf("CreateProject: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if got := resp["dri_user_id"]; got != driID {
		t.Fatalf("dri_user_id: got %v, want %s", got, driID)
	}
	// Cleanup the project we just created
	t.Cleanup(func() {
		if pid, ok := resp["id"].(string); ok {
			testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, pid)
		}
	})
}

func TestAssignProjectDRI_SetAndClear(t *testing.T) {
	projID := insertHandlerTestProject(t, "p-needs-dri")
	driID := insertHandlerTestUser(t, "dri-assign@test.local")

	// Set
	set := strings.NewReader(`{"dri_user_id":"` + driID + `"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/projects/"+projID+"/dri?workspace_id="+testWorkspaceID, set)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", projID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.AssignProjectDRI(w, req)
	if w.Code != 200 {
		t.Fatalf("AssignProjectDRI set: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if got := resp["dri_user_id"]; got != driID {
		t.Fatalf("dri_user_id after set: got %v, want %s", got, driID)
	}

	// Clear via empty string
	clr := strings.NewReader(`{"dri_user_id":""}`)
	w = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/api/projects/"+projID+"/dri?workspace_id="+testWorkspaceID, clr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	rctx = chi.NewRouteContext()
	rctx.URLParams.Add("id", projID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	testHandler.AssignProjectDRI(w, req)
	if w.Code != 200 {
		t.Fatalf("AssignProjectDRI clear: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp = nil
	json.Unmarshal(w.Body.Bytes(), &resp)
	if got := resp["dri_user_id"]; got != nil {
		t.Fatalf("dri_user_id after clear: got %v, want nil", got)
	}
}

func TestListProjects_WithoutDRI(t *testing.T) {
	noDRIID := insertHandlerTestProject(t, "without-dri")
	// Other project with DRI for negative control
	withDRIID := insertHandlerTestProject(t, "with-dri")
	driID := insertHandlerTestUser(t, "dri-list@test.local")
	_, err := testPool.Exec(context.Background(),
		`UPDATE project SET dri_user_id = $2 WHERE id = $1`, withDRIID, driID)
	if err != nil {
		t.Fatalf("backdate DRI: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/projects?without_dri=true&workspace_id="+testWorkspaceID, nil)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	testHandler.ListProjects(w, req)
	if w.Code != 200 {
		t.Fatalf("ListProjects without_dri: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	json.Unmarshal(w.Body.Bytes(), &rows)

	var foundNoDRI, foundWithDRI bool
	for _, r := range rows {
		switch r["id"] {
		case noDRIID:
			foundNoDRI = true
		case withDRIID:
			foundWithDRI = true
		}
		if dri := r["dri_user_id"]; dri != nil {
			t.Fatalf("without_dri=true returned project with DRI: %v (id=%v)", dri, r["id"])
		}
	}
	if !foundNoDRI {
		t.Fatalf("expected project %s in without-DRI list", noDRIID)
	}
	if foundWithDRI {
		t.Fatalf("did not expect project %s (with DRI) in without-DRI list", withDRIID)
	}
}

// insertHandlerTestProject writes a project row directly via SQL and registers
// a cleanup hook. Mirrors insertHandlerTestSkill / insertHandlerTestUser — used
// by DRI-assignment tests that need a project to mutate without exercising the
// create handler.
func insertHandlerTestProject(t *testing.T, namePrefix string) string {
	t.Helper()
	title := namePrefix + "-" + t.Name()
	var id string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO project (workspace_id, title, status, priority)
		VALUES ($1, $2, 'planned', 'none')
		RETURNING id
	`, testWorkspaceID, title).Scan(&id); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, id)
	})
	return id
}
