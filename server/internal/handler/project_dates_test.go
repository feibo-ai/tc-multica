package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// projectDateRow reads start_date / due_date straight from the DB so the test
// asserts persisted state, not just the handler echo. Dates are stored as DATE
// and rendered back as YYYY-MM-DD strings (or NULL).
func projectDateRow(t *testing.T, projID string) (start, due *string) {
	t.Helper()
	var s, d *string
	if err := testPool.QueryRow(context.Background(),
		`SELECT to_char(start_date, 'YYYY-MM-DD'), to_char(due_date, 'YYYY-MM-DD')
		 FROM project WHERE id = $1`, projID).Scan(&s, &d); err != nil {
		t.Fatalf("read project dates: %v", err)
	}
	return s, d
}

// patchProject issues a PATCH/PUT against UpdateProject with the chi route param
// wired up, mirroring the DRI tests' request construction.
func updateProjectReq(t *testing.T, projID, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/projects/"+projID+"?workspace_id="+testWorkspaceID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req = withURLParam(req, "id", projID)
	testHandler.UpdateProject(w, req)
	return w
}

func ptrStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestCreateProject_WithDates creates a project carrying both calendar dates and
// asserts the response and the persisted row both round-trip them.
func TestCreateProject_WithDates(t *testing.T) {
	body := strings.NewReader(`{
		"title": "P with dates",
		"status": "planned",
		"priority": "none",
		"start_date": "2026-07-01",
		"due_date": "2026-07-31"
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
	if got := resp["start_date"]; got != "2026-07-01" {
		t.Fatalf("response start_date: got %v, want 2026-07-01", got)
	}
	if got := resp["due_date"]; got != "2026-07-31" {
		t.Fatalf("response due_date: got %v, want 2026-07-31", got)
	}
	projID, _ := resp["id"].(string)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projID)
	})
	s, d := projectDateRow(t, projID)
	if ptrStr(s) != "2026-07-01" || ptrStr(d) != "2026-07-31" {
		t.Fatalf("DB dates: start=%s due=%s, want 2026-07-01 / 2026-07-31", ptrStr(s), ptrStr(d))
	}
}

// TestUpdateProject_SetDates PATCHes both dates onto a dateless project and
// asserts the response and the persisted row.
func TestUpdateProject_SetDates(t *testing.T) {
	projID := insertHandlerTestProject(t, "set-dates")

	w := updateProjectReq(t, projID, `{"start_date":"2026-08-10","due_date":"2026-08-20"}`)
	if w.Code != 200 {
		t.Fatalf("UpdateProject set dates: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if got := resp["start_date"]; got != "2026-08-10" {
		t.Fatalf("response start_date: got %v, want 2026-08-10", got)
	}
	if got := resp["due_date"]; got != "2026-08-20" {
		t.Fatalf("response due_date: got %v, want 2026-08-20", got)
	}
	s, d := projectDateRow(t, projID)
	if ptrStr(s) != "2026-08-10" || ptrStr(d) != "2026-08-20" {
		t.Fatalf("DB dates: start=%s due=%s, want 2026-08-10 / 2026-08-20", ptrStr(s), ptrStr(d))
	}
}

// TestUpdateProject_DatesSurviveDRIWrite is the key regression: UpdateProject
// runs SetProjectDRI as a second write when dri_user_id is present. SetProjectDRI
// only SETs dri_user_id/updated_at, so its RETURNING row must still carry the
// just-written dates — otherwise the response would show the dates rolled back.
// Two angles:
//  1. Same request changes both dates and DRI -> response keeps the dates.
//  2. Dates set first, then a DRI-only PATCH -> dates remain untouched in DB.
func TestUpdateProject_DatesSurviveDRIWrite(t *testing.T) {
	projID := insertHandlerTestProject(t, "dates-vs-dri")
	driID := insertHandlerTestUser(t, "dri-dates@test.local")

	// (1) Single request: set dates AND dri together.
	w := updateProjectReq(t, projID,
		`{"start_date":"2026-09-01","due_date":"2026-09-30","dri_user_id":"`+driID+`"}`)
	if w.Code != 200 {
		t.Fatalf("UpdateProject dates+dri: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if got := resp["dri_user_id"]; got != driID {
		t.Fatalf("response dri_user_id: got %v, want %s", got, driID)
	}
	// These two assertions fail if SetProjectDRI's RETURNING row clobbers the
	// dates written milliseconds earlier by UpdateProject.
	if got := resp["start_date"]; got != "2026-09-01" {
		t.Fatalf("response start_date after DRI write: got %v, want 2026-09-01", got)
	}
	if got := resp["due_date"]; got != "2026-09-30" {
		t.Fatalf("response due_date after DRI write: got %v, want 2026-09-30", got)
	}
	s, d := projectDateRow(t, projID)
	if ptrStr(s) != "2026-09-01" || ptrStr(d) != "2026-09-30" {
		t.Fatalf("DB dates after dates+dri: start=%s due=%s, want 2026-09-01 / 2026-09-30", ptrStr(s), ptrStr(d))
	}

	// (2) Now change ONLY the DRI in a follow-up request. Dates must persist.
	driID2 := insertHandlerTestUser(t, "dri-dates2@test.local")
	w = updateProjectReq(t, projID, `{"dri_user_id":"`+driID2+`"}`)
	if w.Code != 200 {
		t.Fatalf("UpdateProject dri-only: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp = nil
	json.Unmarshal(w.Body.Bytes(), &resp)
	if got := resp["dri_user_id"]; got != driID2 {
		t.Fatalf("response dri_user_id after dri-only: got %v, want %s", got, driID2)
	}
	if got := resp["start_date"]; got != "2026-09-01" {
		t.Fatalf("response start_date after dri-only PATCH: got %v, want 2026-09-01", got)
	}
	if got := resp["due_date"]; got != "2026-09-30" {
		t.Fatalf("response due_date after dri-only PATCH: got %v, want 2026-09-30", got)
	}
	s, d = projectDateRow(t, projID)
	if ptrStr(s) != "2026-09-01" || ptrStr(d) != "2026-09-30" {
		t.Fatalf("DB dates after dri-only PATCH: start=%s due=%s, want 2026-09-01 / 2026-09-30", ptrStr(s), ptrStr(d))
	}
}

// TestUpdateProject_ClearDateExplicitNull asserts the null/omit contract:
// an explicit `"start_date": null` clears the column, while omitting a field
// leaves the existing value intact (default = preserve).
func TestUpdateProject_ClearDateExplicitNull(t *testing.T) {
	projID := insertHandlerTestProject(t, "clear-date")

	// Seed both dates.
	w := updateProjectReq(t, projID, `{"start_date":"2026-10-05","due_date":"2026-10-15"}`)
	if w.Code != 200 {
		t.Fatalf("seed dates: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if s, d := projectDateRow(t, projID); ptrStr(s) != "2026-10-05" || ptrStr(d) != "2026-10-15" {
		t.Fatalf("seed DB dates: start=%s due=%s", ptrStr(s), ptrStr(d))
	}

	// Explicit null clears start_date; due_date is omitted, so it must survive.
	w = updateProjectReq(t, projID, `{"start_date":null}`)
	if w.Code != 200 {
		t.Fatalf("clear start_date: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if got := resp["start_date"]; got != nil {
		t.Fatalf("response start_date after null: got %v, want nil", got)
	}
	if got := resp["due_date"]; got != "2026-10-15" {
		t.Fatalf("response due_date after start_date-null PATCH: got %v, want 2026-10-15 (omitted = preserve)", got)
	}
	s, d := projectDateRow(t, projID)
	if s != nil {
		t.Fatalf("DB start_date after explicit null: got %s, want NULL", ptrStr(s))
	}
	if ptrStr(d) != "2026-10-15" {
		t.Fatalf("DB due_date after start_date-null PATCH: got %s, want 2026-10-15 (omitted = preserve)", ptrStr(d))
	}

	// A PATCH that touches neither date (only title) must not disturb due_date.
	w = updateProjectReq(t, projID, `{"title":"renamed but dates untouched"}`)
	if w.Code != 200 {
		t.Fatalf("title-only PATCH: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	s, d = projectDateRow(t, projID)
	if s != nil {
		t.Fatalf("DB start_date after title-only PATCH: got %s, want NULL", ptrStr(s))
	}
	if ptrStr(d) != "2026-10-15" {
		t.Fatalf("DB due_date after title-only PATCH: got %s, want 2026-10-15 (omitted = preserve)", ptrStr(d))
	}
}
